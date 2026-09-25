package nervous

import (
	"context"
	"sync"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/backend"
	"water/internal/nervous/intents"
	"water/internal/nervous/render"
	"water/internal/runtime"
	"water/internal/store"
)

// nervousWithLoggingFor builds a Nervous wired to env's real store, so
// Handle actually writes route_log rows (nervousFor, from nervous_test.go,
// deliberately leaves Config.Store nil for tests that don't care).
func nervousWithLoggingFor(t *testing.T, reg *intents.Registry, st *store.Store, clock Clock) *Nervous {
	t.Helper()
	if clock == nil {
		clock = realClock{}
	}
	n, err := New(Config{
		Registry:     func() *intents.Registry { return reg },
		Style:        render.DefaultStyle(),
		Tier0Enabled: true,
		MainEnabled:  true,
		Clock:        clock,
		AckAfter:     DefaultAckAfter,
		Store:        st,
		MissWindow:   DefaultMissWindow,
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func handleAndWait(n *Nervous, ctx context.Context, env runtime.Env, tn Turn) []runtime.Event {
	var events []runtime.Event
	var mu sync.Mutex
	n.Handle(ctx, env, tn, collect(&events, &mu))
	return events
}

// proposePending queues n bare-minimum pending envelopes on env's real
// approvals queue, so approvals.respond's bindPendingHandler (reflex,
// R-8) sees Pending() return them. approvals.Queue has no interface seam
// to fake, and Envelope only requires Action and Origin to propose.
func proposePending(t *testing.T, ctx context.Context, env runtime.Env, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := env.Approvals.Propose(ctx, approvals.Envelope{Action: "gcal.create_event", Origin: "P0"}); err != nil {
			t.Fatalf("Propose: %v", err)
		}
	}
}

func lastRoute(t *testing.T, st *store.Store, since time.Time) store.RouteRow {
	t.Helper()
	rows, err := st.ListRoutes(context.Background(), since, 20)
	if err != nil {
		t.Fatalf("ListRoutes: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no route_log rows found")
	}
	return rows[0] // ListRoutes orders newest first
}

// routelogRespondYAML, unlike tier0_test.go's tier0PendingYAML (which
// points at approvals.pending/approvalsListHandler purely to exercise the
// requires_pending_approval GATING rule), points at the real
// approvals.bind_pending/bindPendingHandler pair, so its Result.Kind
// actually varies with the pending count the way approvals.respond really
// behaves (clarify at 0 or 2+, decision at exactly 1) — the distinction
// this file's outcome-mapping tests need.
const routelogRespondYAML = `
id: approvals.respond
description: Pending-gate fixture (real handler)
function: approvals.bind_pending
requires_pending_approval: true
templates:
  - "yes"
tests:
  - {utterance: "yes", intent: approvals.respond, pending: 1}
  - {utterance: "yes", intent: "none", pending: 0}
  - {utterance: "gibberish nonsense", intent: "none"}
`

func TestRouteLogExactlyOneRowPerOutcome(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{
		"schedule": tier0ScheduleYAML,
		"respond":  routelogRespondYAML,
	})

	t.Run("answered", func(t *testing.T) {
		env, ctx, _ := nervousTestEnv(t)
		n := nervousWithLoggingFor(t, reg, env.Store, nil)
		start := tier0FixedNow.Add(-time.Second)
		handleAndWait(n, ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "what's on my calendar tomorrow", TaskID: "t-answered"})

		rows, err := env.Store.ListRoutes(ctx, start, 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("got %d rows, want exactly 1", len(rows))
		}
		row := rows[0]
		if row.Outcome != "answered" || row.Owner != "quick" || row.AnsweredBy != "t0" || row.Intent != "schedule.on_date" {
			t.Fatalf("row = %+v", row)
		}
	})

	t.Run("clarified", func(t *testing.T) {
		env, ctx, _ := nervousTestEnv(t)
		proposePending(t, ctx, env, 2)
		n := nervousWithLoggingFor(t, reg, env.Store, nil)
		start := tier0FixedNow.Add(-time.Second)
		handleAndWait(n, ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "yes", TaskID: "t-clarified"})

		row := lastRoute(t, env.Store, start)
		if row.Outcome != "clarified" {
			t.Fatalf("outcome = %q, want clarified (row=%+v)", row.Outcome, row)
		}
	})

	t.Run("proposed (single pending decision)", func(t *testing.T) {
		env, ctx, _ := nervousTestEnv(t)
		proposePending(t, ctx, env, 1)
		n := nervousWithLoggingFor(t, reg, env.Store, nil)
		start := tier0FixedNow.Add(-time.Second)
		handleAndWait(n, ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "yes", TaskID: "t-proposed"})

		row := lastRoute(t, env.Store, start)
		if row.Outcome != "proposed" {
			t.Fatalf("outcome = %q, want proposed (row=%+v)", row.Outcome, row)
		}
	})

	t.Run("error (main path, disabled)", func(t *testing.T) {
		env, ctx, _ := nervousTestEnv(t)
		n, err := New(Config{
			Registry: func() *intents.Registry { return reg }, Style: render.DefaultStyle(),
			Tier0Enabled: true, MainEnabled: false, Clock: realClock{}, AckAfter: DefaultAckAfter,
			Store: env.Store,
		})
		if err != nil {
			t.Fatal(err)
		}
		start := tier0FixedNow.Add(-time.Second)
		handleAndWait(n, ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "why is this happening", TaskID: "t-error"})

		row := lastRoute(t, env.Store, start)
		if row.Outcome != "error" {
			t.Fatalf("outcome = %q, want error (row=%+v)", row.Outcome, row)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		env, _, _ := nervousTestEnv(t)
		n := nervousWithLoggingFor(t, reg, env.Store, nil)
		start := tier0FixedNow.Add(-time.Second)
		cctx, cancel := context.WithCancel(context.Background())
		cancel()
		handleAndWait(n, cctx, env, Turn{Channel: runtime.ChannelCLI, Text: "why is this happening", TaskID: "t-cancelled"})

		row := lastRoute(t, env.Store, start)
		if row.Outcome != "cancelled" {
			t.Fatalf("outcome = %q, want cancelled (row=%+v)", row.Outcome, row)
		}
	})
}

// TestRouteLogQuickToolAttribution proves the gap this task (R-22) closed:
// before it, nothing ever called ToolTracer.BeginMain/EndMain, so
// RecordToolUse's calls (wired to gateway/quick.go since R-16) always found
// no turn in flight and were silently dropped — every real route_log row's
// tools_used/tools_attributed/quick_only/tool_signature stayed at their Go
// zero value forever. answerMain now brackets the main path with
// BeginMain/EndMain (mainpath.go), so a main turn that calls only quick.*
// tools is correctly attributed and marked quick_only, with a stable,
// sorted signature.
func TestRouteLogQuickToolAttribution(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule": tier0ScheduleYAML})

	t.Run("quick-only tool calls are attributed and grouped", func(t *testing.T) {
		env, ctx, fk := nervousTestEnv(t)
		n := nervousWithLoggingFor(t, reg, env.Store, nil)
		fk.Reply = func(req backend.Request) string {
			// Recorded out of alphabetical order deliberately: the
			// signature must come back sorted regardless of call order.
			n.RecordToolUse("quick.next_event")
			n.RecordToolUse("quick.calendar")
			return "Sure."
		}
		start := tier0FixedNow.Add(-time.Second)
		handleAndWait(n, ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "what should i prioritize today", TaskID: "t-quickonly"})

		row := lastRoute(t, env.Store, start)
		if row.Owner != "main" {
			t.Fatalf("Owner = %q, want main (row=%+v)", row.Owner, row)
		}
		if !row.ToolsAttributed {
			t.Fatalf("ToolsAttributed = false, want true (row=%+v)", row)
		}
		if !row.QuickOnly {
			t.Fatalf("QuickOnly = false, want true (row=%+v)", row)
		}
		if row.ToolSignature != "quick.calendar+quick.next_event" {
			t.Fatalf("ToolSignature = %q, want sorted \"quick.calendar+quick.next_event\"", row.ToolSignature)
		}
		if len(row.ToolsUsed) != 2 {
			t.Fatalf("ToolsUsed = %v, want 2 entries", row.ToolsUsed)
		}
	})

	t.Run("a main turn that calls no tools is attributed but never quick_only", func(t *testing.T) {
		env, ctx, fk := nervousTestEnv(t)
		n := nervousWithLoggingFor(t, reg, env.Store, nil)
		fk.Reply = func(req backend.Request) string { return "Sure." }
		start := tier0FixedNow.Add(-time.Second)
		handleAndWait(n, ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "what should i prioritize today", TaskID: "t-notools"})

		row := lastRoute(t, env.Store, start)
		if row.Owner != "main" || !row.ToolsAttributed {
			t.Fatalf("row = %+v, want owner=main, attributed", row)
		}
		if row.QuickOnly || row.ToolSignature != "" || len(row.ToolsUsed) != 0 {
			t.Fatalf("row = %+v, want quick_only=false and no tools", row)
		}
	})
}

func TestRouteLogPossibleMissWindow(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule": tier0ScheduleYAML})

	run := func(t *testing.T, gap time.Duration) store.RouteRow {
		env, ctx, _ := nervousTestEnv(t)
		clock := newFakeClock(tier0FixedNow)
		n := nervousWithLoggingFor(t, reg, env.Store, clock)
		start := tier0FixedNow.Add(-time.Second)

		env2 := env
		env2.Now = func() time.Time { return clock.Now() }
		handleAndWait(n, ctx, env2, Turn{Channel: runtime.ChannelCLI, Text: "what's on my calendar tomorrow", TaskID: "t-first"})

		clock.Advance(gap)
		// "that's wrong" is tier0SharedYAML's own corrections phrase — but
		// this registry only loaded _shared.yaml with tier0FixtureRegistry's
		// defaults, so use its actual corrections entry.
		handleAndWait(n, ctx, env2, Turn{Channel: runtime.ChannelCLI, Text: "that's wrong, what's on my calendar tomorrow", TaskID: "t-second"})

		rows, err := env.Store.ListRoutes(ctx, start, 20)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.TurnID == "t-first" {
				return row
			}
		}
		t.Fatal("first row not found")
		return store.RouteRow{}
	}

	t.Run("59s: flagged", func(t *testing.T) {
		row := run(t, 59*time.Second)
		if !row.PossibleMiss {
			t.Fatalf("row at 59s gap should be marked a possible miss: %+v", row)
		}
	})

	t.Run("61s: not flagged", func(t *testing.T) {
		row := run(t, 61*time.Second)
		if row.PossibleMiss {
			t.Fatalf("row at 61s gap should NOT be marked a possible miss: %+v", row)
		}
	})
}

func TestRouteLogPossibleMissDifferentChannelNeverFlagged(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule": tier0ScheduleYAML})
	env, ctx, _ := nervousTestEnv(t)
	clock := newFakeClock(tier0FixedNow)
	n := nervousWithLoggingFor(t, reg, env.Store, clock)
	start := tier0FixedNow.Add(-time.Second)

	env2 := env
	env2.Now = func() time.Time { return clock.Now() }
	handleAndWait(n, ctx, env2, Turn{Channel: runtime.ChannelCLI, Text: "what's on my calendar tomorrow", TaskID: "t-cli"})
	clock.Advance(1 * time.Second)
	handleAndWait(n, ctx, env2, Turn{Channel: runtime.ChannelVoice, Text: "that's wrong, what's on my calendar tomorrow", TaskID: "t-voice"})

	rows, err := env.Store.ListRoutes(ctx, start, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.TurnID == "t-cli" && row.PossibleMiss {
			t.Fatalf("a correction on a DIFFERENT channel must never flag the earlier row: %+v", row)
		}
	}
}

func TestReflexRingEvictsAfter10Minutes(t *testing.T) {
	clock := newFakeClock(tier0FixedNow)
	r := newReflexRing()
	r.add(reflexExchange{at: clock.Now(), utterance: "what's on my calendar tomorrow", answerSummary: "schedule.on_date"})

	if got := r.prompt(clock.Now()); got == "" {
		t.Fatal("expected a fresh entry to appear in the prompt")
	}
	clock.Advance(11 * time.Minute)
	if got := r.prompt(clock.Now()); got != "" {
		t.Fatalf("expected an entry older than 10 minutes to be excluded, got %q", got)
	}
}

func TestReflexRingNeverHoldsMoreThanThree(t *testing.T) {
	clock := newFakeClock(tier0FixedNow)
	r := newReflexRing()
	for i := 0; i < 5; i++ {
		r.add(reflexExchange{at: clock.Now(), utterance: "u", answerSummary: "s"})
	}
	if len(r.items) != 3 {
		t.Fatalf("ring holds %d entries, want at most 3", len(r.items))
	}
}
