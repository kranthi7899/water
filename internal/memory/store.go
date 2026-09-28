// Package memory is Water's long-term memory: distilled facts with
// provenance, never the source content itself (Slice F).
//
// A Record says "the CEO prefers board updates on the first Monday" and
// cites where that came from (an audit sequence number and a source ref
// such as "gmail:msg-abc123"). It never holds the email that said so.
// Writes are append-only: a correction invalidates the old record and
// links the new one to it, never edits or deletes, so "what did the twin
// believe on a given day, and why?" always has an answer.
//
// docs/CONTEXT.md principle 6 is enforced here in code: a record is written
// only from an explicit CEO statement or an approved correction/proposal,
// each with provenance (Record.Validate), and never-store content is
// refused at write time and on read (CheckNeverStore).
//
// Nothing outside this package uses it yet. Wiring it into the daemon, the
// system prompt, the model's tools, config and the CLI is Slice G.
package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Store is a twin's memory. It is the only handle a caller receives (from
// Bind), and deliberately no method takes a twin id, so no code path
// exists by which one twin can reach another's memory. That is a property
// of the API surface, not a convention.
type Store interface {
	// Write validates r (including the never-store check) and appends it.
	// The store assigns r.ID; r.Supersedes and r.Invalidation must be
	// unset (a correction goes through Supersede).
	Write(ctx context.Context, r Record) (Record, error)
	// Supersede writes next with next.Supersedes = oldID and invalidates
	// oldID with SupersededBy = next.ID, atomically: both happen or
	// neither does.
	Supersede(ctx context.Context, oldID string, next Record, inv Invalidation) (Record, error)
	// Invalidate retires a record with no replacement (the fact is simply
	// no longer true). Invalidating an already-invalidated record is an
	// error.
	Invalidate(ctx context.Context, id string, inv Invalidation) error

	// Get returns any record, live or not.
	Get(ctx context.Context, id string) (Record, error)
	// Current returns the records current at now (IsCurrent) that match
	// q's Types, Subjects and Sensitivities, up to q.Limit. q.Text and
	// q.IncludeHistory are ignored.
	Current(ctx context.Context, now time.Time, q Query) ([]Record, error)
	// Search is a deterministic case-insensitive term match over Statement
	// and Subjects, filtered by q. Without q.IncludeHistory only records
	// current at now are returned; with it, every record matches.
	Search(ctx context.Context, now time.Time, q Query) ([]Record, error)
	// ListByType returns records of type t: current ones, or every one
	// when includeHistory is set.
	ListByType(ctx context.Context, t Type, now time.Time, includeHistory bool) ([]Record, error)
	// DueForReview returns current records whose ReviewAfter is at or
	// before now, earliest review first.
	DueForReview(ctx context.Context, now time.Time) ([]Record, error)
	// History returns the supersedes chain containing id, oldest first.
	History(ctx context.Context, id string) ([]Record, error)
}

// Query narrows Current and Search. Every non-empty list is a filter; a
// record passes when it matches any value in each list. Results are
// ordered by ObservedAt descending, then ID.
type Query struct {
	Text           string // Search only: whitespace-separated terms, all must match
	Types          []Type
	Subjects       []string // exact ref match, case-insensitive
	Sensitivities  []Sensitivity
	IncludeHistory bool // Search only
	Limit          int  // <= 0 = no limit
}

// Limits are the ceilings on a twin's live (not invalidated) records.
// Exceeding them is an explicit error, never silent truncation. A zero
// field means no limit on that dimension.
type Limits struct {
	MaxEntries int
	MaxBytes   int
}

// DefaultLimits are the council-era defaults, carried over.
var DefaultLimits = Limits{MaxEntries: 200, MaxBytes: 32768}

// ErrBoundsExceeded is returned when a write would take a twin's live
// records past its Limits.
var ErrBoundsExceeded = errors.New("memory bounds exceeded: invalidate records that are no longer true, or raise the limits")

// recordSize is a record's weight against Limits.MaxBytes.
func recordSize(r Record) int {
	n := len(r.ID) + len(r.Statement) + 40
	for _, s := range r.Subjects {
		n += len(s)
	}
	return n
}

// CheckBounds validates a set of live records against l.
func CheckBounds(live []Record, l Limits) error {
	if l.MaxEntries > 0 && len(live) > l.MaxEntries {
		return fmt.Errorf("%w: %d live records > max_entries %d", ErrBoundsExceeded, len(live), l.MaxEntries)
	}
	if l.MaxBytes > 0 {
		n := 0
		for _, r := range live {
			n += recordSize(r)
		}
		if n > l.MaxBytes {
			return fmt.Errorf("%w: %d bytes > max_bytes %d", ErrBoundsExceeded, n, l.MaxBytes)
		}
	}
	return nil
}

var twinIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Bind scopes a Backend to one twin ("ceo", "ceo-demo", "counterparty").
// The twin id is captured privately and cannot be changed or read back.
func Bind(b Backend, twinID string, l Limits) (Store, error) {
	if b == nil {
		return nil, errors.New("memory: nil backend")
	}
	if !twinIDRe.MatchString(twinID) {
		return nil, fmt.Errorf("memory: invalid twin id %q", twinID)
	}
	return &bound{b: b, twin: twinID, limits: l}, nil
}

type bound struct {
	b      Backend
	twin   string
	limits Limits
}

var _ Store = (*bound)(nil)

// supersedeFault, when set (tests only, via export_test.go), runs inside
// Supersede's transaction between writing the new record and invalidating
// the old one. An error from it must leave neither half applied.
var supersedeFault func() error

var idRe = regexp.MustCompile(`^mem_[0-9a-f]{24}$`)

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "mem_" + hex.EncodeToString(b[:])
}

// utc strips the monotonic reading and location so a stored time compares
// equal after a round trip.
func utc(t time.Time) time.Time { return t.UTC() }

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := utc(*t)
	return &u
}

// prepare checks the caller-supplied shape of a new record and normalizes
// it: a fresh id, UTC times, ValidFrom defaulted, empty Subjects as nil.
func prepare(r Record) (Record, error) {
	if r.ID != "" {
		return Record{}, invalid("id is assigned by the store; leave it empty")
	}
	if r.Invalidation != nil {
		return Record{}, invalid("a new record cannot already be invalidated")
	}
	r.ID = newID()
	r.Time.ObservedAt = utc(r.Time.ObservedAt)
	if r.Time.ValidFrom.IsZero() {
		r.Time.ValidFrom = r.Time.ObservedAt
	} else {
		r.Time.ValidFrom = utc(r.Time.ValidFrom)
	}
	r.Time.ValidUntil = utcPtr(r.Time.ValidUntil)
	r.Time.ReviewAfter = utcPtr(r.Time.ReviewAfter)
	if len(r.Subjects) == 0 {
		r.Subjects = nil
	} else {
		r.Subjects = append([]string(nil), r.Subjects...)
	}
	return r, nil
}

// checkWrite is the package-level gate every write passes before any
// backend sees the record: the never-store check first (so a pasted body
// is reported as such, not as a shape error), then Validate.
func checkWrite(r Record) error {
	if err := CheckNeverStore(r); err != nil {
		return err
	}
	return r.Validate()
}

func checkInvalidation(inv Invalidation) error {
	if err := CheckInvalidation(inv); err != nil {
		return err
	}
	return inv.Validate()
}

// checkRead is the same gate on the way out: a hand-edited or corrupted
// store holding a forbidden or malformed value fails loudly rather than
// serving it. The error never echoes record content.
func checkRead(r Record) (Record, error) {
	if !idRe.MatchString(r.ID) {
		return Record{}, fmt.Errorf("memory: a stored record has a malformed id: %w", ErrInvalidRecord)
	}
	if err := CheckNeverStore(r); err != nil {
		return Record{}, fmt.Errorf("memory: record %s failed to load: %w", r.ID, err)
	}
	if err := r.Validate(); err != nil {
		return Record{}, fmt.Errorf("memory: record %s failed to load: %w", r.ID, err)
	}
	return r, nil
}

func checkReadAll(rs []Record) ([]Record, error) {
	for i := range rs {
		if _, err := checkRead(rs[i]); err != nil {
			return nil, err
		}
	}
	return rs, nil
}

func (s *bound) Write(ctx context.Context, r Record) (Record, error) {
	if r.Supersedes != "" {
		return Record{}, invalid("supersedes is set only by Supersede")
	}
	r, err := prepare(r)
	if err != nil {
		return Record{}, err
	}
	if err := checkWrite(r); err != nil {
		return Record{}, err
	}
	err = s.b.Update(ctx, s.twin, func(tx Tx) error {
		live, err := tx.Live(ctx)
		if err != nil {
			return err
		}
		if err := CheckBounds(append(live, r), s.limits); err != nil {
			return err
		}
		return tx.Insert(ctx, r)
	})
	if err != nil {
		return Record{}, err
	}
	return r, nil
}

func (s *bound) Supersede(ctx context.Context, oldID string, next Record, inv Invalidation) (Record, error) {
	if next.Supersedes != "" && next.Supersedes != oldID {
		return Record{}, invalid("next.supersedes must be empty or equal to the superseded id")
	}
	if inv.SupersededBy != "" {
		return Record{}, invalid("invalidation superseded_by is set by the store")
	}
	if !idRe.MatchString(oldID) {
		return Record{}, ErrNotFound
	}
	next.Supersedes = ""
	next, err := prepare(next)
	if err != nil {
		return Record{}, err
	}
	next.Supersedes = oldID
	inv.At = utc(inv.At)
	inv.SupersededBy = next.ID
	if err := checkWrite(next); err != nil {
		return Record{}, err
	}
	if err := checkInvalidation(inv); err != nil {
		return Record{}, err
	}
	err = s.b.Update(ctx, s.twin, func(tx Tx) error {
		old, err := tx.Get(ctx, oldID)
		if err != nil {
			return err
		}
		if _, err := checkRead(old); err != nil {
			return err
		}
		if old.Invalidation != nil {
			return ErrAlreadyInvalidated
		}
		live, err := tx.Live(ctx)
		if err != nil {
			return err
		}
		after := make([]Record, 0, len(live))
		for _, r := range live {
			if r.ID != oldID {
				after = append(after, r)
			}
		}
		if err := CheckBounds(append(after, next), s.limits); err != nil {
			return err
		}
		if err := tx.Insert(ctx, next); err != nil {
			return err
		}
		if supersedeFault != nil {
			if err := supersedeFault(); err != nil {
				return err
			}
		}
		return tx.Invalidate(ctx, oldID, inv)
	})
	if err != nil {
		return Record{}, err
	}
	return next, nil
}

func (s *bound) Invalidate(ctx context.Context, id string, inv Invalidation) error {
	if inv.SupersededBy != "" {
		return invalid("invalidation superseded_by is set only by Supersede")
	}
	if !idRe.MatchString(id) {
		return ErrNotFound
	}
	inv.At = utc(inv.At)
	if err := checkInvalidation(inv); err != nil {
		return err
	}
	return s.b.Update(ctx, s.twin, func(tx Tx) error {
		return tx.Invalidate(ctx, id, inv)
	})
}

func (s *bound) Get(ctx context.Context, id string) (Record, error) {
	if !idRe.MatchString(id) {
		return Record{}, ErrNotFound
	}
	r, err := s.b.Get(ctx, s.twin, id)
	if err != nil {
		return Record{}, err
	}
	return checkRead(r)
}

func (s *bound) Current(ctx context.Context, now time.Time, q Query) ([]Record, error) {
	q.Text = ""
	q.IncludeHistory = false
	return s.query(ctx, now, q)
}

func (s *bound) Search(ctx context.Context, now time.Time, q Query) ([]Record, error) {
	return s.query(ctx, now, q)
}

func (s *bound) ListByType(ctx context.Context, t Type, now time.Time, includeHistory bool) ([]Record, error) {
	if !t.Valid() {
		return nil, invalid("unknown type")
	}
	return s.query(ctx, now, Query{Types: []Type{t}, IncludeHistory: includeHistory})
}

func (s *bound) query(ctx context.Context, now time.Time, q Query) ([]Record, error) {
	rs, err := s.b.List(ctx, s.twin, Filter{Types: q.Types, LiveOnly: !q.IncludeHistory})
	if err != nil {
		return nil, err
	}
	if _, err := checkReadAll(rs); err != nil {
		return nil, err
	}
	terms := strings.Fields(strings.ToLower(q.Text))
	out := rs[:0]
	for _, r := range rs {
		if !q.IncludeHistory && !IsCurrent(r, now) {
			continue
		}
		if matches(r, q, terms) {
			out = append(out, r)
		}
	}
	sortRecords(out)
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func matches(r Record, q Query, terms []string) bool {
	if len(q.Types) > 0 && !containsType(q.Types, r.Type) {
		return false
	}
	if len(q.Sensitivities) > 0 && !containsSensitivity(q.Sensitivities, r.Sensitivity) {
		return false
	}
	if len(q.Subjects) > 0 {
		hit := false
		for _, want := range q.Subjects {
			for _, have := range r.Subjects {
				if strings.EqualFold(want, have) {
					hit = true
				}
			}
		}
		if !hit {
			return false
		}
	}
	if len(terms) > 0 {
		hay := strings.ToLower(r.Statement + "\n" + strings.Join(r.Subjects, "\n"))
		for _, t := range terms {
			if !strings.Contains(hay, t) {
				return false
			}
		}
	}
	return true
}

func containsType(ts []Type, t Type) bool {
	for _, v := range ts {
		if v == t {
			return true
		}
	}
	return false
}

func containsSensitivity(ss []Sensitivity, s Sensitivity) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// sortRecords is the deterministic order: ObservedAt descending, then ID.
func sortRecords(rs []Record) {
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i].Time.ObservedAt, rs[j].Time.ObservedAt
		if !a.Equal(b) {
			return a.After(b)
		}
		return rs[i].ID < rs[j].ID
	})
}

func (s *bound) DueForReview(ctx context.Context, now time.Time) ([]Record, error) {
	rs, err := s.b.List(ctx, s.twin, Filter{LiveOnly: true})
	if err != nil {
		return nil, err
	}
	if _, err := checkReadAll(rs); err != nil {
		return nil, err
	}
	var out []Record
	for _, r := range rs {
		if IsCurrent(r, now) && r.Time.ReviewAfter != nil && !r.Time.ReviewAfter.After(now) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := *out[i].Time.ReviewAfter, *out[j].Time.ReviewAfter
		if !a.Equal(b) {
			return a.Before(b)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *bound) History(ctx context.Context, id string) ([]Record, error) {
	start, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{start.ID: true}
	chain := []Record{start}
	// Back to the oldest ancestor.
	for cur := start; cur.Supersedes != ""; {
		if seen[cur.Supersedes] {
			return nil, fmt.Errorf("memory: supersedes chain of %s loops", id)
		}
		prev, err := s.Get(ctx, cur.Supersedes)
		if err != nil {
			return nil, fmt.Errorf("memory: supersedes chain of %s is broken: %w", id, err)
		}
		seen[prev.ID] = true
		chain = append([]Record{prev}, chain...)
		cur = prev
	}
	// Forward to the newest replacement.
	for cur := start; cur.Invalidation != nil && cur.Invalidation.SupersededBy != ""; {
		nextID := cur.Invalidation.SupersededBy
		if seen[nextID] {
			return nil, fmt.Errorf("memory: supersedes chain of %s loops", id)
		}
		next, err := s.Get(ctx, nextID)
		if err != nil {
			return nil, fmt.Errorf("memory: supersedes chain of %s is broken: %w", id, err)
		}
		seen[next.ID] = true
		chain = append(chain, next)
		cur = next
	}
	return chain, nil
}
