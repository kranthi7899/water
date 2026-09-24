package nervous

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/nervous/intents"
	"water/internal/nervous/render"
	"water/internal/nervous/turn"
	"water/internal/runtime"
	"water/internal/store"
	"water/internal/twins"
)

const tier0BriefYAML = `
id: brief.today
description: Today's cached morning brief
function: store.cached_brief
templates:
  - "what's my brief"
  - "my brief"
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
tests:
  - {utterance: "my brief", intent: brief.today}
  - {utterance: "gibberish nonsense", intent: "none"}
`

// nervousTestEnv builds a real (temp-dir-backed) runtime.Env, reusing
// tier0_test.go's fixture manifest/clock so a schedule.on_date intent built
// against tier0FixtureRegistry resolves against the same "gcal" connector
// the manifest declares.
func nervousTestEnv(t *testing.T) (runtime.Env, context.Context, *backend.Fake) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	q := approvals.NewQueue(st, log)
	q.Now = func() time.Time { return tier0FixedNow }
	m, err := twins.Parse([]byte(tier0ManifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	fk := backend.NewFake("fake")
	env := runtime.Env{
		Store: st, Approvals: q, Manifest: m, Backend: fk,
		Now: func() time.Time { return tier0FixedNow },
	}
	return env, context.Background(), fk
}

func nervousFor(t *testing.T, reg *intents.Registry, clock Clock) *Nervous {
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
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func collect(events *[]runtime.Event, mu *sync.Mutex) func(runtime.Event) {
	return func(e runtime.Event) {
		mu.Lock()
		*events = append(*events, e)
		mu.Unlock()
	}
}

func kinds(events []runtime.Event) []runtime.EventKind {
	out := make([]runtime.EventKind, len(events))
	for i, e := range events {
		out[i] = e.Kind
	}
	return out
}

// TestHandleTier0Hit: a schedule.on_date match answers with zero backend
// calls, and the turn ends StateDone/OwnerQuick.
func TestHandleTier0Hit(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	env, ctx, fk := nervousTestEnv(t)
	if err := env.Store.Upsert(ctx, &store.Event{
		Meta:    store.Meta{Source: "gcal", SourceID: "e1", CreatedAt: tier0FixedNow},
		Title:   "Board sync",
		StartAt: tier0FixedNow.Add(2 * time.Hour),
		EndAt:   tier0FixedNow.Add(3 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	table := turn.NewTable(func() time.Time { return tier0FixedNow })
	n, err := New(Config{
		Registry: func() *intents.Registry { return reg }, Style: render.DefaultStyle(),
		Turns: table, Tier0Enabled: true, MainEnabled: true, Clock: realClock{}, AckAfter: DefaultAckAfter,
	})
	if err != nil {
		t.Fatal(err)
	}

	var events []runtime.Event
	var mu sync.Mutex
	n.Handle(ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "what's on my calendar today", TaskID: "t1"}, collect(&events, &mu))

	if fk.Calls() != 0 {
		t.Fatalf("backend calls = %d, want 0 (Tier 0 must never call the model)", fk.Calls())
	}
	ks := kinds(events)
	if len(ks) < 2 || ks[0] != runtime.EventAck || ks[len(ks)-1] != runtime.EventDone {
		t.Fatalf("events = %v, want ack ... done", ks)
	}
	tn, ok := table.Get("t1")
	if !ok || tn.State != turn.StateDone || tn.Owner != turn.OwnerQuick {
		t.Fatalf("turn = %+v, want StateDone/OwnerQuick", tn)
	}
}

// TestHandleEscalatesToMain: an escalate-word utterance ("should") never
// reaches Tier 0 at all, and the main path answers via the backend.
func TestHandleEscalatesToMain(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	env, ctx, fk := nervousTestEnv(t)
	fk.Reply = func(req backend.Request) string { return "You should focus on the board deck." }

	table := turn.NewTable(func() time.Time { return tier0FixedNow })
	n, err := New(Config{
		Registry: func() *intents.Registry { return reg }, Style: render.DefaultStyle(),
		Turns: table, Tier0Enabled: true, MainEnabled: true, Clock: realClock{}, AckAfter: DefaultAckAfter,
	})
	if err != nil {
		t.Fatal(err)
	}

	var events []runtime.Event
	var mu sync.Mutex
	n.Handle(ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "what should i prioritize today", TaskID: "t2"}, collect(&events, &mu))

	if fk.Calls() != 1 {
		t.Fatalf("backend calls = %d, want 1", fk.Calls())
	}
	ks := kinds(events)
	if len(ks) < 2 || ks[0] != runtime.EventAck || ks[len(ks)-1] != runtime.EventDone {
		t.Fatalf("events = %v, want ack ... done", ks)
	}
	tn, ok := table.Get("t2")
	if !ok || tn.State != turn.StateDone || tn.Owner != turn.OwnerMain {
		t.Fatalf("turn = %+v, want StateDone/OwnerMain", tn)
	}
}

// TestHandleBriefCacheMiss: brief.today's handler returns
// reflex.ErrBriefCacheMiss on an empty cache. Handle must not treat this as
// a generic conversational escalation — it computes and caches the brief
// through runtime.ComputeAndCacheBrief, the one deliberate model call the
// quick tiers themselves never make (Design §11.4 step 7 / Risk 7).
func TestHandleBriefCacheMiss(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"brief_today": tier0BriefYAML})
	env, ctx, fk := nervousTestEnv(t)
	fk.Reply = func(req backend.Request) string { return "Nothing urgent today." }

	n := nervousFor(t, reg, realClock{})
	var events []runtime.Event
	var mu sync.Mutex
	n.Handle(ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "what's my brief", TaskID: "t3"}, collect(&events, &mu))

	if fk.Calls() != 1 {
		t.Fatalf("backend calls = %d, want exactly 1 (the one deliberate brief compute)", fk.Calls())
	}
	ks := kinds(events)
	last := events[len(ks)-1]
	if last.Kind != runtime.EventDone || last.Text != "Nothing urgent today." {
		t.Fatalf("last event = %+v, want done with the computed brief text", last)
	}

	// A second ask must not compute again: the brief is now cached, so this
	// is an ordinary Tier 0 hit with zero further backend calls.
	n.Handle(ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "what's my brief", TaskID: "t3b"}, func(runtime.Event) {})
	if fk.Calls() != 1 {
		t.Fatalf("backend calls after a second ask = %d, want still 1 (cache hit)", fk.Calls())
	}
}

// TestEveryTurnStartsAtT0: two unrelated, sequential turns on the same
// Nervous never share state — a prior turn's outcome can't skip the
// cascade for a later one.
func TestEveryTurnStartsAtT0(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	env, ctx, fk := nervousTestEnv(t)
	fk.Reply = func(req backend.Request) string { return "You should reprioritize." }
	n := nervousFor(t, reg, realClock{})

	n.Handle(ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "what should i do today", TaskID: "a1"}, func(runtime.Event) {})
	if fk.Calls() != 1 {
		t.Fatalf("first turn: backend calls = %d, want 1", fk.Calls())
	}
	n.Handle(ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "what's on my calendar today", TaskID: "a2"}, func(runtime.Event) {})
	if fk.Calls() != 1 {
		t.Fatalf("second turn (a plain Tier 0 match) made a backend call: calls = %d, want still 1", fk.Calls())
	}
}

// TestExactlyOneOwner: once Handle has routed and finished a turn, no other
// caller can route it again — turn.Table's CAS Route rejects it.
func TestExactlyOneOwner(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	env, ctx, _ := nervousTestEnv(t)
	table := turn.NewTable(func() time.Time { return tier0FixedNow })
	n, err := New(Config{
		Registry: func() *intents.Registry { return reg }, Style: render.DefaultStyle(),
		Turns: table, Tier0Enabled: true, MainEnabled: true, Clock: realClock{}, AckAfter: DefaultAckAfter,
	})
	if err != nil {
		t.Fatal(err)
	}
	n.Handle(ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "what's on my calendar today", TaskID: "one-owner"}, func(runtime.Event) {})

	if err := table.Route("one-owner", turn.OwnerMain); err == nil {
		t.Fatal("Route succeeded on an already-done turn; exactly one owner must ever be assigned")
	}
}

// TestTier0WhileMainBlocked: Tier 0 shares no lock or semaphore with the
// main path. A concurrent Tier-0-matchable turn must complete quickly even
// while another turn's main-path call is blocked. (R-12 has no warm
// session yet, so this proves the invariant at internal/nervous's own
// level — turn.Table and Tier 0 hold no resource a slow main-path call
// keeps — rather than against a real WarmSession's one-slot semaphore,
// which is a later task's integration concern.)
func TestTier0WhileMainBlocked(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	env, ctx, fk := nervousTestEnv(t)
	block := make(chan struct{})
	started := make(chan struct{})
	fk.Reply = func(req backend.Request) string {
		close(started)
		<-block
		return "answer"
	}
	n := nervousFor(t, reg, realClock{})

	done := make(chan struct{})
	go func() {
		n.Handle(ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "what should i do today", TaskID: "blocked"}, func(runtime.Event) {})
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked turn's backend call never started")
	}

	quickDone := make(chan struct{})
	go func() {
		n.Handle(ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "what's on my calendar today", TaskID: "quick"}, func(runtime.Event) {})
		close(quickDone)
	}()
	select {
	case <-quickDone:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Tier 0 turn did not complete within 250ms while the main path was blocked")
	}

	close(block)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked turn never finished after being released")
	}
	if fk.Calls() != 1 {
		t.Fatalf("backend calls = %d, want 1 (only the blocked main-path turn)", fk.Calls())
	}
}

// fakeClock is a controllable Clock for testing the ack timer without a
// real sleep: AfterFunc records a pending callback, and Advance fires every
// callback whose deadline has passed.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	mu      sync.Mutex
	fire    time.Time
	f       func()
	fired   bool
	stopped bool
}

func (ft *fakeTimer) Stop() {
	ft.mu.Lock()
	ft.stopped = true
	ft.mu.Unlock()
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{now: start} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	ft := &fakeTimer{fire: c.now.Add(d), f: f}
	c.timers = append(c.timers, ft)
	return ft
}

// Advance moves the fake clock forward and synchronously runs every timer
// whose deadline is now due (and not stopped), in the goroutine that calls
// Advance — the same way a real time.AfterFunc callback would run in its
// own goroutine relative to whoever scheduled it, except deterministic.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	for _, ft := range c.timers {
		ft.mu.Lock()
		if !ft.fired && !ft.stopped && !ft.fire.After(c.now) {
			ft.fired = true
			due = append(due, ft)
		}
		ft.mu.Unlock()
	}
	c.mu.Unlock()
	for _, ft := range due {
		ft.f()
	}
}

// TestAckTimerFiresAndStops is a direct unit test of the Clock/Timer
// plumbing Handle relies on (Design §11.4 step 1/6): AfterFunc schedules
// exactly one callback at the right virtual deadline, and Stop prevents it.
func TestAckTimerFiresAndStops(t *testing.T) {
	start := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	c := newFakeClock(start)

	fired := 0
	timer := c.AfterFunc(DefaultAckAfter, func() { fired++ })
	c.Advance(DefaultAckAfter - time.Millisecond)
	if fired != 0 {
		t.Fatalf("fired = %d before the deadline, want 0", fired)
	}
	c.Advance(time.Millisecond)
	if fired != 1 {
		t.Fatalf("fired = %d at the deadline, want 1", fired)
	}

	timer2 := c.AfterFunc(DefaultAckAfter, func() { fired++ })
	timer2.Stop()
	c.Advance(DefaultAckAfter)
	if fired != 1 {
		t.Fatalf("fired = %d after Stop, want still 1", fired)
	}
	_ = timer
}

// TestHandoffAckBeforeMainOutput: on escalation, the handoff acknowledgement
// is always visible before the main path's first output — Handle fires it
// explicitly at the moment it routes to main (answerMain), rather than
// waiting for AckAfter to elapse, so even a slow model's first delta is
// always preceded by an ack. (Design §11.4 step 6's other half — the ack
// firing purely from the timer while a slower quick tier is still working —
// needs Tier 1, which doesn't exist until a later task; see
// TestAckTimerFiresAndStops above for that mechanism tested directly.)
func TestHandoffAckBeforeMainOutput(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	env, ctx, fk := nervousTestEnv(t)
	release := make(chan struct{})
	fk.Reply = func(req backend.Request) string {
		<-release
		return "answer"
	}
	n := nervousFor(t, reg, realClock{})

	var events []runtime.Event
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		n.Handle(ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "what should i do today", TaskID: "ack-order"}, collect(&events, &mu))
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(events)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	gotAck := len(events) >= 1 && events[0].Kind == runtime.EventAck
	mu.Unlock()
	if !gotAck {
		t.Fatal("ack event was not emitted before the (still blocked) main path produced anything")
	}

	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("turn never finished after being released")
	}
	if fk.Calls() != 1 {
		t.Fatalf("backend calls = %d, want 1", fk.Calls())
	}
}
