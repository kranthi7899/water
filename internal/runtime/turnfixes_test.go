package runtime

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/decisions"
	"water/internal/store"
)

// countingDecisions counts Run calls, so a test can prove a path never
// reaches the (network- and model-backed) decision trigger.
type countingDecisions struct {
	n     atomic.Int64
	cards []*decisions.Card
	err   error
}

func (c *countingDecisions) Run(context.Context, time.Time) ([]*decisions.Card, error) {
	c.n.Add(1)
	return c.cards, c.err
}

// brokenApprovals is an approvals queue over a closed store: every read
// fails, while env.Store (and so the cached brief) keeps working.
func brokenApprovals(t *testing.T) *approvals.Queue {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "broken.db"))
	if err != nil {
		t.Fatal(err)
	}
	log, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	st.Close()
	return approvals.NewQueue(st, log)
}

// TestBriefAnswerFailsClosedOnSignalError: a cached brief may be built from
// external mail; if CachedBrief cannot recompute its signals, it must still
// report tainted rather than serve it un-tainted (Design §11.4/Risk 7 — the
// caller, internal/nervous's Handle, escalates the session on that bit).
func TestBriefAnswerFailsClosedOnSignalError(t *testing.T) {
	env, ctx := testEnv(t)
	day := startOfDay(env.now()).Format("2006-01-02")
	if err := env.Store.SetBrief(ctx, day, "cached brief"); err != nil {
		t.Fatal(err)
	}
	env.Approvals = brokenApprovals(t)

	text, tainted, ok, err := CachedBrief(ctx, env, day)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || text != "cached brief" {
		t.Fatalf("CachedBrief = %q, %v; want the cached brief", text, ok)
	}
	if !tainted {
		t.Fatal("tainted should be true when today's signals can't be recomputed")
	}
}

// TestCachedBriefDoesNotRunDecisions: reading back an already-cached brief
// is a store read, not a decision-trigger run (gate fetches and model calls
// per card).
func TestCachedBriefDoesNotRunDecisions(t *testing.T) {
	env, ctx := testEnv(t)
	cd := &countingDecisions{}
	env.Decisions = cd
	if _, err := ComputeAndCacheBrief(ctx, env); err != nil {
		t.Fatal(err)
	}
	before := cd.n.Load()
	day := startOfDay(env.now()).Format("2006-01-02")
	if _, _, ok, err := CachedBrief(ctx, env, day); err != nil || !ok {
		t.Fatalf("CachedBrief = ok=%v err=%v; want a cache hit", ok, err)
	}
	if after := cd.n.Load(); after != before {
		t.Fatalf("reading a cached brief ran the decision trigger %d time(s)", after-before)
	}
	if before > 1 {
		t.Fatalf("computing one brief ran the decision trigger %d times, want at most 1", before)
	}
}

// TestFailingDecisionSourceStillProducesABrief: open cards are an optional
// signal; a classification failure must not take the whole brief down.
func TestFailingDecisionSourceStillProducesABrief(t *testing.T) {
	env, ctx := testEnv(t)
	env.Decisions = &countingDecisions{err: errBoom}
	text, err := ComputeAndCacheBrief(ctx, env)
	if err != nil {
		t.Fatalf("brief failed on a decision-source error: %v", err)
	}
	if text == "" {
		t.Fatal("empty brief")
	}
}

// TestCachedBriefKeepsItsComputedTaint: a brief whose open cards came from
// untrusted content still taints the session when later served from cache,
// even though the cached ask no longer runs the decision trigger.
func TestCachedBriefKeepsItsComputedTaint(t *testing.T) {
	env, ctx := testEnv(t)
	env.Decisions = &countingDecisions{cards: []*decisions.Card{{ID: "c1", TypeID: "generic", Untrusted: true, Lead: "x"}}}
	if _, err := ComputeAndCacheBrief(ctx, env); err != nil {
		t.Fatal(err)
	}
	day := startOfDay(env.now()).Format("2006-01-02")
	_, got, ok, err := CachedBrief(ctx, env, day)
	if err != nil || !ok {
		t.Fatalf("CachedBrief = ok=%v err=%v; want a cache hit", ok, err)
	}
	if !got {
		t.Fatal("a cached brief built from an untrusted card did not taint the session")
	}
}

// TestBriefDoesNotUseTheWarmSession: the brief has its own system prompt and
// no tools, so running it through the chat's warm session would kill the
// chat's process (and its conversation) each time.
func TestBriefDoesNotUseTheWarmSession(t *testing.T) {
	env, ctx := testEnv(t)
	fk := backend.NewFake("fake")
	env.Backend = fk
	// A warm session whose CLI cannot start: if the brief went through it,
	// the call would fail instead of reaching the fake backend.
	env.Warm = backend.NewWarmSession(backend.WarmSessionConfig{Bin: filepath.Join(t.TempDir(), "no-such-claude")})
	defer env.Warm.Close()
	if _, err := ComputeAndCacheBrief(ctx, env); err != nil {
		t.Fatalf("brief went through the warm session: %v", err)
	}
	if fk.Calls() != 1 {
		t.Fatalf("backend calls = %d, want 1", fk.Calls())
	}
}

// TestRunTurnSystemPromptIsStableAcrossStateChanges: live state (today's
// events, the pending count) belongs in the turn, not the system prompt,
// because a changed system prompt restarts the warm session and loses the
// conversation.
func TestRunTurnSystemPromptIsStableAcrossStateChanges(t *testing.T) {
	env, ctx := testEnv(t)
	env.Manifest = testManifest(t)
	fk := backend.NewFake("fake")
	env.Backend = fk

	if _, err := ModelTurn(ctx, env, Turn{Channel: ChannelCLI, Prompt: "draft a note to dana"}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Approvals.Propose(ctx, approvals.Envelope{Action: "x.y", Payload: map[string]any{"a": 1}, Origin: "p0", Risk: "low"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ModelTurn(ctx, env, Turn{Channel: ChannelCLI, Prompt: "make the subject shorter"}, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	reqs := fk.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	if reqs[0].System != reqs[1].System {
		t.Fatalf("system prompt changed between turns:\n%q\nvs\n%q", reqs[0].System, reqs[1].System)
	}
	if !contains(reqs[1].Prompt, "Pending approvals: 1") || !contains(reqs[1].Prompt, "make the subject shorter") {
		t.Fatalf("turn prompt should carry the current state and the request: %q", reqs[1].Prompt)
	}
}

// The old TestFastPathLeavesActionRequestsToTheModel (keyword-trigger
// near-misses vs. matches) tested internal/runtime's deleted FastPath.
// Its original cases are preserved as internal/nervous/intents/embedded_test.go's
// TestLegacyFastPathCases (R-9), run against the real Tier 0 registry
// instead, and its positives/negatives also live in
// internal/nervous/eval/testdata/ceo_eval.yaml (R-7).
//
// A concurrent upstream commit (rebased in during R-12) added five more
// near-miss cases here after a review finding: FastPath's old substring
// triggers ("what's on my ...", "what's pending") over-matched an
// unrelated subject ("what's on my mind", "what's pending on the Acme
// deal?"). Verified by hand against the real committed intent files rather
// than re-added as a test, since Tier 0 can't have this bug by
// construction: matching is anchored (the whole normalized utterance must
// be consumed, so trailing words like "on the acme deal" can't be silently
// absorbed) and schedule.on_date's own "what's on ..." template requires
// the literal word "calendar" or "schedule", which none of these five
// phrases contain. See twins/ceo/intents/schedule_on_date.yaml and
// approvals_list.yaml.
