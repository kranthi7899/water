// Package turn is the front door's turn state machine: it guarantees
// exactly one owner ever answers a turn, regardless of how many tiers or
// speculative work are racing to produce that answer. It is pure — no
// store, no backend, no network — so nervous.Handle and speculate.go can
// depend on it without pulling in anything else.
package turn

import (
	"context"
	"errors"
	"sync"
	"time"

	"water/internal/runtime"
)

// State is where a turn is in its life. listening precedes a final
// transcript (partials only); final means the utterance is known but not
// yet routed; routed means one owner (quick or main) is answering;
// done/expired/cancelled are terminal.
type State string

const (
	StateListening State = "listening"
	StateFinal     State = "final"
	StateRouted    State = "routed"
	StateDone      State = "done"
	StateExpired   State = "expired"
	StateCancelled State = "cancelled"
)

func (s State) terminal() bool {
	return s == StateDone || s == StateExpired || s == StateCancelled
}

// Owner names who is answering a routed turn. "" means unrouted (or the
// router itself, for its own ack/handoff emissions).
type Owner string

const (
	OwnerNone   Owner = ""
	OwnerRouter Owner = "router"
	OwnerQuick  Owner = "quick"
	OwnerMain   Owner = "main"
)

var (
	// ErrState is returned when a requested transition isn't legal from
	// the turn's current state (e.g. Route on an already-routed turn).
	ErrState = errors.New("turn: illegal state transition")
	// ErrUnknown is returned when an operation names a turn id the table
	// has no record of (and the operation isn't one that creates one).
	ErrUnknown = errors.New("turn: unknown turn")
)

// Turn is one in-flight (or recently finished) turn's state.
type Turn struct {
	ID       string
	ClientID string
	Channel  runtime.Channel

	State State
	Owner Owner

	Partials       int
	FirstPartialAt time.Time
	FinalAt        time.Time
	doneAt         time.Time

	// mainStarted is set the first time an event from OwnerMain passes
	// through Emitter. The router may still emit its handoff
	// acknowledgement up to that point, never after.
	mainStarted bool

	// Dropped counts events Emitter refused to deliver, for tests and
	// diagnostics.
	Dropped int

	// Spec is an opaque slot for whatever speculative work (a later,
	// separate task, in package nervous) wants to attach to a listening
	// turn. This package never reads or writes anything into it beyond
	// carrying the pointer, which keeps this package free of a dependency
	// on nervous (which itself depends on turn).
	Spec any
}

// Config bounds the table's bookkeeping.
type Config struct {
	// ListeningTTL: a turn stuck in StateListening longer than this (no
	// final transcript arrived) is swept to StateExpired.
	ListeningTTL time.Duration
	// MaxListening caps how many turns may be StateListening at once; the
	// oldest is evicted (expired) to make room for a new one.
	MaxListening int
	// DoneKeep: a terminal turn is retained for this long (so a late
	// duplicate Final/Route/Done call still sees a real error, not
	// ErrUnknown) before Sweep prunes it.
	DoneKeep time.Duration
}

// DefaultConfig matches docs/slices/R.md's Design §11.1 defaults.
func DefaultConfig() Config {
	return Config{ListeningTTL: 30 * time.Second, MaxListening: 8, DoneKeep: 10 * time.Minute}
}

// Table is the one shared turn-state table for a daemon.
type Table struct {
	mu    sync.Mutex
	cfg   Config
	now   func() time.Time
	turns map[string]*Turn
	order []string // insertion order of listening turns, oldest first, for MaxListening eviction
}

// NewTable builds a Table with DefaultConfig and the given clock (pass a
// fake clock in tests).
func NewTable(now func() time.Time) *Table {
	return NewTableConfig(DefaultConfig(), now)
}

// NewTableConfig builds a Table with an explicit Config.
func NewTableConfig(cfg Config, now func() time.Time) *Table {
	return &Table{cfg: cfg, now: now, turns: make(map[string]*Turn)}
}

// Partial records one partial transcript under clientID, creating a new
// listening turn if none exists yet for that client, or extending one
// already listening. It is an error to post a partial for a turn that has
// already gone final or further.
func (t *Table) Partial(clientID string, ch runtime.Channel, seq int) (*Turn, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	tn, ok := t.turns[clientID]
	if !ok {
		tn = &Turn{ID: clientID, ClientID: clientID, Channel: ch, State: StateListening}
		tn.FirstPartialAt = t.now()
		t.turns[clientID] = tn
		t.order = append(t.order, clientID)
		t.evictOldestListeningLocked()
	}
	if tn.State != StateListening {
		return nil, ErrState
	}
	tn.Partials = seq
	return tn, nil
}

// evictOldestListeningLocked expires the oldest StateListening turn once
// more than MaxListening are open. Callers hold t.mu.
func (t *Table) evictOldestListeningLocked() {
	if t.cfg.MaxListening <= 0 {
		return
	}
	listening := 0
	for _, id := range t.order {
		if tn, ok := t.turns[id]; ok && tn.State == StateListening {
			listening++
		}
	}
	for listening > t.cfg.MaxListening {
		for _, id := range t.order {
			tn, ok := t.turns[id]
			if !ok || tn.State != StateListening {
				continue
			}
			tn.State = StateExpired
			tn.doneAt = t.now()
			listening--
			break
		}
	}
}

// Final records the arrival of a final transcript: the turn is now known
// and ready to be routed, under the daemon's own task id (which becomes
// the turn's canonical ID from here on — a client-supplied ClientID from
// Partial calls is carried over for correlation). Valid from absent or
// StateListening only.
func (t *Table) Final(clientID, taskID string, ch runtime.Channel) (*Turn, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	var tn *Turn
	if clientID != "" {
		if existing, ok := t.turns[clientID]; ok {
			if existing.State != StateListening {
				return nil, ErrState
			}
			tn = existing
			delete(t.turns, clientID)
		}
	}
	if tn == nil {
		tn = &Turn{ClientID: clientID, Channel: ch}
	}
	tn.ID = taskID
	tn.Channel = ch
	tn.State = StateFinal
	tn.FinalAt = t.now()
	t.turns[taskID] = tn
	t.order = append(t.order, taskID)
	return tn, nil
}

// Route assigns a turn's owner exactly once (compare-and-set): only the
// first caller for a given turn ID succeeds; every later call, including a
// concurrent racer, gets ErrState.
func (t *Table) Route(id string, o Owner) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	tn, ok := t.turns[id]
	if !ok {
		return ErrUnknown
	}
	if tn.State != StateFinal {
		return ErrState
	}
	tn.State = StateRouted
	tn.Owner = o
	return nil
}

// Done marks a turn terminal (StateDone or StateCancelled — StateExpired is
// only ever set by the sweep/eviction paths). Valid from StateFinal or
// StateRouted.
func (t *Table) Done(id string, st State) error {
	if st != StateDone && st != StateCancelled {
		return ErrState
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	tn, ok := t.turns[id]
	if !ok {
		return ErrUnknown
	}
	if tn.State != StateFinal && tn.State != StateRouted {
		return ErrState
	}
	tn.State = st
	tn.doneAt = t.now()
	return nil
}

// Get returns a snapshot of a turn's current state, for tests and
// diagnostics.
func (t *Table) Get(id string) (Turn, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tn, ok := t.turns[id]
	if !ok {
		return Turn{}, false
	}
	return *tn, true
}

// SetSpec attaches (or clears, with a nil spec) speculative work to a turn
// this table still knows about, keyed by whatever id the caller currently
// addresses it by (clientID while listening; the daemon task id from Final
// onward — Final reindexes the same *Turn under that id, carrying Spec over
// automatically). A caller naming a turn id the table has no record of is a
// silent no-op: speculate.go's own background work (package nervous) must
// never fail a request over a turn that has already expired or been
// forgotten.
func (t *Table) SetSpec(id string, spec any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if tn, ok := t.turns[id]; ok {
		tn.Spec = spec
	}
}

// GetSpec returns whatever speculative work (Turn.Spec's own doc comment)
// is currently attached to id, and whether the turn itself is still known
// at all.
func (t *Table) GetSpec(id string) (any, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tn, ok := t.turns[id]
	if !ok {
		return nil, false
	}
	return tn.Spec, true
}

// Emitter returns a function that delivers an event only while o currently
// owns id, dropping (and counting) anything else:
//   - OwnerRouter may emit only while the turn is StateFinal, or
//     StateRouted(main) before the main path's own first event.
//   - OwnerQuick may emit only while StateRouted(quick).
//   - OwnerMain may emit only while StateRouted(main); its first emission
//     closes the router's own handoff window.
//   - Nothing may emit once the turn is terminal.
func (t *Table) Emitter(id string, o Owner, emit func(runtime.Event)) func(runtime.Event) {
	return func(ev runtime.Event) {
		if !t.allow(id, o) {
			return
		}
		emit(ev)
	}
}

func (t *Table) allow(id string, o Owner) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	tn, ok := t.turns[id]
	if !ok || tn.State.terminal() {
		if ok {
			tn.Dropped++
		}
		return false
	}

	switch o {
	case OwnerRouter:
		if tn.State == StateFinal || (tn.State == StateRouted && tn.Owner == OwnerMain && !tn.mainStarted) {
			return true
		}
	case OwnerQuick:
		if tn.State == StateRouted && tn.Owner == OwnerQuick {
			return true
		}
	case OwnerMain:
		if tn.State == StateRouted && tn.Owner == OwnerMain {
			tn.mainStarted = true
			return true
		}
	}
	tn.Dropped++
	return false
}

// Sweep expires listening turns older than ListeningTTL and prunes
// terminal turns older than DoneKeep. Call it periodically (the daemon's
// existing tick is a natural place).
func (t *Table) Sweep() {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	var kept []string
	for _, id := range t.order {
		tn, ok := t.turns[id]
		if !ok {
			continue
		}
		if tn.State == StateListening && now.Sub(tn.FirstPartialAt) > t.cfg.ListeningTTL {
			tn.State = StateExpired
			tn.doneAt = now
		}
		if tn.State.terminal() && !tn.doneAt.IsZero() && now.Sub(tn.doneAt) > t.cfg.DoneKeep {
			delete(t.turns, id)
			continue
		}
		kept = append(kept, id)
	}
	t.order = kept
}

// DefaultSweepInterval is how often RunSweep calls Sweep when the daemon
// doesn't override it.
const DefaultSweepInterval = time.Minute

// RunSweep calls Sweep every interval until ctx is done. Nothing else ever
// removes an entry from the table (Partial/Final only add), so without a
// caller running this for the life of the process, turns and their order
// index grow without bound, and evictOldestListeningLocked's per-partial
// scan over the whole (never-shrinking) order slice gets slower forever.
// The daemon runs this in its own goroutine tied to its own shutdown
// context; interval <= 0 means DefaultSweepInterval.
func (t *Table) RunSweep(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultSweepInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.Sweep()
		}
	}
}
