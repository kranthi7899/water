package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"water/internal/agentmail"
	"water/internal/backend"
	"water/internal/decisions"
	"water/internal/store"
)

var errBoom = errors.New("boom")

// fakeDecisionSource is a minimal runtime.DecisionSource stand-in for tests,
// so the brief's open-cards signal can be exercised without a real gate,
// registry or backend.
type fakeDecisionSource struct {
	cards []*decisions.Card
	err   error
}

func (f fakeDecisionSource) Run(context.Context, time.Time) ([]*decisions.Card, error) {
	return f.cards, f.err
}

func TestComputeAndCacheBriefReturnsCachedInstantlyWithNoBackendCall(t *testing.T) {
	env, ctx := testEnv(t)
	day := startOfDay(env.now()).Format("2006-01-02")
	if err := env.Store.SetBrief(ctx, day, "cached text"); err != nil {
		t.Fatal(err)
	}
	fk := backend.NewFake("fake")
	env.Backend = fk

	text, err := ComputeAndCacheBrief(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	if text != "cached text" {
		t.Fatalf("text = %q, want the cached text", text)
	}
	if fk.Calls() != 0 {
		t.Fatalf("backend called %d times on a cache hit, want 0", fk.Calls())
	}
}

func TestComputeAndCacheBriefComputesAndCachesOnFirstCall(t *testing.T) {
	env, ctx := testEnv(t)
	fk := backend.NewFake("fake")
	env.Backend = fk

	text, err := ComputeAndCacheBrief(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	if text == "" {
		t.Fatal("computed brief is empty")
	}
	if fk.Calls() != 1 {
		t.Fatalf("backend called %d times, want exactly 1", fk.Calls())
	}
	day := startOfDay(env.now()).Format("2006-01-02")
	cached, ok, err := env.Store.GetBrief(ctx, day)
	if err != nil || !ok || cached != text {
		t.Fatalf("cached=%q ok=%v err=%v, want %q", cached, ok, err, text)
	}
}

// TestComputeAndCacheBriefConcurrentCallersShareOneComputation is the
// compute-once guard: a background precompute and an on-demand ask racing
// for the same day must never both call the model.
func TestComputeAndCacheBriefConcurrentCallersShareOneComputation(t *testing.T) {
	env, ctx := testEnv(t)
	fk := backend.NewFake("fake")
	fk.Reply = func(backend.Request) string {
		time.Sleep(30 * time.Millisecond) // widen the race window
		return "the brief"
	}
	env.Backend = fk

	const n = 4
	var wg sync.WaitGroup
	texts := make([]string, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			texts[i], errs[i] = ComputeAndCacheBrief(ctx, env)
		}()
	}
	wg.Wait()

	for i := range texts {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if texts[i] != "the brief" {
			t.Fatalf("caller %d text = %q, want %q", i, texts[i], "the brief")
		}
	}
	if calls := fk.Calls(); calls != 1 {
		t.Fatalf("backend called %d times across %d concurrent callers, want exactly 1", calls, n)
	}
}

// TestBriefAnswerPropagatesTaintForExternalMessages checks that a fast-path
// brief answer escalates taint (via Env.OnTaint) exactly when its signals
// include an External record, same rule as StateSummary.
func TestBriefAnswerPropagatesTaintForExternalMessages(t *testing.T) {
	env, ctx := testEnv(t)
	var taintCalled, gotTaint bool
	env.OnTaint = func(tainted bool) { taintCalled, gotTaint = true, tainted }

	msg := &store.Message{
		Meta:    store.Meta{Source: "gmail", SourceID: "m1", External: true, CreatedAt: env.now().Add(-time.Hour)},
		From:    "dana@acme.com",
		Subject: "Question about the deck",
		Body:    "Can you review this by tomorrow?",
	}
	if err := env.Store.Upsert(ctx, msg); err != nil {
		t.Fatal(err)
	}

	if _, ok := FastPath(ctx, env, "what's my morning brief"); !ok {
		t.Fatal("expected a fast-path match")
	}
	if !taintCalled {
		t.Fatal("OnTaint was never called")
	}
	if !gotTaint {
		t.Fatal("taint should be true: an External message was in the brief's signals")
	}
}

// TestBriefIgnoresOldMailIngestedToday is the review finding: a prefetch or
// model search pulls a months-old message into the store today (created_at
// = now). It was sent long ago, so it is not "new since yesterday" and must
// not be flagged as needing a reply.
func TestBriefIgnoresOldMailIngestedToday(t *testing.T) {
	env, ctx := testEnv(t)
	old := &store.Message{
		Meta:    store.Meta{Source: "gmail", SourceID: "old", External: true, CreatedAt: env.now()},
		From:    "dana@acme.com",
		Subject: "can you review the deck?",
		SentAt:  env.now().AddDate(0, 0, -90),
	}
	fresh := &store.Message{
		Meta:    store.Meta{Source: "gmail", SourceID: "fresh", External: true, CreatedAt: env.now()},
		From:    "lee@acme.com",
		Subject: "urgent: sign today",
		SentAt:  env.now().Add(-time.Hour),
	}
	for _, m := range []*store.Message{old, fresh} {
		if err := env.Store.Upsert(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	sig, _, err := computeBriefSignals(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	if sig.NewMessages != 1 || sig.DistinctSenders != 1 {
		t.Fatalf("NewMessages=%d DistinctSenders=%d, want 1/1 (only the message sent in the window)", sig.NewMessages, sig.DistinctSenders)
	}
	if len(sig.NeedsAttention) != 1 || sig.NeedsAttention[0].SourceID != "fresh" {
		t.Fatalf("NeedsAttention = %+v, want only the fresh message", sig.NeedsAttention)
	}
}

// TestBriefSignalsRankOpenCardsAndOmitTheSectionWhenEmpty covers both the
// morning brief's new signal (task 3) and its explicit "absent, not an
// empty section" rule: no Env.Decisions at all renders no cards section,
// while a Decisions source that finds cards renders them ranked by severity
// (decisions.Rank), highest first.
func TestBriefSignalsRankOpenCardsAndOmitTheSectionWhenEmpty(t *testing.T) {
	env, ctx := testEnv(t)

	sig, _, err := computeBriefSignals(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	if sig.OpenCards != nil {
		t.Fatalf("OpenCards = %v, want nil with no Env.Decisions set", sig.OpenCards)
	}
	if strings.Contains(renderBriefSignals(sig), "Open decision cards") {
		t.Fatal("rendered signals should not mention open decision cards when there are none")
	}

	low := &decisions.Card{ID: "card-low", TypeID: "generic", Severity: 1, Lead: "Low severity item", Readiness: decisions.Ready}
	high := &decisions.Card{ID: "card-high", TypeID: "budget_request", Severity: 3, Lead: "High severity item", Readiness: decisions.MissingInfo}
	env.Decisions = fakeDecisionSource{cards: []*decisions.Card{low, high}}

	sig.OpenCards, _, err = openCardSignals(ctx, env, env.now())
	if err != nil {
		t.Fatal(err)
	}
	if len(sig.OpenCards) != 2 || sig.OpenCards[0].ID != "card-high" || sig.OpenCards[1].ID != "card-low" {
		t.Fatalf("OpenCards = %+v, want [card-high, card-low]", sig.OpenCards)
	}
	rendered := renderBriefSignals(sig)
	if !strings.Contains(rendered, "High severity item") || !strings.Contains(rendered, "Low severity item") {
		t.Fatalf("rendered signals missing a card's lead: %q", rendered)
	}
	if strings.Index(rendered, "High severity item") > strings.Index(rendered, "Low severity item") {
		t.Fatalf("higher-severity card should render before the lower one: %q", rendered)
	}
}

// TestBriefSignalsPropagateUntrustedCards checks that an Untrusted open card
// escalates the brief's taint, the same rule External events/messages get.
func TestBriefSignalsPropagateUntrustedCards(t *testing.T) {
	env, ctx := testEnv(t)
	env.Decisions = fakeDecisionSource{cards: []*decisions.Card{{ID: "card-1", TypeID: "generic", Untrusted: true}}}

	_, tainted, err := openCardSignals(ctx, env, env.now())
	if err != nil {
		t.Fatal(err)
	}
	if !tainted {
		t.Fatal("an Untrusted open card should taint the brief's signals")
	}
}

// TestBriefSaysWhenDecisionCardsAreUnavailable confirms a failing Decisions
// source is surfaced, not hidden: openCardSignals returns its error, and the
// brief built without the cards says they were unavailable rather than
// silently omitting the section as if there were none.
func TestBriefSaysWhenDecisionCardsAreUnavailable(t *testing.T) {
	env, ctx := testEnv(t)
	env.Decisions = fakeDecisionSource{err: errBoom}
	if _, _, err := openCardSignals(ctx, env, env.now()); err == nil {
		t.Fatal("expected openCardSignals to surface the Decisions source's error")
	}

	fk := backend.NewFake("fake")
	env.Backend = fk
	if _, err := ComputeAndCacheBrief(ctx, env); err != nil {
		t.Fatal(err)
	}
	reqs := fk.Requests()
	if len(reqs) != 1 || !strings.Contains(reqs[0].Prompt, "Open decision cards: unavailable") {
		t.Fatalf("brief signal block should note the cards were unavailable: %+v", reqs)
	}
}

func TestBriefAnswerNoTaintWhenNothingExternal(t *testing.T) {
	env, ctx := testEnv(t)
	var taintCalled, gotTaint bool
	env.OnTaint = func(tainted bool) { taintCalled, gotTaint = true, tainted }

	if _, ok := FastPath(ctx, env, "what's my morning brief"); !ok {
		t.Fatal("expected a fast-path match")
	}
	if !taintCalled {
		t.Fatal("OnTaint was never called")
	}
	if gotTaint {
		t.Fatal("taint should be false: nothing in the signals is External")
	}
}

// TestBriefIncludesAgentMailSignalAndTaintsIt confirms internal/agentmail's
// rolling agent-directed list reaches the brief as a plain, code-computed
// signal (never a live call: computeBriefSignals only reads what the
// watcher already persisted via RecentSignals), and that its presence
// taints the brief like any other externally-sourced content.
func TestBriefIncludesAgentMailSignalAndTaintsIt(t *testing.T) {
	env, ctx := testEnv(t)
	sigs := []agentmail.Signal{{From: "notify@service.com", Subject: "Your weekly digest", SeenAt: env.now()}}
	b, err := json.Marshal(sigs)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Store.SetCursor(ctx, agentmail.DefaultSignalKey, string(b)); err != nil {
		t.Fatal(err)
	}

	sig, tainted, err := computeBriefSignals(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	if !tainted {
		t.Fatal("a non-empty agent-mail signal must taint the brief")
	}
	if len(sig.AgentMail) != 1 || sig.AgentMail[0].Subject != "Your weekly digest" {
		t.Fatalf("AgentMail = %+v, want the one recorded signal", sig.AgentMail)
	}

	fk := backend.NewFake("fake")
	fk.Reply = func(backend.Request) string { return "ok" }
	env.Backend = fk
	if _, ok := FastPath(ctx, env, "what's my morning brief"); !ok {
		t.Fatal("expected a fast-path match")
	}
	reqs := fk.Requests()
	if len(reqs) != 1 || !strings.Contains(reqs[0].Prompt, "Your weekly digest") {
		t.Fatalf("brief signal block should carry the agent-mail signal: %+v", reqs)
	}
}

// TestBriefOmitsAgentMailSectionWhenThereIsNothingToShow confirms the
// section is simply absent, not an empty header, when RecentSignals has
// nothing (the watcher never ran, or the agent mailbox isn't connected).
func TestBriefOmitsAgentMailSectionWhenThereIsNothingToShow(t *testing.T) {
	env, ctx := testEnv(t)
	sig, tainted, err := computeBriefSignals(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	if tainted {
		t.Fatal("no agent-mail signal should not taint the brief")
	}
	if len(sig.AgentMail) != 0 {
		t.Fatalf("AgentMail = %+v, want none", sig.AgentMail)
	}
	if strings.Contains(renderBriefSignals(sig), "addressed to the agent") {
		t.Fatal("the agent-mail section must not render when there is nothing to show")
	}
}
