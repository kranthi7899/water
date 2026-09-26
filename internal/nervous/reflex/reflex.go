// Package reflex holds Tier 0's read-only handlers: the audited, fixed set
// of functions a quick answer or a quick.* tool call may ever run. Every handler here reads the twin's local store only — never a
// connector, never the gate, never a subprocess, never a mutation. That
// invariant is enforced structurally, not by convention: see
// imports_test.go's import denylist and forbidden-selector check.
package reflex

import (
	"context"
	"time"

	"water/internal/approvals"
	"water/internal/nervous/intents"
	"water/internal/nervous/render"
	"water/internal/nervous/slots"
	"water/internal/store"
	"water/internal/twins"
)

// StoreView is the read-only slice of *store.Store a reflex handler may
// use. Production code constructs it over a store opened with
// store.OpenReadOnly (internal/nervous/reflex/storeview.go), so a write
// attempt fails at the SQLite level even if a handler somehow tried one.
type StoreView interface {
	EventsInRange(ctx context.Context, from, to time.Time) ([]store.Event, error)
	NextEvent(ctx context.Context, after time.Time) (*store.Event, error)
	LatestMessages(ctx context.Context, limit int) ([]store.Message, error)
	MessagesFrom(ctx context.Context, email string, limit int) ([]store.Message, error)
	CountMessagesSince(ctx context.Context, since time.Time) (int, error)
	Senders(ctx context.Context, since time.Time, limit int) ([]slots.Person, error)
	CursorUpdatedAt(ctx context.Context, key string) (time.Time, bool, error)
}

// PendingLister is the read-only slice of *approvals.Queue a reflex handler
// may use.
type PendingLister interface {
	Pending(ctx context.Context) ([]approvals.Envelope, error)
}

// TaskControl lets control.cancel_tasks reach the daemon's in-flight-turn
// bookkeeping without reflex importing the gateway. The real implementation
// is wired in a later task (R-15); tests use a fake.
type TaskControl interface {
	Running() int
	CancelAllExcept(taskID string) int
}

// TierHealth is a minimal, package-local stand-in for
// internal/nervous.TierHealth (which doesn't exist until a later task).
// status.overview reports it when Deps.Health is non-nil.
type TierHealth struct {
	Tier    string
	Enabled bool
	State   string
	Reason  string
}

// IntentSummary is what help.intents lists: the fields a registry entry
// exposes without reflex needing to import internal/nervous/intents'
// Registry type (which doesn't have a stable public listing shape until a
// later task uses it here).
type IntentSummary struct {
	ID          string
	Description string
}

// Deps is every handler's readable world. All fields it doesn't need may
// be left zero; nil Health and nil Registry are both handled explicitly by
// the one handler (status.overview, help.intents) that reads them.
type Deps struct {
	Store     StoreView
	Approvals PendingLister
	// Brief reads day's cached brief only — no model call. Normally
	// water/internal/runtime.CachedBrief, adapted to this signature by the
	// caller that builds Deps (runtime imports reflex indirectly through
	// nervous in a later task; reflex itself never imports runtime, to
	// keep the dependency direction the plan specifies: nervous -> runtime
	// and nervous -> reflex, never reflex -> runtime).
	Brief func(ctx context.Context, day string) (text string, tainted bool, ok bool, err error)
	Tasks TaskControl
	// Health may be nil; status.overview omits that section when it is.
	Health   func() []TierHealth
	Manifest *twins.Manifest
	Registry []IntentSummary
	Now      func() time.Time
	TaskID   string
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Args is a resolved slot map, keyed by slot name.
type Args map[string]slots.Value

// Handler pairs a function's declared contract with its implementation.
type Handler struct {
	Spec intents.FunctionSpec
	Run  func(ctx context.Context, d Deps, a Args) (render.Result, error)
}
