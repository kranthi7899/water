package nervous

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"water/internal/backend"
	"water/internal/nervous/intents"
	"water/internal/nervous/render"
	"water/internal/nervous/slots"
	"water/internal/runtime"
	"water/internal/store"
	"water/internal/twins"
)

// promotionLearnedYAML is a valid learned intent wrapping the same
// store.calendar_events handler schedule.on_date already uses in
// tier0_test.go's fixtures, with a phrasing that doesn't collide with any
// embedded template.
const promotionLearnedYAML = `
id: learned.peek
description: a learned phrasing wrapping store.calendar_events
function: store.calendar_events
slots:
  when: {type: daterange, default: today}
templates:
  - "peek at my day {when}"
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
origin: learned
tests:
  - {utterance: "peek at my day today", intent: learned.peek, slots: {when: today}}
  - {utterance: "gibberish nonsense", intent: "none"}
`

// promotionFixtureRegistry builds a registry with exactly one active
// learned intent (learned.peek) and no embedded intents beyond the required
// _shared.yaml, optionally already disabled per the disabled map (the same
// shape store.ListIntentStates returns).
func promotionFixtureRegistry(t *testing.T, disabled map[string]string) *intents.Registry {
	t.Helper()
	m, err := twins.Parse([]byte(tier0ManifestYAML))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	fns := intents.Functions{Read: map[string]intents.FunctionSpec{
		"store.calendar_events": {
			ID: "store.calendar_events", Args: map[string]slots.Type{"when": slots.TypeDateRange},
			ReadOnly: true, Deterministic: true, Class: intents.ClassLookup,
		},
	}}
	fsys := fstest.MapFS{"twins/testtwin/intents/_shared.yaml": {Data: []byte(tier0SharedYAML)}}
	learned := fstest.MapFS{"peek.yaml": {Data: []byte(promotionLearnedYAML)}}
	reg, err := intents.LoadRegistry(fsys, m, fns, intents.LoadOptions{Learned: learned, Disabled: disabled})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	return reg
}

func promotionNervous(t *testing.T, reg *intents.Registry, st *store.Store, clock Clock, minSamples, missRatePct int) *Nervous {
	t.Helper()
	n, err := New(Config{
		Registry:     func() *intents.Registry { return reg },
		Style:        render.DefaultStyle(),
		Tier0Enabled: true,
		MainEnabled:  true,
		Clock:        clock,
		AckAfter:     DefaultAckAfter,
		Store:        st,
		MissWindow:   DefaultMissWindow,
		Promotion:    PromotionConfig{DemoteMinSamples: minSamples, DemoteMissRatePct: missRatePct},
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func waitForDisabled(t *testing.T, n *Nervous, id string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if it, ok := n.registry().Lookup(id); ok && it.Disabled != "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s to be disabled in the live registry", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func seedLearnedRow(t *testing.T, st *store.Store, turnID string, at time.Time, possibleMiss bool) {
	t.Helper()
	row := store.RouteRow{
		TurnID:       turnID,
		At:           at,
		Channel:      "cli",
		Utterance:    "peek at my day today",
		Owner:        "quick",
		AnsweredBy:   "t0",
		Intent:       "learned.peek",
		IntentKind:   "read",
		IntentOrigin: "learned",
		Outcome:      "answered",
		PossibleMiss: possibleMiss,
	}
	if _, err := st.InsertRoute(context.Background(), row); err != nil {
		t.Fatalf("seed InsertRoute(%s): %v", turnID, err)
	}
}

// TestLearnedAutoDemoted drives the automatic-demotion hook end to end
// through Handle (not just the pure LearnedIntentShouldDemote function,
// which breaker.go's own tests already cover in isolation): once a learned
// intent's possible-miss rate crosses the configured threshold over enough
// samples, it is disabled, and the very next matching turn escalates
// instead of answering via it.
func TestLearnedAutoDemoted(t *testing.T) {
	reg := promotionFixtureRegistry(t, nil)
	env, ctx, fk := nervousTestEnv(t)
	fk.Reply = func(req backend.Request) string { return "Sure, one moment." }
	clock := newFakeClock(tier0FixedNow)
	n := promotionNervous(t, reg, env.Store, clock, 10, 20)
	env2 := env
	env2.Now = func() time.Time { return clock.Now() }

	start := clock.Now()

	// Seed 9 prior answered rows for learned.peek, 2 already flagged as
	// possible misses.
	for i := 0; i < 9; i++ {
		seedLearnedRow(t, env.Store, fmt.Sprintf("seed-%d", i), start.Add(-time.Duration(9-i)*time.Hour), i < 2)
	}

	// A clean Tier 0 answer (the 10th sample), then a correction within the
	// miss window: this marks the 10th row a possible miss too, bringing
	// the total to 10 samples / 3 misses = 30%, above the 20% threshold.
	handleAndWait(n, ctx, env2, Turn{Channel: runtime.ChannelCLI, Text: "peek at my day today", TaskID: "t-tenth"})
	clock.Advance(5 * time.Second)
	handleAndWait(n, ctx, env2, Turn{Channel: runtime.ChannelCLI, Text: "that's wrong, peek at my day today", TaskID: "t-correction"})

	waitForDisabled(t, n, "learned.peek", 2*time.Second)

	states, err := env.Store.ListIntentStates(ctx)
	if err != nil {
		t.Fatalf("ListIntentStates: %v", err)
	}
	if _, ok := states["learned.peek"]; !ok {
		t.Fatalf("store.intent_state does not record learned.peek as disabled: %v", states)
	}

	// The next matching turn must escalate (to the main path) instead of
	// answering via the now-disabled learned intent.
	clock.Advance(1 * time.Second)
	handleAndWait(n, ctx, env2, Turn{Channel: runtime.ChannelCLI, Text: "peek at my day today", TaskID: "t-after-demote"})
	row := lastRoute(t, env.Store, start)
	if row.TurnID != "t-after-demote" {
		t.Fatalf("unexpected last route row: %+v", row)
	}
	if row.Owner == "quick" {
		t.Fatalf("row after auto-demotion still answered via the quick tier: %+v", row)
	}
}

// TestLearnedNotDemotedBelowThreshold is the negative case: fewer samples
// than DemoteMinSamples never demotes, however bad the visible rate looks.
func TestLearnedNotDemotedBelowThreshold(t *testing.T) {
	reg := promotionFixtureRegistry(t, nil)
	env, ctx, fk := nervousTestEnv(t)
	fk.Reply = func(req backend.Request) string { return "Sure, one moment." }
	clock := newFakeClock(tier0FixedNow)
	n := promotionNervous(t, reg, env.Store, clock, 10, 20)
	env2 := env
	env2.Now = func() time.Time { return clock.Now() }
	start := clock.Now()

	// Only 2 prior rows (both possible misses): far short of the 10-sample
	// minimum, so even a 100% visible miss rate must not demote.
	seedLearnedRow(t, env.Store, "seed-0", start.Add(-2*time.Hour), true)
	seedLearnedRow(t, env.Store, "seed-1", start.Add(-1*time.Hour), true)

	handleAndWait(n, ctx, env2, Turn{Channel: runtime.ChannelCLI, Text: "peek at my day today", TaskID: "t-third"})
	clock.Advance(5 * time.Second)
	handleAndWait(n, ctx, env2, Turn{Channel: runtime.ChannelCLI, Text: "that's wrong, peek at my day today", TaskID: "t-correction"})

	// Give the (fire-and-forget) auto-demote goroutine a moment to run, then
	// confirm it did NOT disable the intent.
	time.Sleep(100 * time.Millisecond)
	if it, ok := n.registry().Lookup("learned.peek"); !ok || it.Disabled != "" {
		t.Fatalf("learned.peek was demoted with only 3 samples, want it to stay enabled: %+v", it)
	}
	states, err := env.Store.ListIntentStates(ctx)
	if err != nil {
		t.Fatalf("ListIntentStates: %v", err)
	}
	if _, ok := states["learned.peek"]; ok {
		t.Fatal("store.intent_state records learned.peek as disabled with only 3 samples")
	}
}

// TestAnswerQuickRecordsIntentOrigin proves the wiring gap this task closed:
// before it, routeRecorder.intentOrigin was never set by answerQuick at
// all, so route_log.intent_origin stayed at its Go zero value ("") for
// every real turn — the automatic-demotion hook above cannot work at all
// without this.
func TestAnswerQuickRecordsIntentOrigin(t *testing.T) {
	reg := promotionFixtureRegistry(t, nil)
	env, ctx, _ := nervousTestEnv(t)
	n := nervousWithLoggingFor(t, reg, env.Store, nil)
	start := tier0FixedNow.Add(-time.Second)

	handleAndWait(n, ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "peek at my day today", TaskID: "t-learned-origin"})
	row := lastRoute(t, env.Store, start)
	if row.IntentOrigin != "learned" {
		t.Fatalf("IntentOrigin = %q, want \"learned\" for a learned.* intent (row=%+v)", row.IntentOrigin, row)
	}
}

// TestReloadAtomicUnderConcurrentHandle fires several concurrent Handle
// calls while Reload swaps the registry repeatedly on another goroutine:
// -race must find nothing, and nothing may panic.
func TestReloadAtomicUnderConcurrentHandle(t *testing.T) {
	regA := promotionFixtureRegistry(t, nil)
	regB := promotionFixtureRegistry(t, map[string]string{"learned.peek": "manual: race test"})

	env, ctx, fk := nervousTestEnv(t)
	fk.Reply = func(req backend.Request) string { return "Sure, one moment." }
	n := promotionNervous(t, regA, env.Store, realClock{}, 10, 20)

	stop := make(chan struct{})
	var reloaders sync.WaitGroup
	reloaders.Add(1)
	go func() {
		defer reloaders.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				n.Reload(regA)
			} else {
				n.Reload(regB)
			}
			i++
		}
	}()

	var handlers sync.WaitGroup
	for g := 0; g < 4; g++ {
		handlers.Add(1)
		go func(g int) {
			defer handlers.Done()
			for i := 0; i < 15; i++ {
				handleAndWait(n, ctx, env, Turn{
					Channel: runtime.ChannelCLI,
					Text:    "peek at my day today",
					TaskID:  fmt.Sprintf("race-%d-%d", g, i),
				})
			}
		}(g)
	}
	handlers.Wait()
	close(stop)
	reloaders.Wait()
}
