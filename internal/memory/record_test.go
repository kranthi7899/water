package memory

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestIsCurrent(t *testing.T) {
	t0 := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	from, until := t0.Add(time.Hour), t0.Add(2*time.Hour)
	bounded := Record{Time: TimeBounds{ObservedAt: t0, ValidFrom: from, ValidUntil: &until}}
	open := Record{Time: TimeBounds{ObservedAt: t0}} // ValidFrom defaults to ObservedAt
	review := t0.Add(time.Minute)
	reviewed := Record{Time: TimeBounds{ObservedAt: t0, ReviewAfter: &review}}
	dead := Record{Time: TimeBounds{ObservedAt: t0}, Invalidation: &Invalidation{At: t0}}
	for _, c := range []struct {
		name string
		r    Record
		now  time.Time
		want bool
	}{
		{"before ValidFrom", bounded, from.Add(-time.Nanosecond), false},
		{"at ValidFrom", bounded, from, true},
		{"inside", bounded, from.Add(time.Minute), true},
		{"just before ValidUntil", bounded, until.Add(-time.Nanosecond), true},
		{"at ValidUntil", bounded, until, false},
		{"after ValidUntil", bounded, until.Add(time.Hour), false},
		{"open-ended at ObservedAt", open, t0, true},
		{"open-ended before ObservedAt", open, t0.Add(-time.Second), false},
		{"open-ended far future", open, t0.AddDate(50, 0, 0), true},
		{"past ReviewAfter stays current", reviewed, t0.AddDate(1, 0, 0), true},
		{"invalidated", dead, t0.Add(time.Hour), false},
	} {
		if got := IsCurrent(c.r, c.now); got != c.want {
			t.Errorf("%s: IsCurrent = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestPrinciple6Validate is CONTEXT.md principle 6 at the Validate level:
// anything the twin wrote names an approver; the CEO's own statement
// needs none.
func TestPrinciple6Validate(t *testing.T) {
	r := cleanRecord("prefers async standups")
	if err := r.Validate(); err != nil {
		t.Fatalf("ceo_statement with no approver: %v", err)
	}
	r.Provenance.WrittenBy = AuthorTwin
	for _, trig := range []Trigger{CEOStatement, ApprovedCorrection, ApprovedProposal} {
		r.Provenance.Trigger = trig
		if err := r.Validate(); !errors.Is(err, ErrInvalidRecord) {
			t.Errorf("twin-written %s with empty ApprovedBy: %v", trig, err)
		}
	}
	r.Provenance.Trigger, r.Provenance.ApprovedBy = ApprovedProposal, "ceo"
	if err := r.Validate(); err != nil {
		t.Fatalf("approved proposal: %v", err)
	}
}

func TestValidateNeverEchoesContent(t *testing.T) {
	r := cleanRecord("x")
	r.Provenance.SourceRef = "the full email text goes here"
	err := r.Validate()
	if err == nil || strings.Contains(err.Error(), "full email") {
		t.Fatalf("validate error: %v", err)
	}
}

// TestStoreMethodSetHasNoTwinParameter pins the bound handle's surface.
// The compile-time check (var _ Store = (*bound)(nil) in store.go) proves
// Bind's result satisfies Store; this proves Store has exactly these
// methods with exactly these signatures, so a method taking a twin id
// cannot be added without failing here.
func TestStoreMethodSetHasNoTwinParameter(t *testing.T) {
	want := map[string]string{
		"Write":        "func(context.Context, memory.Record) (memory.Record, error)",
		"Supersede":    "func(context.Context, string, memory.Record, memory.Invalidation) (memory.Record, error)",
		"Invalidate":   "func(context.Context, string, memory.Invalidation) error",
		"Get":          "func(context.Context, string) (memory.Record, error)",
		"Current":      "func(context.Context, time.Time, memory.Query) ([]memory.Record, error)",
		"Search":       "func(context.Context, time.Time, memory.Query) ([]memory.Record, error)",
		"ListByType":   "func(context.Context, memory.Type, time.Time, bool) ([]memory.Record, error)",
		"DueForReview": "func(context.Context, time.Time) ([]memory.Record, error)",
		"History":      "func(context.Context, string) ([]memory.Record, error)",
	}
	typ := reflect.TypeOf((*Store)(nil)).Elem()
	got := map[string]string{}
	var names []string
	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		got[m.Name] = m.Type.String()
		names = append(names, m.Name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Store's method set changed: %v\n%v", names, got)
	}
	// Bind's result exposes nothing beyond Store.
	var b Backend = nopBackend{}
	s, err := Bind(b, "ceo", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(interface{ Twin() string }); ok {
		t.Fatal("bound handle exposes its twin id")
	}
	if _, err := Bind(b, "", Limits{}); err == nil {
		t.Fatal("empty twin id accepted")
	}
	if _, err := Bind(b, "../ceo", Limits{}); err == nil {
		t.Fatal("malformed twin id accepted")
	}
}

type nopBackend struct{}

func (nopBackend) Update(_ context.Context, _ string, _ func(Tx) error) error { return nil }
func (nopBackend) Get(context.Context, string, string) (Record, error)        { return Record{}, ErrNotFound }
func (nopBackend) List(context.Context, string, Filter) ([]Record, error)     { return nil, nil }
