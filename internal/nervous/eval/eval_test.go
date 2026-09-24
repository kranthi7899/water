package eval

import (
	"context"
	"embed"
	"math"
	"testing"
)

//go:embed testdata/ceo_eval_t1.yaml
var t1FS embed.FS

func loadT1Supplement(t *testing.T) []Case {
	t.Helper()
	b, err := t1FS.ReadFile("testdata/ceo_eval_t1.yaml")
	if err != nil {
		t.Fatalf("read ceo_eval_t1.yaml: %v", err)
	}
	cases, err := parseCaseFile(b)
	if err != nil {
		t.Fatalf("parse ceo_eval_t1.yaml: %v", err)
	}
	return cases
}

// ---- harness mechanics, against a trivial fake tier ----

type fakeTier struct {
	// answers maps utterance -> (intent, slots); anything absent escalates.
	answers map[string]fakeAnswer
}

type fakeAnswer struct {
	intent string
	slots  map[string]string
}

func (f *fakeTier) Try(ctx context.Context, utterance string, pending int) (bool, string, map[string]string, bool, error) {
	a, ok := f.answers[utterance]
	if !ok {
		return false, "", nil, true, nil
	}
	return true, a.intent, a.slots, false, nil
}

func TestRunArithmetic(t *testing.T) {
	tier := &fakeTier{answers: map[string]fakeAnswer{
		"a": {intent: "x.y", slots: map[string]string{"s": "1"}}, // correct positive
		"b": {intent: "x.z"},                                     // wrong intent on a positive -> false accept
		"c": {intent: "x.y", slots: map[string]string{"s": "2"}}, // wrong slot on a positive -> false accept
		"e": {intent: "x.y"},                                     // negative wrongly answered -> false accept
		// "d" and "f" are absent -> escalate
	}}
	cases := []Case{
		{Utterance: "a", Intent: "x.y", Class: "positive", Slots: map[string]string{"s": "1"}},
		{Utterance: "b", Intent: "x.y", Class: "positive"},
		{Utterance: "c", Intent: "x.y", Class: "positive", Slots: map[string]string{"s": "1"}},
		{Utterance: "d", Intent: "x.y", Class: "positive"},
		{Utterance: "e", Intent: "none", Class: "ood"},
		{Utterance: "f", Intent: "none", Class: "reasoning"},
	}
	r := Run(context.Background(), tier, DefaultFixture(), cases)

	if r.N != 6 {
		t.Fatalf("N = %d, want 6", r.N)
	}
	if r.Positives != 4 || r.Negatives != 2 {
		t.Fatalf("Positives=%d Negatives=%d, want 4/2", r.Positives, r.Negatives)
	}
	// Only "a" is a true hit (correct intent + correct slots).
	if got, want := r.HitRate, 1.0/4.0; math.Abs(got-want) > 1e-9 {
		t.Fatalf("HitRate = %v, want %v", got, want)
	}
	// 3 false accepts (b, c, e) out of 6 total cases.
	if got, want := r.FalseAcceptRate, 3.0/6.0; math.Abs(got-want) > 1e-9 {
		t.Fatalf("FalseAcceptRate = %v, want %v", got, want)
	}
	if len(r.FalseAccepts) != 3 {
		t.Fatalf("len(FalseAccepts) = %d, want 3", len(r.FalseAccepts))
	}
	// 2 escalations (d, f) out of 6.
	if got, want := r.EscalationRate, 2.0/6.0; math.Abs(got-want) > 1e-9 {
		t.Fatalf("EscalationRate = %v, want %v", got, want)
	}
	if r.ReasoningAnswered != 0 {
		t.Fatalf("ReasoningAnswered = %d, want 0 (f escalated, was not answered)", r.ReasoningAnswered)
	}
}

func TestRunReasoningAnsweredCounted(t *testing.T) {
	tier := &fakeTier{answers: map[string]fakeAnswer{
		"reasoning case": {intent: "x.y"},
	}}
	cases := []Case{{Utterance: "reasoning case", Intent: "none", Class: "reasoning"}}
	r := Run(context.Background(), tier, DefaultFixture(), cases)
	if r.ReasoningAnswered != 1 {
		t.Fatalf("ReasoningAnswered = %d, want 1", r.ReasoningAnswered)
	}
	if len(r.FalseAccepts) != 1 {
		t.Fatalf("answering a reasoning/negative case must count as a false accept, got %d", len(r.FalseAccepts))
	}
}

func TestSlotsMatchLenientOnMissingKeys(t *testing.T) {
	// A tier that doesn't report a slot at all is not penalized for it;
	// only a present-but-wrong value is a mismatch.
	if !slotsMatch(map[string]string{}, map[string]string{"when": "today"}) {
		t.Fatal("missing slot key should not count as a mismatch")
	}
	if slotsMatch(map[string]string{"when": "tomorrow"}, map[string]string{"when": "today"}) {
		t.Fatal("a present, wrong value must count as a mismatch")
	}
	if !slotsMatch(map[string]string{"when": "Today"}, map[string]string{"when": "today"}) {
		t.Fatal("comparison should be case-insensitive")
	}
}

// ---- Wilson bound ----

func TestWilson95Upper(t *testing.T) {
	cases := []struct {
		successes, n int
		want         float64
		tol          float64
	}{
		{0, 180, 0.0204, 0.001},
		{0, 100, 0.0362, 0.001},
		{1, 100, 0.0532, 0.002},
		{5, 100, 0.1114, 0.002},
	}
	for _, c := range cases {
		got := wilson95Upper(c.successes, c.n)
		if math.Abs(got-c.want) > c.tol {
			t.Errorf("wilson95Upper(%d, %d) = %v, want ~%v (tol %v)", c.successes, c.n, got, c.want, c.tol)
		}
	}
}

// ---- eval set shape ----

func TestEvalSetMeetsMinimums(t *testing.T) {
	cases, err := LoadCEOEval()
	if err != nil {
		t.Fatalf("LoadCEOEval: %v", err)
	}
	t1 := loadT1Supplement(t)

	if len(cases) < 276 {
		t.Errorf("ceo_eval.yaml has %d cases, want >= 276", len(cases))
	}

	readIntents := []string{
		"schedule.on_date", "schedule.next_event", "schedule.free_time",
		"mail.latest", "mail.unread_count", "mail.latest_from",
		"brief.today", "approvals.list", "approvals.respond",
		"control.stop", "status.overview", "help.intents",
	}
	writeIntents := []string{"calendar.create_event", "calendar.move_event", "mail.draft_reply", "mail.send_reply"}

	byIntent := map[string]int{}
	byClass := map[string]int{}
	for _, c := range cases {
		if c.isPositive() {
			byIntent[c.Intent]++
		}
		byClass[c.Class]++
	}

	for _, id := range readIntents {
		if byIntent[id] < 12 {
			t.Errorf("read intent %q has %d paraphrases, want >= 12", id, byIntent[id])
		}
	}
	for _, id := range writeIntents {
		if byIntent[id] < 8 {
			t.Errorf("write intent %q has %d paraphrases, want >= 8", id, byIntent[id])
		}
	}

	minClass := map[string]int{
		"reasoning":          30,
		"multi_clause":       10,
		"legacy_near_miss":   5,
		"action_unsupported": 1,
		"ood":                1,
		"yesno_zero_pending": 2,
	}
	for class, min := range minClass {
		if byClass[class] < min {
			t.Errorf("class %q has %d cases, want >= %d", class, byClass[class], min)
		}
	}

	negatives := byClass["reasoning"] + byClass["multi_clause"] + byClass["legacy_near_miss"] +
		byClass["action_unsupported"] + byClass["ood"] + byClass["yesno_zero_pending"]
	if negatives < 100 {
		t.Errorf("total negatives = %d, want >= 100", negatives)
	}

	combinedN := len(cases) + len(t1)
	if combinedN < 400 {
		t.Errorf("combined ceo_eval.yaml + ceo_eval_t1.yaml has %d cases, want >= 400 for the live Tier 1 eval", combinedN)
	}
}

func TestLegacyNearMissesVerbatim(t *testing.T) {
	want := []string{
		"help me schedule a meeting with the calendar team",
		"please schedule a meeting with bob tomorrow",
		"can you approve this for me",
		"tell me about the weather",
		"write a brief history of the company",
	}
	cases, err := LoadCEOEval()
	if err != nil {
		t.Fatalf("LoadCEOEval: %v", err)
	}
	have := map[string]bool{}
	for _, c := range cases {
		if c.Class == "legacy_near_miss" {
			have[c.Utterance] = true
		}
	}
	for _, u := range want {
		if !have[u] {
			t.Errorf("legacy near-miss %q missing from ceo_eval.yaml", u)
		}
	}
}

func TestNegativesExcludesPositiveAndWrite(t *testing.T) {
	for _, c := range Negatives() {
		if c.Class == "positive" || c.Class == "write" {
			t.Fatalf("Negatives() returned a %s case: %q", c.Class, c.Utterance)
		}
		if c.isPositive() {
			t.Fatalf("Negatives() returned a case with a real intent: %q -> %q", c.Utterance, c.Intent)
		}
	}
	if len(Negatives()) < 100 {
		t.Fatalf("Negatives() returned %d cases, want >= 100", len(Negatives()))
	}
}

// The legacy baseline (legacyTier wrapping internal/runtime.FastPath) was a
// one-time measurement, taken in R-7 before internal/runtime.FastPath
// existed as deleted code, specifically so Tier 0's own acceptance
// criterion ("hit rate at least the legacy baseline plus 20 points") would
// have a real number to compare against. That number is recorded
// permanently in docs/slices/R.md's "Baseline" section. Task R-12 deletes
// FastPath itself (its replacement, Tier 0, is what this eval harness now
// measures instead), so legacy.go and this test are gone with it — there is
// nothing left to re-measure, and the recorded numbers don't change.
