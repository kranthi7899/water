package backend

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Registry maps backend names to constructors. A Registry instance (rather than
// only a package global) exists so tests can build a fake registry and assert
// selection behaviour — see the metered-leak guard test.
type Registry struct {
	mu       sync.RWMutex
	backends map[string]Backend
	order    []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{backends: map[string]Backend{}} }

// Register adds a backend. Registration order is the auto-selection order.
func (r *Registry) Register(b Backend) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.backends[b.Name()]; !dup {
		r.order = append(r.order, b.Name())
	}
	r.backends[b.Name()] = b
}

// Get returns a backend by name.
func (r *Registry) Get(name string) (Backend, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.backends[name]
	return b, ok
}

// Names returns registered names in registration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.order...)
}

// All returns every backend in registration order.
func (r *Registry) All() []Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Backend, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.backends[n])
	}
	return out
}

// Default is the process-wide registry populated by init() in each
// implementation file. Adding a backend = one new file + one Register line.
var Default = NewRegistry()

// SelectConfig is the slice of configuration that drives selection.
type SelectConfig struct {
	// Preferred is an explicit backend name, or "auto" / "" for precedence.
	Preferred string
	// AllowMetered permits falling back to a backend whose Availability.Metered
	// is true. Default false.
	AllowMetered bool
}

// Selection is the result of Select, including why the backend was chosen.
type Selection struct {
	Backend      Backend
	Availability Availability
	Reason       string
}

// ErrNoBackend is returned when nothing usable is available.
var ErrNoBackend = errors.New("no usable backend")

// ErrMeteredRefused is returned when the only usable backend is metered and
// allow_metered is false.
var ErrMeteredRefused = errors.New("only metered backends are available and backend.allow_metered is false")

// Select is the ONLY place backend selection is resolved. Precedence:
//
//  1. explicit config/flag (Preferred != "" && != "auto")
//  2. first available non-metered backend, in registration order
//  3. metered fallback — only if AllowMetered
//
// An explicit metered preference is still refused unless AllowMetered is set;
// a user must opt into paying twice, never fall into it.
func Select(ctx context.Context, reg *Registry, cfg SelectConfig) (Selection, error) {
	pref := strings.TrimSpace(strings.ToLower(cfg.Preferred))
	if pref != "" && pref != "auto" {
		b, ok := reg.Get(pref)
		if !ok {
			return Selection{}, fmt.Errorf("backend %q is not registered (known: %s)", pref, strings.Join(reg.Names(), ", "))
		}
		av := b.Available(ctx)
		if !av.Usable() {
			return Selection{}, fmt.Errorf("backend %q is not usable: %s", pref, av.Detail)
		}
		if av.Metered && !cfg.AllowMetered {
			return Selection{}, fmt.Errorf("backend %q is metered: %w", pref, ErrMeteredRefused)
		}
		return Selection{Backend: b, Availability: av, Reason: "explicit preference"}, nil
	}

	var meteredCandidate *Selection
	var details []string
	for _, b := range reg.All() {
		av := b.Available(ctx)
		details = append(details, fmt.Sprintf("%s: %s", b.Name(), av.Detail))
		if !av.Usable() {
			continue
		}
		if !av.Metered {
			return Selection{Backend: b, Availability: av, Reason: "first available non-metered backend"}, nil
		}
		if meteredCandidate == nil {
			meteredCandidate = &Selection{Backend: b, Availability: av, Reason: "metered fallback (allow_metered=true)"}
		}
	}
	if meteredCandidate != nil {
		if cfg.AllowMetered {
			return *meteredCandidate, nil
		}
		return Selection{}, ErrMeteredRefused
	}
	sort.Strings(details)
	return Selection{}, fmt.Errorf("%w: %s", ErrNoBackend, strings.Join(details, "; "))
}
