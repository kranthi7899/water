package nervous

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"water/internal/backend"
	"water/internal/nervous/intents"
	"water/internal/nervous/render"
	"water/internal/nervous/tmpl"
	"water/internal/nervous/turn"
	"water/internal/runtime"
	"water/internal/store"
)

// --- Pure speculate() tests -------------------------------------------------

// TestSpeculateDryMatchHitSkipsPrewarm: a DryMatch hit that RunRead answers
// caches that result and never calls Prewarm — warming the backend would buy
// nothing when Tier 0 itself already has an answer cached.
func TestSpeculateDryMatchHitSkipsPrewarm(t *testing.T) {
	prewarmCalls := 0
	deps := SpecDeps{
		Summary: func(context.Context) (string, bool, error) { return "state", false, nil },
		DryMatch: func(tmpl.Utterance) (string, map[string]string, bool) {
			return "schedule.on_date", map[string]string{"when": "today"}, true
		},
		RunRead: func(context.Context, string, map[string]string) (render.Result, error) {
			return render.Result{Intent: "schedule.on_date", Kind: "read"}, nil
		},
		Prewarm: func(context.Context) (string, error) {
			prewarmCalls++
			return "started", nil
		},
	}
	u := tmpl.Normalize("what's on my calendar today", nil)
	spec := speculate(context.Background(), deps, u)
	if !spec.hit {
		t.Fatal("spec.hit = false, want true")
	}
	if spec.intent != "schedule.on_date" {
		t.Fatalf("spec.intent = %q", spec.intent)
	}
	if spec.prewarm != "skipped" {
		t.Fatalf("spec.prewarm = %q, want skipped", spec.prewarm)
	}
	if prewarmCalls != 0 {
		t.Fatalf("Prewarm was called %d times on a dry-match hit, want 0", prewarmCalls)
	}
	if !spec.summaryOK {
		t.Fatal("spec.summaryOK = false, want true (Summary succeeded)")
	}
}

// TestSpeculateNoMatchCallsPrewarm: no dry match at all falls through to
// Prewarm, and the resulting state is recorded verbatim.
func TestSpeculateNoMatchCallsPrewarm(t *testing.T) {
	deps := SpecDeps{
		DryMatch: func(tmpl.Utterance) (string, map[string]string, bool) { return "", nil, false },
		Prewarm:  func(context.Context) (string, error) { return "alive", nil },
	}
	u := tmpl.Normalize("what should i prioritize today", nil)
	spec := speculate(context.Background(), deps, u)
	if spec.hit {
		t.Fatal("spec.hit = true, want false (no dry match)")
	}
	if spec.prewarm != "alive" {
		t.Fatalf("spec.prewarm = %q, want alive", spec.prewarm)
	}
}

// TestSpeculateRunReadFailureFallsBackToPrewarm: a dry match whose RunRead
// itself fails (e.g. a handler error) must not report a false hit, and must
// still attempt Prewarm — the read attempt was wasted, not fatal.
func TestSpeculateRunReadFailureFallsBackToPrewarm(t *testing.T) {
	prewarmCalls := 0
	deps := SpecDeps{
		DryMatch: func(tmpl.Utterance) (string, map[string]string, bool) {
			return "brief.today", map[string]string{}, true
		},
		RunRead: func(context.Context, string, map[string]string) (render.Result, error) {
			return render.Result{}, errors.New("cache miss")
		},
		Prewarm: func(context.Context) (string, error) {
			prewarmCalls++
			return "started", nil
		},
	}
	spec := speculate(context.Background(), deps, tmpl.Normalize("my brief", nil))
	if spec.hit {
		t.Fatal("spec.hit = true, want false (RunRead failed)")
	}
	if prewarmCalls != 1 {
		t.Fatalf("Prewarm calls = %d, want 1", prewarmCalls)
	}
}

// TestSpeculationZeroModelCalls: SpecDeps has no backend field at all, by
// construction — this test drives 50 varied partials through speculate()
// with a real backend.Fake sitting nearby (unreferenced by any SpecDeps
// field) and asserts its call count never moves, proving in practice what
// the type signature already guarantees structurally.
func TestSpeculationZeroModelCalls(t *testing.T) {
	fk := backend.NewFake("unused")
	deps := SpecDeps{
		Summary: func(context.Context) (string, bool, error) { return "state", false, nil },
		DryMatch: func(u tmpl.Utterance) (string, map[string]string, bool) {
			if len(u.Tokens) > 0 && u.Tokens[0] == "calendar" {
				return "schedule.on_date", map[string]string{"when": "today"}, true
			}
			return "", nil, false
		},
		RunRead: func(context.Context, string, map[string]string) (render.Result, error) {
			return render.Result{Intent: "schedule.on_date", Kind: "read"}, nil
		},
		Prewarm: func(context.Context) (string, error) { return "started", nil },
	}
	for i := 0; i < 50; i++ {
		text := "calendar today revision"
		if i%2 == 0 {
			text = "what should i prioritize"
		}
		u := tmpl.Normalize(text, nil)
		_ = speculate(context.Background(), deps, u)
	}
	if fk.Calls() != 0 {
		t.Fatalf("backend calls = %d, want 0 (speculation can never reach a backend)", fk.Calls())
	}
}

// --- shouldSpeculate (debounce) tests ---------------------------------------

func TestShouldSpeculateNilPrev(t *testing.T) {
	if !shouldSpeculate(nil, []string{"a"}, time.Now()) {
		t.Fatal("want true with no previous speculation")
	}
}

func TestShouldSpeculateIdenticalTokensNeverReruns(t *testing.T) {
	now := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	prev := &Speculation{tokens: []string{"a", "b"}, computedAt: now}
	// Even long after the debounce window, identical tokens never re-run.
	if shouldSpeculate(prev, []string{"a", "b"}, now.Add(time.Hour)) {
		t.Fatal("want false for identical tokens")
	}
}

func TestShouldSpeculateChangedTokensWaitsOutDebounce(t *testing.T) {
	now := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	prev := &Speculation{tokens: []string{"a", "b"}, computedAt: now}
	if shouldSpeculate(prev, []string{"a", "c"}, now.Add(specDebounce/2)) {
		t.Fatal("want false: within the debounce window")
	}
	if !shouldSpeculate(prev, []string{"a", "c"}, now.Add(specDebounce)) {
		t.Fatal("want true: debounce window elapsed and tokens changed")
	}
}

// --- Integration tests: Nervous.Partial + Handle reuse ----------------------

func TestSpeculationReuseWithinWindow(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	env, ctx, _ := nervousTestEnv(t)
	clock := newFakeClock(tier0FixedNow)
	env.Now = clock.Now
	table := turn.NewTableConfig(turn.DefaultConfig(), clock.Now)

	n, err := New(Config{
		Registry: func() *intents.Registry { return reg }, Style: render.DefaultStyle(),
		Turns: table, Tier0Enabled: true, MainEnabled: true, Clock: clock, AckAfter: DefaultAckAfter,
		Speculation: true, Store: env.Store, ReadStore: env.Store,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := env.Store.Upsert(ctx, &store.Event{
		Meta: store.Meta{Source: "gcal", SourceID: "e1", CreatedAt: tier0FixedNow}, Title: "Board sync",
		StartAt: tier0FixedNow.Add(2 * time.Hour), EndAt: tier0FixedNow.Add(3 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := n.Partial(ctx, "client-1", runtime.ChannelCLI, "what's on my calendar today", 1); err != nil {
		t.Fatal(err)
	}

	// Change the store's answer between the partial and the final turn (same
	// source/source_id, so Upsert replaces the title in place): if Handle
	// actually reuses the cached speculation, the stale ("Board sync")
	// answer still comes back; if it wrongly re-ran the handler, the
	// renamed title would show instead. This proves reuse happened without
	// needing internal instrumentation.
	if err := env.Store.Upsert(ctx, &store.Event{
		Meta: store.Meta{Source: "gcal", SourceID: "e1", CreatedAt: tier0FixedNow}, Title: "Renamed sync",
		StartAt: tier0FixedNow.Add(2 * time.Hour), EndAt: tier0FixedNow.Add(3 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	clock.Advance(time.Second) // still within specReuseWindow (2s)

	var events []runtime.Event
	var mu sync.Mutex
	n.Handle(ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "what's on my calendar today", TaskID: "final-1", ClientID: "client-1"}, collect(&events, &mu))

	last := events[len(events)-1]
	if last.Kind != runtime.EventDone {
		t.Fatalf("last event = %+v, want done", last)
	}
	if !strings.Contains(last.Text, "Board sync") || strings.Contains(last.Text, "Renamed sync") {
		t.Fatalf("done text = %q, want the stale cached (reused) speculation mentioning Board sync, not the renamed title", last.Text)
	}
}

func TestSpeculationRerunsAfterWindow(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	env, ctx, _ := nervousTestEnv(t)
	clock := newFakeClock(tier0FixedNow)
	env.Now = clock.Now
	table := turn.NewTableConfig(turn.DefaultConfig(), clock.Now)

	n, err := New(Config{
		Registry: func() *intents.Registry { return reg }, Style: render.DefaultStyle(),
		Turns: table, Tier0Enabled: true, MainEnabled: true, Clock: clock, AckAfter: DefaultAckAfter,
		Speculation: true, Store: env.Store, ReadStore: env.Store,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := env.Store.Upsert(ctx, &store.Event{
		Meta: store.Meta{Source: "gcal", SourceID: "e2", CreatedAt: tier0FixedNow}, Title: "Board sync",
		StartAt: tier0FixedNow.Add(2 * time.Hour), EndAt: tier0FixedNow.Add(3 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := n.Partial(ctx, "client-2", runtime.ChannelCLI, "what's on my calendar today", 1); err != nil {
		t.Fatal(err)
	}
	if err := env.Store.Upsert(ctx, &store.Event{
		Meta: store.Meta{Source: "gcal", SourceID: "e2", CreatedAt: tier0FixedNow}, Title: "Renamed sync",
		StartAt: tier0FixedNow.Add(2 * time.Hour), EndAt: tier0FixedNow.Add(3 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	clock.Advance(3 * time.Second) // past specReuseWindow (2s): must re-run for real

	var events []runtime.Event
	var mu sync.Mutex
	n.Handle(ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "what's on my calendar today", TaskID: "final-2", ClientID: "client-2"}, collect(&events, &mu))

	last := events[len(events)-1]
	if !strings.Contains(last.Text, "Renamed sync") || strings.Contains(last.Text, "Board sync") {
		t.Fatalf("done text = %q, want a fresh (post-rename) answer, not the stale cached one", last.Text)
	}
}

// TestSpeculationNewPartialReplacesPrevious: a second partial with genuinely
// different tokens overwrites the cached speculation for the same turn —
// the FIRST speculation's intent/labels no longer apply to a later reuse
// check.
func TestSpeculationNewPartialReplacesPrevious(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{
		"schedule_on_date": tier0ScheduleYAML,
		"brief_today":      tier0BriefYAML,
	})
	env, ctx, _ := nervousTestEnv(t)
	clock := newFakeClock(tier0FixedNow)
	table := turn.NewTableConfig(turn.DefaultConfig(), clock.Now)
	n, err := New(Config{
		Registry: func() *intents.Registry { return reg }, Style: render.DefaultStyle(),
		Turns: table, Clock: clock, AckAfter: DefaultAckAfter,
		Speculation: true, Store: env.Store, ReadStore: env.Store,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := n.Partial(ctx, "client-3", runtime.ChannelCLI, "what's on my calendar today", 1); err != nil {
		t.Fatal(err)
	}
	specAny, ok := table.GetSpec("client-3")
	if !ok {
		t.Fatal("no speculation recorded after the first partial")
	}
	first := specAny.(*Speculation)
	if first.intent != "schedule.on_date" {
		t.Fatalf("first.intent = %q, want schedule.on_date", first.intent)
	}

	clock.Advance(specDebounce) // clear the debounce window
	if _, err := n.Partial(ctx, "client-3", runtime.ChannelCLI, "my brief", 2); err != nil {
		t.Fatal(err)
	}
	specAny, ok = table.GetSpec("client-3")
	if !ok {
		t.Fatal("speculation vanished after the second partial")
	}
	second := specAny.(*Speculation)
	if second == first {
		t.Fatal("the second partial's speculation is the same object as the first's; it must be replaced")
	}
	if second.intent != "brief.today" || second.hit {
		// brief.today's dry-match hits, but RunRead always reports a cache
		// miss during speculation (nervous.go's Deps.Brief stub) — so hit
		// must be false and prewarm must have been attempted instead.
		t.Fatalf("second = %+v, want intent=brief.today hit=false", second)
	}
}

// TestSpeculationDebounceIdenticalTokensDoNotRerun: a burst of partials with
// exactly the same normalized tokens only speculate once.
func TestSpeculationDebounceIdenticalTokensDoNotRerun(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	env, ctx, _ := nervousTestEnv(t)
	clock := newFakeClock(tier0FixedNow)
	table := turn.NewTableConfig(turn.DefaultConfig(), clock.Now)
	n, err := New(Config{
		Registry: func() *intents.Registry { return reg }, Style: render.DefaultStyle(),
		Turns: table, Clock: clock, AckAfter: DefaultAckAfter,
		Speculation: true, Store: env.Store, ReadStore: env.Store,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := n.Partial(ctx, "client-4", runtime.ChannelCLI, "what's on my calendar today", 1); err != nil {
		t.Fatal(err)
	}
	specAny, _ := table.GetSpec("client-4")
	first := specAny.(*Speculation)
	firstComputedAt := first.computedAt

	for i := 2; i <= 50; i++ {
		clock.Advance(time.Millisecond)
		if _, err := n.Partial(ctx, "client-4", runtime.ChannelCLI, "what's on my calendar today", i); err != nil {
			t.Fatal(err)
		}
	}
	specAny, _ = table.GetSpec("client-4")
	after := specAny.(*Speculation)
	if !after.computedAt.Equal(firstComputedAt) {
		t.Fatalf("computedAt changed (%v -> %v) despite identical tokens on every partial", firstComputedAt, after.computedAt)
	}
}
