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
// external mail; if the fast path cannot recompute its signals, it must
// still escalate the session rather than serve it un-tainted.
func TestBriefAnswerFailsClosedOnSignalError(t *testing.T) {
	env, ctx := testEnv(t)
	day := startOfDay(env.now()).Format("2006-01-02")
	if err := env.Store.SetBrief(ctx, day, "cached brief"); err != nil {
		t.Fatal(err)
	}
	env.Approvals = brokenApprovals(t)
	var called, got bool
	env.OnTaint = func(tainted bool) { called, got = true, tainted }

	text, ok := FastPath(ctx, env, "is my morning brief ready")
	if !ok || text != "cached brief" {
		t.Fatalf("FastPath = %q, %v; want the cached brief", text, ok)
	}
	if !called || !got {
		t.Fatalf("OnTaint called=%v tainted=%v; want called with true when signals fail", called, got)
	}
}

// TestCachedBriefFastPathDoesNotRunDecisions: asking for an already-cached
// brief is a store read, not a decision-trigger run (gate fetches and
// model calls per card).
func TestCachedBriefFastPathDoesNotRunDecisions(t *testing.T) {
	env, ctx := testEnv(t)
	cd := &countingDecisions{}
	env.Decisions = cd
	if _, err := ComputeAndCacheBrief(ctx, env); err != nil {
		t.Fatal(err)
	}
	before := cd.n.Load()
	if _, ok := FastPath(ctx, env, "is my morning brief ready"); !ok {
		t.Fatal("expected a fast-path match")
	}
	if after := cd.n.Load(); after != before {
		t.Fatalf("cached-brief fast path ran the decision trigger %d time(s)", after-before)
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
	var got bool
	env.OnTaint = func(tainted bool) { got = got || tainted }
	if _, ok := FastPath(ctx, env, "is my morning brief ready"); !ok {
		t.Fatal("expected a fast-path match")
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

	RunTurn(ctx, env, Turn{Channel: ChannelCLI, Prompt: "draft a note to dana"}, func(Event) {})
	if _, err := env.Approvals.Propose(ctx, approvals.Envelope{Action: "x.y", Payload: map[string]any{"a": 1}, Origin: "p0", Risk: "low"}); err != nil {
		t.Fatal(err)
	}
	RunTurn(ctx, env, Turn{Channel: ChannelCLI, Prompt: "make the subject shorter"}, func(Event) {})

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

// TestFastPathLeavesActionRequestsToTheModel: prompts that contain a fast
// path trigger phrase but ask for something else must reach the model.
func TestFastPathLeavesActionRequestsToTheModel(t *testing.T) {
	env, ctx := testEnv(t)
	for _, p := range []string{
		"Reschedule my meetings with Bob to Friday",
		"cancel my meetings tomorrow",
		"Draft a reply about the pending approval from legal",
		"Summarize my brief for the board deck",
		"What's on my calendar next week",
		"what's on my calendar on thursday",
		"move my 3pm meeting on my calendar to 4",
		// Open-ended triggers followed by a different subject (review finding).
		"what's pending on the Acme deal?",
		"what's on my mind",
		"what's on my reading list",
		"whats pending with legal",
		"what's on my plate for Acme",
	} {
		if _, ok := FastPath(ctx, env, p); ok {
			t.Errorf("FastPath(%q) answered from the fast path; it must fall through to the model", p)
		}
	}
	for _, p := range []string{
		"what's on my calendar today", "any pending approvals", "what's my morning brief",
		"do I have any meetings tomorrow", "is my brief ready", "what needs my approval",
		"what's pending", "so what's pending for me right now?", "what's on my plate today",
		"what is on my schedule tomorrow",
	} {
		if _, ok := FastPath(ctx, env, p); !ok {
			t.Errorf("FastPath(%q) should still match", p)
		}
	}
}
