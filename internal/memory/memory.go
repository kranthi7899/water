// Package memory is extension point 2: bounded, curated, per-role memory.
//
// The pattern is Hermes's: one markdown file per role, parsed into entries,
// loaded ONCE per run as a frozen snapshot, managed explicitly via
// add/replace/remove. No automatic growth, decay, or scoring — unbounded
// memory drift is what a fixed identity anchor exists to prevent.
package memory

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"sync"
	"time"
)

// Entry is one curated memory item.
type Entry struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
	Tags      []string  `json:"tags,omitempty"`
}

// Provider is the storage seam. Implementations register by name.
type Provider interface {
	Name() string
	// Snapshot returns the frozen session-start view. Called ONCE per run.
	Snapshot(ctx context.Context, role string) ([]Entry, error)
	Add(ctx context.Context, role string, e Entry) error
	Replace(ctx context.Context, role, id string, e Entry) error
	Remove(ctx context.Context, role, id string) error
}

// Limits are the configured ceilings. Exceeding them is an explicit error
// (ErrBoundsExceeded) that prompts pruning — never silent truncation.
type Limits struct {
	MaxEntries int
	MaxBytes   int
}

// DefaultLimits mirrors the config defaults.
var DefaultLimits = Limits{MaxEntries: 200, MaxBytes: 32768}

// ErrBoundsExceeded is returned when a write would exceed Limits.
var ErrBoundsExceeded = errors.New("memory bounds exceeded; run `water memory <role> prune`")

// ErrNotFound is returned for an unknown entry id.
var ErrNotFound = errors.New("memory entry not found")

// CheckBounds validates a candidate entry set against limits.
func CheckBounds(entries []Entry, l Limits) error {
	if l.MaxEntries > 0 && len(entries) > l.MaxEntries {
		return fmt.Errorf("%w: %d entries > max_entries %d", ErrBoundsExceeded, len(entries), l.MaxEntries)
	}
	if l.MaxBytes > 0 {
		n := 0
		for _, e := range entries {
			n += len(e.Text) + len(e.ID) + 40
		}
		if n > l.MaxBytes {
			return fmt.Errorf("%w: %d bytes > max_bytes %d", ErrBoundsExceeded, n, l.MaxBytes)
		}
	}
	return nil
}

// Scoped is a Provider bound to exactly one role. It is the ONLY memory handle
// a node ever receives. There is deliberately no method that takes a role
// argument, so no code path exists by which a node can reach another role's
// memory. This is a property of the API surface, not a convention.
type Scoped interface {
	Role() string
	Snapshot(ctx context.Context) ([]Entry, error)
	Add(ctx context.Context, e Entry) error
	Replace(ctx context.Context, id string, e Entry) error
	Remove(ctx context.Context, id string) error
}

type scoped struct {
	role string
	p    Provider
}

// Bind scopes a Provider to a single role. The role is captured privately and
// cannot be changed or read back as a provider.
func Bind(p Provider, role string) Scoped { return &scoped{role: role, p: p} }

func (s *scoped) Role() string { return s.role }
func (s *scoped) Snapshot(ctx context.Context) ([]Entry, error) {
	return s.p.Snapshot(ctx, s.role)
}
func (s *scoped) Add(ctx context.Context, e Entry) error { return s.p.Add(ctx, s.role, e) }
func (s *scoped) Replace(ctx context.Context, id string, e Entry) error {
	return s.p.Replace(ctx, s.role, id, e)
}
func (s *scoped) Remove(ctx context.Context, id string) error { return s.p.Remove(ctx, s.role, id) }

// Options are passed to provider factories.
type Options struct {
	// Root is the writable data directory (e.g. ~/.water/memory).
	Root string
	// Seed, if set, is an fs rooted at the agents tree; <role>/memory/session.md
	// seeds a role's file on first use.
	Seed   fs.FS
	Limits Limits
}

// Factory constructs a Provider from Options.
type Factory func(Options) (Provider, error)

var (
	regMu     sync.RWMutex
	factories = map[string]Factory{}
)

// Register adds a provider factory by name.
func Register(name string, f Factory) {
	regMu.Lock()
	defer regMu.Unlock()
	factories[name] = f
}

// Open constructs the named provider.
func Open(name string, opts Options) (Provider, error) {
	regMu.RLock()
	f, ok := factories[name]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("memory provider %q is not registered (known: %v)", name, Names())
	}
	return f(opts)
}

// Names lists registered providers.
func Names() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(factories))
	for n := range factories {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
