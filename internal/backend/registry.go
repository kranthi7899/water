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
//
// Part 8 precedence, highest first: --backend flag → role.yaml backend: →
// config file default → first available non-metered → metered only if
// AllowMetered. Callers fill Flag/Role/Config; Select resolves them here and
// nowhere else.
type SelectConfig struct {
	// Preferred is an explicit backend name, or "auto" / "" for precedence.
	// (Legacy single-source field; equivalent to Config below.)
	Preferred string
	// Flag is the --backend flag value ("" = unset).
	Flag string
	// Role is the role.yaml backend: value ("" = inherit).
	Role string
	// RoleSlug names the role being resolved, for the reason string.
	RoleSlug string
	// AllowMetered permits falling back to a backend whose Availability.Metered
	// is true. Default false.
	AllowMetered bool
}

// preferred resolves the effective explicit preference and its source.
func (c SelectConfig) preferred() (string, string) {
	norm := func(s string) string { return strings.TrimSpace(strings.ToLower(s)) }
	if v := norm(c.Flag); v != "" && v != "auto" {
		return v, "--backend flag"
	}
	if v := norm(c.Role); v != "" && v != "auto" {
		return v, "role.yaml backend: (" + c.RoleSlug + ")"
	}
	if v := norm(c.Preferred); v != "" && v != "auto" {
		return v, "config backend.preferred"
	}
	return "", "auto"
}

// Selection is the result of Select, including why the backend was chosen.
type Selection struct {
	Backend      Backend
	Availability Availability
	Reason       string
	Source       string // which layer decided: flag | role | config | auto
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
	pref, source := cfg.preferred()
	if pref != "" {
		b, ok := reg.Get(pref)
		if !ok {
			return Selection{}, fmt.Errorf("backend %q (from %s) is not registered (known: %s)", pref, source, strings.Join(reg.Names(), ", "))
		}
		av := b.Available(ctx)
		if !av.Usable() {
			return Selection{}, fmt.Errorf("backend %q (from %s) is not usable: %s", pref, source, av.Detail)
		}
		if av.Metered && !cfg.AllowMetered {
			return Selection{}, fmt.Errorf("backend %q (from %s) is metered: %w", pref, source, ErrMeteredRefused)
		}
		return Selection{Backend: b, Availability: av, Reason: "explicit preference via " + source, Source: source}, nil
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
			return Selection{Backend: b, Availability: av, Reason: "first available non-metered backend", Source: "auto"}, nil
		}
		if meteredCandidate == nil {
			meteredCandidate = &Selection{Backend: b, Availability: av, Reason: "metered fallback (allow_metered=true)", Source: "auto"}
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
