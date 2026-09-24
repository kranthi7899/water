package turn

import (
	"sync"
	"testing"
	"time"

	"water/internal/runtime"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)} }

// ---- transition table ----

func TestTransitions(t *testing.T) {
	fc := newFakeClock()
	tbl := NewTable(fc.now)

	// Partial creates a listening turn.
	if _, err := tbl.Partial("c1", runtime.ChannelVoice, 1); err != nil {
		t.Fatalf("Partial: %v", err)
	}
	if tn, ok := tbl.Get("c1"); !ok || tn.State != StateListening {
		t.Fatalf("after Partial: %+v ok=%v", tn, ok)
	}

	// Final moves listening -> final, keyed by the daemon task id.
	if _, err := tbl.Final("c1", "task1", runtime.ChannelVoice); err != nil {
		t.Fatalf("Final: %v", err)
	}
	if _, ok := tbl.Get("c1"); ok {
		t.Fatal("the client-id key should be gone once Final fires")
	}
	if tn, ok := tbl.Get("task1"); !ok || tn.State != StateFinal {
		t.Fatalf("after Final: %+v ok=%v", tn, ok)
	}

	// Route: final -> routed.
	if err := tbl.Route("task1", OwnerQuick); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if tn, ok := tbl.Get("task1"); !ok || tn.State != StateRouted || tn.Owner != OwnerQuick {
		t.Fatalf("after Route: %+v ok=%v", tn, ok)
	}

	// Done: routed -> done.
	if err := tbl.Done("task1", StateDone); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if tn, ok := tbl.Get("task1"); !ok || tn.State != StateDone {
		t.Fatalf("after Done: %+v ok=%v", tn, ok)
	}
}

func TestIllegalTransitions(t *testing.T) {
	fc := newFakeClock()
	tbl := NewTable(fc.now)

	// Route before Final: unknown id.
	if err := tbl.Route("nope", OwnerQuick); err != ErrUnknown {
		t.Fatalf("Route(unknown) = %v, want ErrUnknown", err)
	}
	// Done before Final/Route: unknown id.
	if err := tbl.Done("nope", StateDone); err != ErrUnknown {
		t.Fatalf("Done(unknown) = %v, want ErrUnknown", err)
	}

	if _, err := tbl.Partial("c1", runtime.ChannelVoice, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := tbl.Final("c1", "task1", runtime.ChannelVoice); err != nil {
		t.Fatal(err)
	}

	// A second Partial for the same client id, after Final already
	// consumed it, creates a fresh listening turn under that id rather
	// than erroring — c1's old entry is gone (moved to task1). This is
	// expected: a genuinely new "c1" key after Final is a brand new turn.
	if _, err := tbl.Partial("c1", runtime.ChannelVoice, 1); err != nil {
		t.Fatalf("Partial on a fresh client id after Final moved the old one: %v", err)
	}

	// Route twice: the second is illegal (not StateFinal any more).
	if err := tbl.Route("task1", OwnerQuick); err != nil {
		t.Fatal(err)
	}
	if err := tbl.Route("task1", OwnerMain); err != ErrState {
		t.Fatalf("second Route = %v, want ErrState", err)
	}

	// Done with an invalid terminal state.
	if err := tbl.Done("task1", StateExpired); err != ErrState {
		t.Fatalf("Done(StateExpired) = %v, want ErrState (only Sweep/eviction sets Expired)", err)
	}

	// Done twice.
	if err := tbl.Done("task1", StateDone); err != nil {
		t.Fatal(err)
	}
	if err := tbl.Done("task1", StateDone); err != ErrState {
		t.Fatalf("second Done = %v, want ErrState", err)
	}
}

// ---- Route CAS under concurrency ----

func TestRouteExactlyOneWinnerUnderConcurrency(t *testing.T) {
	fc := newFakeClock()
	tbl := NewTable(fc.now)
	if _, err := tbl.Final("", "task1", runtime.ChannelCLI); err != nil {
		t.Fatal(err)
	}

	const n = 50
	var wg sync.WaitGroup
	wins := make([]bool, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			wins[i] = tbl.Route("task1", OwnerMain) == nil
		}(i)
	}
	wg.Wait()

	winCount := 0
	for _, w := range wins {
		if w {
			winCount++
		}
	}
	if winCount != 1 {
		t.Fatalf("winCount = %d, want exactly 1", winCount)
	}
}

// ---- Emitter ----

func TestEmitterDropsNonOwnerAndAfterDone(t *testing.T) {
	fc := newFakeClock()
	tbl := NewTable(fc.now)
	if _, err := tbl.Final("", "task1", runtime.ChannelCLI); err != nil {
		t.Fatal(err)
	}
	if err := tbl.Route("task1", OwnerQuick); err != nil {
		t.Fatal(err)
	}

	var delivered []runtime.Event
	sink := func(ev runtime.Event) { delivered = append(delivered, ev) }

	quickEmit := tbl.Emitter("task1", OwnerQuick, sink)
	mainEmit := tbl.Emitter("task1", OwnerMain, sink)

	mainEmit(runtime.Event{Kind: runtime.EventDelta, Text: "should be dropped: main doesn't own this turn"})
	quickEmit(runtime.Event{Kind: runtime.EventDelta, Text: "quick owns it"})

	if len(delivered) != 1 || delivered[0].Text != "quick owns it" {
		t.Fatalf("delivered = %+v, want exactly the quick-owner event", delivered)
	}
	if tn, _ := tbl.Get("task1"); tn.Dropped != 1 {
		t.Fatalf("Dropped = %d, want 1", tn.Dropped)
	}

	if err := tbl.Done("task1", StateDone); err != nil {
		t.Fatal(err)
	}
	quickEmit(runtime.Event{Kind: runtime.EventDelta, Text: "too late"})
	if len(delivered) != 1 {
		t.Fatalf("an emission after Done must be dropped; delivered = %+v", delivered)
	}
}

func TestEmitterRouterHandoffWindow(t *testing.T) {
	fc := newFakeClock()
	tbl := NewTable(fc.now)
	if _, err := tbl.Final("", "task1", runtime.ChannelVoice); err != nil {
		t.Fatal(err)
	}

	var delivered []runtime.Event
	sink := func(ev runtime.Event) { delivered = append(delivered, ev) }
	routerEmit := tbl.Emitter("task1", OwnerRouter, sink)
	mainEmit := tbl.Emitter("task1", OwnerMain, sink)

	// Router may emit its ack while the turn is merely StateFinal.
	routerEmit(runtime.Event{Kind: runtime.EventAck})
	if len(delivered) != 1 {
		t.Fatalf("router ack while final should be delivered, got %+v", delivered)
	}

	if err := tbl.Route("task1", OwnerMain); err != nil {
		t.Fatal(err)
	}

	// Router may still emit its handoff acknowledgement before main's
	// first event.
	routerEmit(runtime.Event{Kind: runtime.EventSentence, Text: "One moment."})
	if len(delivered) != 2 {
		t.Fatalf("router handoff before main's first event should be delivered, got %+v", delivered)
	}

	// Once main has emitted anything, the router's window is closed.
	mainEmit(runtime.Event{Kind: runtime.EventDelta, Text: "the answer"})
	routerEmit(runtime.Event{Kind: runtime.EventSentence, Text: "too late for a handoff"})
	if len(delivered) != 3 {
		t.Fatalf("a router emission after main started should be dropped, got %+v", delivered)
	}

	// Quick never owns this turn at all.
	quickEmit := tbl.Emitter("task1", OwnerQuick, sink)
	quickEmit(runtime.Event{Kind: runtime.EventDelta, Text: "quick shouldn't be able to answer a main-routed turn"})
	if len(delivered) != 3 {
		t.Fatalf("quick emitting on a main-routed turn should be dropped, got %+v", delivered)
	}
}

// ---- Sweep: TTL, maxListening, doneKeep ----

func TestSweepExpiresStaleListening(t *testing.T) {
	fc := newFakeClock()
	tbl := NewTableConfig(Config{ListeningTTL: 30 * time.Second, MaxListening: 8, DoneKeep: 10 * time.Minute}, fc.now)
	if _, err := tbl.Partial("c1", runtime.ChannelVoice, 1); err != nil {
		t.Fatal(err)
	}
	fc.advance(31 * time.Second)
	tbl.Sweep()
	if tn, ok := tbl.Get("c1"); !ok || tn.State != StateExpired {
		t.Fatalf("after TTL sweep: %+v ok=%v, want StateExpired", tn, ok)
	}
}

func TestMaxListeningEvictsOldest(t *testing.T) {
	fc := newFakeClock()
	tbl := NewTableConfig(Config{ListeningTTL: time.Hour, MaxListening: 2, DoneKeep: time.Hour}, fc.now)
	for i, id := range []string{"c1", "c2", "c3"} {
		if _, err := tbl.Partial(id, runtime.ChannelVoice, 1); err != nil {
			t.Fatal(err)
		}
		fc.advance(time.Duration(i) * time.Millisecond)
	}
	// c1 was first in and should be evicted once a 3rd listening turn
	// (c3) pushes the count past MaxListening=2.
	tn1, ok1 := tbl.Get("c1")
	tn2, ok2 := tbl.Get("c2")
	tn3, ok3 := tbl.Get("c3")
	if !ok1 || tn1.State != StateExpired {
		t.Fatalf("c1 = %+v ok=%v, want evicted (StateExpired)", tn1, ok1)
	}
	if !ok2 || tn2.State != StateListening {
		t.Fatalf("c2 = %+v ok=%v, want still StateListening", tn2, ok2)
	}
	if !ok3 || tn3.State != StateListening {
		t.Fatalf("c3 = %+v ok=%v, want still StateListening", tn3, ok3)
	}
}

func TestSweepPrunesAfterDoneKeep(t *testing.T) {
	fc := newFakeClock()
	tbl := NewTableConfig(Config{ListeningTTL: time.Hour, MaxListening: 8, DoneKeep: time.Minute}, fc.now)
	if _, err := tbl.Final("", "task1", runtime.ChannelCLI); err != nil {
		t.Fatal(err)
	}
	if err := tbl.Done("task1", StateDone); err != nil {
		t.Fatal(err)
	}
	// Immediately after Done, a duplicate Done call still sees the real
	// terminal state (not ErrUnknown).
	if err := tbl.Done("task1", StateDone); err != ErrState {
		t.Fatalf("Done on an already-done turn (within DoneKeep) = %v, want ErrState", err)
	}
	fc.advance(2 * time.Minute)
	tbl.Sweep()
	if _, ok := tbl.Get("task1"); ok {
		t.Fatal("a terminal turn older than DoneKeep should be pruned by Sweep")
	}
	// Now a duplicate Done call genuinely doesn't know about it any more.
	if err := tbl.Done("task1", StateDone); err != ErrUnknown {
		t.Fatalf("Done after pruning = %v, want ErrUnknown", err)
	}
}
