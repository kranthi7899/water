package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"water/internal/nervous/intents"
	"water/internal/nervous/reflex"
	"water/internal/nervous/sidecar"
	"water/internal/twins"
)

// liveEvalPrereqs decides whether the live Tier 1 eval should run, and
// where. It is a standalone function (rather than inlined into
// TestTier1Live) precisely so its two independent skip conditions —
// WATER_EVAL_LIVE unset, and the model file missing — can each be tested
// directly below, without needing to invoke and then inspect a real
// t.Skip from inside another test.
func liveEvalPrereqs() (modelPath, home string, skip bool, reason string) {
	if os.Getenv("WATER_EVAL_LIVE") != "1" {
		return "", "", true, "WATER_EVAL_LIVE is not set to 1"
	}
	home = os.Getenv("WATER_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", "", true, "cannot resolve a home directory to default WATER_HOME"
		}
		home = filepath.Join(h, ".water")
	}
	modelPath = filepath.Join(home, "models", sidecar.ModelFileName)
	if _, err := os.Stat(modelPath); err != nil {
		return "", "", true, fmt.Sprintf("functiongemma model not found at %s (%v); run `water model pull functiongemma --accept-gemma-terms` first", modelPath, err)
	}
	return modelPath, home, false, ""
}

// TestTier1Live runs the real, end-to-end Tier 1 live eval: a real
// sidecar.Supervisor over a real llama-server process serving the actually
// downloaded FunctionGemma GGUF, driven through the combined
// ceo_eval.yaml + ceo_eval_t1.yaml case sets over the real cascade
// (eligibility -> Tier 0 -> Tier 1 on a clean no_match), writing the
// resulting eval-gate record to $WATER_HOME/router/tier1_eval.json.
//
// This is the mechanism `water route eval --tier1`
// (internal/cli/cmd_route.go) drives too. It never runs in a normal
// `go test`/CI pass and never fails the suite just because nobody has
// pulled the model yet — see liveEvalPrereqs' two independent skip
// conditions, each proven separately below.
func TestTier1Live(t *testing.T) {
	modelPath, home, skip, reason := liveEvalPrereqs()
	if skip {
		t.Skip(reason)
	}

	reg := liveEvalRegistry(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	rec, report, err := RunTier1Live(ctx, Tier1LiveOptions{
		Home:      home,
		ModelPath: modelPath,
		Registry:  reg,
	})
	if err != nil {
		t.Fatalf("RunTier1Live: %v", err)
	}

	t.Logf("Tier 1 live eval: n=%d false_accepts=%d fa_rate=%.4f wilson95_upper=%.4f warm_p95_ms=%d",
		rec.N, rec.FalseAccepts, rec.FARate, rec.Wilson95Upper, rec.WarmP95Ms)
	t.Logf("report: hit_rate=%.3f intent_acc=%.3f escalation_rate=%.3f reasoning_answered=%d",
		report.HitRate, report.IntentAcc, report.EscalationRate, report.ReasoningAnswered)

	if report.ReasoningAnswered != 0 {
		t.Errorf("ReasoningAnswered = %d, want 0 (Tier 1 must never answer a reasoning/multi_clause case)", report.ReasoningAnswered)
	}

	if ok, reason := rec.Check(rec.ModelSHA256, rec.RegistryHash); !ok {
		t.Errorf("eval gate would not pass (reason=%s): fa_rate=%.4f wilson95_upper=%.4f n=%d warm_p95_ms=%d",
			reason, rec.FARate, rec.Wilson95Upper, rec.N, rec.WarmP95Ms)
	}

	got, err := sidecar.ReadEvalRecord(home)
	if err != nil {
		t.Fatalf("ReadEvalRecord after RunTier1Live: %v", err)
	}
	if got.N != rec.N || got.ModelSHA256 != rec.ModelSHA256 {
		t.Fatalf("record on disk = %+v, want it to match what RunTier1Live returned (%+v)", got, rec)
	}
}

// TestTier1LiveSkipsWithoutEnvVar proves the first of liveEvalPrereqs' two
// independent skip conditions in isolation: WATER_EVAL_LIVE unset must
// skip, regardless of whether a model happens to be present.
func TestTier1LiveSkipsWithoutEnvVar(t *testing.T) {
	t.Setenv("WATER_EVAL_LIVE", "")
	_, _, skip, reason := liveEvalPrereqs()
	if !skip {
		t.Fatal("liveEvalPrereqs did not skip with WATER_EVAL_LIVE unset")
	}
	if reason == "" {
		t.Fatal("expected a non-empty skip reason")
	}
}

// TestTier1LiveSkipsWithoutModelFile proves the second independent skip
// condition: WATER_EVAL_LIVE=1 alone is not enough — a missing model file
// under WATER_HOME must also skip cleanly.
func TestTier1LiveSkipsWithoutModelFile(t *testing.T) {
	t.Setenv("WATER_EVAL_LIVE", "1")
	home := t.TempDir()
	t.Setenv("WATER_HOME", home)

	modelPath := filepath.Join(home, "models", sidecar.ModelFileName)
	if _, err := os.Stat(modelPath); err == nil {
		t.Fatal("test setup: model file must not exist in a fresh temp dir")
	}

	_, _, skip, reason := liveEvalPrereqs()
	if !skip {
		t.Fatal("liveEvalPrereqs did not skip with the model file absent")
	}
	if reason == "" {
		t.Fatal("expected a non-empty skip reason")
	}
}

func liveEvalRegistry(t *testing.T) *intents.Registry {
	t.Helper()
	m, err := twins.Parse([]byte(`
id: testtwin
name: Test twin
usage: {window: 5h, model_calls: 200, auto_model_calls: 40}
connectors:
  - name: gcal
    functions:
      - {name: list_events, level: R}
  - name: gmail
    functions:
      - {name: list_messages, level: R}
`))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	fsys := fstest.MapFS{
		"twins/testtwin/intents/_shared.yaml": {Data: []byte(`
rules: {}
skip_words: [please, hey, um, uh, just, quickly]
deny_words: [cancel, delete, reschedule, send, approve]
escalate_words: [should, why, because, prioritize, summarize, plan, draft, compare]
clause_joiners: ["and then", "and also"]
`)},
		"twins/testtwin/intents/schedule_on_date.yaml": {Data: []byte(`
id: schedule.on_date
description: Events on a given day
function: store.calendar_events
slots:
  when: {type: daterange, default: today}
templates:
  - "(what's|what is|what have i got) on [my] (calendar|schedule) [for] {when}"
  - "what am i doing {when}"
escalate_if: [slot_unresolved, ambiguous_match]
reflex_eligible: true
tests:
  - {utterance: "what am i doing tuesday", intent: schedule.on_date, slots: {when: tuesday}}
  - {utterance: "gibberish nonsense", intent: "none"}
`)},
		"twins/testtwin/intents/mail_latest.yaml": {Data: []byte(`
id: mail.latest
description: The most recent email(s)
function: store.latest_messages
slots:
  n: {type: count, default: "5", min: 1, max: 20}
templates:
  - "show me [my] last {n} emails"
  - "what's [my] latest email"
escalate_if: [slot_unresolved, ambiguous_match]
reflex_eligible: true
tests:
  - {utterance: "show me my last 2 emails", intent: mail.latest, slots: {n: "2"}}
  - {utterance: "gibberish nonsense", intent: "none"}
`)},
	}
	reg, err := intents.LoadRegistry(fsys, m, intents.Functions{Read: reflex.Specs()}, intents.LoadOptions{})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	return reg
}
