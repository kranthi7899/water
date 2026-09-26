package memory

import (
	"context"
	"errors"
)

// Backend is the storage seam under a Store. It moves records and nothing
// else: validation, the never-store check, bounds, "current" and search
// all live in this package (store.go), so every backend gets them
// identically and a backend is never handed an unchecked record.
//
// A Backend is keyed by twin id. Callers never hold one to read or write
// memory: they receive a Store from Bind, which has no twin parameter.
//
// SQLite (sqlite.go) is the only backend (owner decision, 2026-09-25). A
// second backend must pass storetest's contract suite unchanged.
type Backend interface {
	// Update runs fn in one atomic transaction over twin's records: every
	// Tx call fn makes commits together, or none does if fn returns an
	// error. Concurrent Updates are serialized.
	Update(ctx context.Context, twin string, fn func(Tx) error) error
	// Get returns twin's record id, live or not, or ErrNotFound.
	Get(ctx context.Context, twin, id string) (Record, error)
	// List returns twin's records narrowed by f, in any order.
	List(ctx context.Context, twin string, f Filter) ([]Record, error)
}

// Filter is the narrowing a backend may push down to its index. The Store
// applies every other condition itself.
type Filter struct {
	Types    []Type // empty = every type
	LiveOnly bool   // true = only records with no Invalidation
}

// Tx is a Backend's view of one transaction, already scoped to a twin.
type Tx interface {
	Get(ctx context.Context, id string) (Record, error)
	// Live returns every record with no Invalidation.
	Live(ctx context.Context) ([]Record, error)
	// Insert appends r. r.Invalidation is nil.
	Insert(ctx context.Context, r Record) error
	// Invalidate sets id's Invalidation. ErrNotFound for an unknown id,
	// ErrAlreadyInvalidated if it already has one.
	Invalidate(ctx context.Context, id string, inv Invalidation) error
}

var (
	// ErrNotFound is returned for an id the twin has no record of.
	ErrNotFound = errors.New("memory record not found")
	// ErrAlreadyInvalidated is returned when superseding or invalidating a
	// record that is already invalidated: you correct the current belief,
	// not a dead one.
	ErrAlreadyInvalidated = errors.New("memory record is already invalidated")
)
