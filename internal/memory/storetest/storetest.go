// Package storetest is the contract suite every memory Backend runs. The
// chosen backend (SQLite) runs it from internal/memory's tests; a second
// backend added later must pass it unchanged.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"water/internal/memory"
)

// Harness is what a backend supplies to the suite.
type Harness struct {
	// New returns a fresh, empty Backend.
	New func(t *testing.T) memory.Backend
	// NewShared returns two Backends over the same underlying storage, as
	// two processes would open it.
	NewShared func(t *testing.T) (memory.Backend, memory.Backend)
	// InjectSupersedeFault installs f to run between Supersede's two
	// halves and returns a function that removes it.
	InjectSupersedeFault func(f func() error) (restore func())
	// CorruptStatement overwrites a stored record's statement the way a
	// hand edit of the backing storage would, bypassing every check.
	CorruptStatement func(t *testing.T, b memory.Backend, id, statement string)
}

// T0 is the suite's reference time.
var T0 = time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)

func tp(t time.Time) *time.Time { return &t }

// Fixture is a well-formed ceo_statement record of type typ.
func Fixture(typ memory.Type, statement string) memory.Record {
	return memory.Record{
		Type:        typ,
		Statement:   statement,
		Sensitivity: memory.Normal,
		Provenance: memory.Provenance{
			Trigger: memory.CEOStatement, SourceRef: "audit:1842", AuditSeq: 1842, WrittenBy: memory.AuthorCEO,
		},
		Time:       memory.TimeBounds{ObservedAt: T0},
		Confidence: 1,
	}
}

func inv(reason string) memory.Invalidation {
	return memory.Invalidation{At: T0.Add(time.Hour), By: "ceo", Reason: reason, AuditSeq: 1900}
}

func bind(t *testing.T, b memory.Backend, twin string, l memory.Limits) memory.Store {
	t.Helper()
	s, err := memory.Bind(b, twin, l)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustWrite(t *testing.T, s memory.Store, r memory.Record) memory.Record {
	t.Helper()
	w, err := s.Write(context.Background(), r)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	return w
}

func ids(rs []memory.Record) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

// Run runs the whole contract suite against h.
func Run(t *testing.T, h Harness) {
	ctx := context.Background()
	fresh := func(t *testing.T) memory.Store { return bind(t, h.New(t), "ceo", memory.Limits{}) }

	t.Run("RoundTripEveryType", func(t *testing.T) {
		s := fresh(t)
		for i, typ := range memory.Types {
			r := Fixture(typ, fmt.Sprintf("fact %d about %s", i, typ))
			r.Sensitivity = memory.Sensitivities[i%len(memory.Sensitivities)]
			r.Subjects = []string{"person:p-" + fmt.Sprint(i), "project:crane"}
			r.Provenance = memory.Provenance{Trigger: memory.ApprovedProposal, SourceRef: "gmail:msg-abc123",
				AuditSeq: int64(100 + i), WrittenBy: memory.AuthorTwin, ApprovedBy: "ceo"}
			r.Time = memory.TimeBounds{ObservedAt: T0.Add(time.Duration(i) * time.Minute).In(time.FixedZone("X", 3600)),
				ValidFrom: T0.Add(time.Duration(i) * time.Minute), ValidUntil: tp(T0.AddDate(1, 0, 0)), ReviewAfter: tp(T0.AddDate(0, 1, 0))}
			r.Confidence = 0.25
			w := mustWrite(t, s, r)
			if !strings.HasPrefix(w.ID, "mem_") {
				t.Fatalf("id %q", w.ID)
			}
			got, err := s.Get(ctx, w.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, w) {
				t.Fatalf("%s did not round-trip:\n got  %+v\n want %+v", typ, got, w)
			}
			if got.Statement != r.Statement || !got.Time.ObservedAt.Equal(r.Time.ObservedAt) || got.Provenance != r.Provenance {
				t.Fatalf("%s: stored record differs from the input", typ)
			}
		}
		// ValidFrom defaults to ObservedAt; empty Subjects come back nil.
		w := mustWrite(t, s, Fixture(memory.Preference, "defaults"))
		got, _ := s.Get(ctx, w.ID)
		if !got.Time.ValidFrom.Equal(T0) || got.Subjects != nil || !reflect.DeepEqual(got, w) {
			t.Fatalf("defaults did not round-trip: %+v", got)
		}
	})

	t.Run("ValidationRejects", func(t *testing.T) {
		s := fresh(t)
		cases := map[string]func(r *memory.Record){
			"unknown type":             func(r *memory.Record) { r.Type = "gossip" },
			"unknown sensitivity":      func(r *memory.Record) { r.Sensitivity = "secret" },
			"empty sensitivity":        func(r *memory.Record) { r.Sensitivity = "" },
			"empty statement":          func(r *memory.Record) { r.Statement = "  " },
			"empty source ref":         func(r *memory.Record) { r.Provenance.SourceRef = "" },
			"source ref not a ref":     func(r *memory.Record) { r.Provenance.SourceRef = "the email from Dana" },
			"source ref too long":      func(r *memory.Record) { r.Provenance.SourceRef = "gmail:" + strings.Repeat("a", 300) },
			"zero audit seq":           func(r *memory.Record) { r.Provenance.AuditSeq = 0 },
			"negative audit seq":       func(r *memory.Record) { r.Provenance.AuditSeq = -4 },
			"empty written by":         func(r *memory.Record) { r.Provenance.WrittenBy = "" },
			"unknown written by":       func(r *memory.Record) { r.Provenance.WrittenBy = "someone" },
			"unknown trigger":          func(r *memory.Record) { r.Provenance.Trigger = "overheard" },
			"correction no approver":   func(r *memory.Record) { r.Provenance.Trigger = memory.ApprovedCorrection },
			"proposal no approver":     func(r *memory.Record) { r.Provenance.Trigger = memory.ApprovedProposal },
			"zero observed at":         func(r *memory.Record) { r.Time.ObservedAt = time.Time{} },
			"valid until == from":      func(r *memory.Record) { r.Time.ValidUntil = tp(T0) },
			"valid until before from":  func(r *memory.Record) { r.Time.ValidUntil = tp(T0.Add(-time.Hour)) },
			"review before valid from": func(r *memory.Record) { r.Time.ReviewAfter = tp(T0.Add(-time.Second)) },
			"confidence above 1":       func(r *memory.Record) { r.Confidence = 1.01 },
			"confidence below 0":       func(r *memory.Record) { r.Confidence = -0.1 },
			"subject not a ref":        func(r *memory.Record) { r.Subjects = []string{"Dana Ruiz"} },
			"caller-chosen id":         func(r *memory.Record) { r.ID = "mem_000000000000000000000000" },
			"pre-invalidated":          func(r *memory.Record) { i := inv("x"); r.Invalidation = &i },
			"supersedes via Write":     func(r *memory.Record) { r.Supersedes = "mem_000000000000000000000000" },
		}
		for name, mut := range cases {
			r := Fixture(memory.Preference, "board updates on the first Monday")
			mut(&r)
			if _, err := s.Write(ctx, r); !errors.Is(err, memory.ErrInvalidRecord) {
				t.Errorf("%s: want ErrInvalidRecord, got %v", name, err)
			}
		}
		if rs, _ := s.Search(ctx, T0, memory.Query{IncludeHistory: true}); len(rs) != 0 {
			t.Fatalf("rejected writes stored %d records", len(rs))
		}
		// Supersedes naming a record that does not exist, or a dead one.
		if _, err := s.Supersede(ctx, "mem_000000000000000000000000", Fixture(memory.Preference, "x"), inv("r")); !errors.Is(err, memory.ErrNotFound) {
			t.Errorf("supersede unknown id: %v", err)
		}
		old := mustWrite(t, s, Fixture(memory.Preference, "old"))
		if err := s.Invalidate(ctx, old.ID, inv("no longer true")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Supersede(ctx, old.ID, Fixture(memory.Preference, "new"), inv("r")); !errors.Is(err, memory.ErrAlreadyInvalidated) {
			t.Errorf("supersede invalidated record: %v", err)
		}
		// An invalidation needs its own fields.
		live := mustWrite(t, s, Fixture(memory.Preference, "live"))
		for name, i := range map[string]memory.Invalidation{
			"no at":     {By: "ceo", Reason: "r", AuditSeq: 1},
			"no by":     {At: T0, Reason: "r", AuditSeq: 1},
			"no reason": {At: T0, By: "ceo", AuditSeq: 1},
			"no seq":    {At: T0, By: "ceo", Reason: "r"},
			"set superseded_by": {At: T0, By: "ceo", Reason: "r", AuditSeq: 1,
				SupersededBy: "mem_000000000000000000000000"},
		} {
			if err := s.Invalidate(ctx, live.ID, i); !errors.Is(err, memory.ErrInvalidRecord) {
				t.Errorf("invalidation %s: %v", name, err)
			}
		}
	})

	t.Run("Principle6", func(t *testing.T) {
		s := fresh(t)
		twin := Fixture(memory.Preference, "prefers async standups")
		twin.Provenance.WrittenBy = memory.AuthorTwin
		for _, trig := range []memory.Trigger{memory.CEOStatement, memory.ApprovedCorrection, memory.ApprovedProposal} {
			twin.Provenance.Trigger = trig
			if _, err := s.Write(ctx, twin); !errors.Is(err, memory.ErrInvalidRecord) {
				t.Errorf("twin-written %s with no approver: want rejection, got %v", trig, err)
			}
		}
		twin.Provenance.ApprovedBy = "ceo"
		twin.Provenance.Trigger = memory.ApprovedProposal
		mustWrite(t, s, twin)
		mustWrite(t, s, Fixture(memory.Preference, "the CEO's own words, no approver needed"))
	})

	t.Run("InvalidateNeverOverwrite", func(t *testing.T) {
		s := fresh(t)
		old := mustWrite(t, s, Fixture(memory.Preference, "board updates on the first Monday"))
		nextIn := Fixture(memory.Preference, "board updates on the first Tuesday")
		nextIn.Time.ObservedAt = T0.Add(2 * time.Hour)
		next, err := s.Supersede(ctx, old.ID, nextIn, inv("CEO corrected the day"))
		if err != nil {
			t.Fatal(err)
		}
		if next.Supersedes != old.ID {
			t.Fatalf("next.Supersedes = %q", next.Supersedes)
		}
		gotOld, err := s.Get(ctx, old.ID)
		if err != nil {
			t.Fatal(err)
		}
		if gotOld.Statement != old.Statement || gotOld.Invalidation == nil || gotOld.Invalidation.SupersededBy != next.ID {
			t.Fatalf("old after supersede: %+v", gotOld)
		}
		gotOld.Invalidation = nil
		if !reflect.DeepEqual(gotOld, old) {
			t.Fatalf("old record's content changed:\n got  %+v\n want %+v", gotOld, old)
		}
		cur, err := s.Current(ctx, T0.Add(3*time.Hour), memory.Query{})
		if err != nil || !reflect.DeepEqual(ids(cur), []string{next.ID}) {
			t.Fatalf("current = %v %v, want only next", ids(cur), err)
		}
		for _, id := range []string{old.ID, next.ID} {
			h, err := s.History(ctx, id)
			if err != nil || !reflect.DeepEqual(ids(h), []string{old.ID, next.ID}) {
				t.Fatalf("history(%s) = %v %v", id, ids(h), err)
			}
		}
		// A second correction extends the chain.
		third, err := s.Supersede(ctx, next.ID, Fixture(memory.Preference, "board updates on the first Wednesday"), inv("again"))
		if err != nil {
			t.Fatal(err)
		}
		if h, _ := s.History(ctx, next.ID); !reflect.DeepEqual(ids(h), []string{old.ID, next.ID, third.ID}) {
			t.Fatalf("three-link history = %v", ids(h))
		}
		// Superseding or invalidating an already-invalidated record errors.
		if _, err := s.Supersede(ctx, old.ID, Fixture(memory.Preference, "x"), inv("r")); !errors.Is(err, memory.ErrAlreadyInvalidated) {
			t.Fatalf("re-supersede: %v", err)
		}
		if err := s.Invalidate(ctx, next.ID, inv("r")); !errors.Is(err, memory.ErrAlreadyInvalidated) {
			t.Fatalf("invalidate superseded: %v", err)
		}
		if err := s.Invalidate(ctx, third.ID, inv("no longer true")); err != nil {
			t.Fatal(err)
		}
		if err := s.Invalidate(ctx, third.ID, inv("again")); !errors.Is(err, memory.ErrAlreadyInvalidated) {
			t.Fatalf("double invalidate: %v", err)
		}
		if g, _ := s.Get(ctx, third.ID); g.Invalidation == nil || g.Invalidation.Reason != "no longer true" || g.Invalidation.SupersededBy != "" {
			t.Fatalf("invalidation was overwritten: %+v", g.Invalidation)
		}
		if err := s.Invalidate(ctx, "mem_000000000000000000000000", inv("r")); !errors.Is(err, memory.ErrNotFound) {
			t.Fatalf("invalidate unknown: %v", err)
		}
	})

	t.Run("SupersedeIsAtomic", func(t *testing.T) {
		s := fresh(t)
		old := mustWrite(t, s, Fixture(memory.Preference, "original"))
		boom := errors.New("simulated crash between the halves")
		called := false
		restore := h.InjectSupersedeFault(func() error { called = true; return boom })
		_, err := s.Supersede(ctx, old.ID, Fixture(memory.Preference, "replacement"), inv("r"))
		restore()
		if !called || !errors.Is(err, boom) {
			t.Fatalf("fault not hit: called=%v err=%v", called, err)
		}
		got, _ := s.Get(ctx, old.ID)
		if got.Invalidation != nil {
			t.Fatal("old record was invalidated although the supersede failed")
		}
		all, _ := s.Search(ctx, T0, memory.Query{IncludeHistory: true})
		if !reflect.DeepEqual(ids(all), []string{old.ID}) {
			t.Fatalf("records after failed supersede = %v, want only the original", ids(all))
		}
		// And with the fault gone, the same supersede succeeds.
		if _, err := s.Supersede(ctx, old.ID, Fixture(memory.Preference, "replacement"), inv("r")); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("CurrentAndTimeBounds", func(t *testing.T) {
		s := fresh(t)
		from, until := T0.Add(time.Hour), T0.Add(48*time.Hour)
		bounded := Fixture(memory.Commitment, "offsite runs through Friday")
		bounded.Time.ValidFrom = from
		bounded.Time.ValidUntil = &until
		b := mustWrite(t, s, bounded)
		open := mustWrite(t, s, Fixture(memory.Preference, "open-ended"))
		for _, c := range []struct {
			name string
			now  time.Time
			want []string
		}{
			{"before ValidFrom", from.Add(-time.Nanosecond), []string{open.ID}},
			{"exactly ValidFrom", from, []string{b.ID, open.ID}},
			{"inside", from.Add(time.Hour), []string{b.ID, open.ID}},
			{"just before ValidUntil", until.Add(-time.Nanosecond), []string{b.ID, open.ID}},
			{"exactly ValidUntil", until, []string{open.ID}},
			{"after ValidUntil", until.Add(time.Hour), []string{open.ID}},
		} {
			got, err := s.Current(ctx, c.now, memory.Query{})
			if err != nil {
				t.Fatal(err)
			}
			if !sameSet(ids(got), c.want) {
				t.Errorf("%s: current = %v, want %v", c.name, ids(got), c.want)
			}
		}
		// ReviewAfter never hides a record; it only lists it as due.
		rev := Fixture(memory.Entity, "vendor contract terms")
		rev.Time.ReviewAfter = tp(T0.Add(24 * time.Hour))
		r := mustWrite(t, s, rev)
		later := T0.Add(25 * time.Hour)
		cur, _ := s.Current(ctx, later, memory.Query{Types: []memory.Type{memory.Entity}})
		if !reflect.DeepEqual(ids(cur), []string{r.ID}) {
			t.Fatalf("record past ReviewAfter is not current: %v", ids(cur))
		}
		due, _ := s.DueForReview(ctx, later)
		if !reflect.DeepEqual(ids(due), []string{r.ID}) {
			t.Fatalf("due = %v", ids(due))
		}
		if due, _ := s.DueForReview(ctx, T0.Add(23*time.Hour)); len(due) != 0 {
			t.Fatalf("due before ReviewAfter = %v", ids(due))
		}
		if due, _ := s.DueForReview(ctx, T0.Add(24*time.Hour)); len(due) != 1 {
			t.Fatalf("due exactly at ReviewAfter = %v", ids(due))
		}
		_ = s.Invalidate(ctx, r.ID, inv("resolved"))
		if due, _ := s.DueForReview(ctx, later); len(due) != 0 {
			t.Fatalf("invalidated record still due: %v", ids(due))
		}
	})

	t.Run("SearchAndListByType", func(t *testing.T) {
		s := fresh(t)
		mk := func(typ memory.Type, stmt string, sens memory.Sensitivity, minutes int, subjects ...string) memory.Record {
			r := Fixture(typ, stmt)
			r.Sensitivity = sens
			r.Subjects = subjects
			r.Time.ObservedAt = T0.Add(time.Duration(minutes) * time.Minute)
			return mustWrite(t, s, r)
		}
		a := mk(memory.Preference, "Board updates go out on the first Monday", memory.Normal, 1, "project:board")
		b := mk(memory.Preference, "Prefers board decks as PDF", memory.Sensitive, 3)
		c := mk(memory.Entity, "Dana leads the Crane project", memory.Normal, 2, "person:dana", "project:crane")
		d := mk(memory.Decision, "Board approved the Q4 hiring plan", memory.Restricted, 3)
		now := T0.Add(time.Hour)

		q := func(qq memory.Query) []string {
			rs, err := s.Search(ctx, now, qq)
			if err != nil {
				t.Fatal(err)
			}
			return ids(rs)
		}
		// Order: ObservedAt descending, then ID (b and d tie at minute 3).
		tie := []string{b.ID, d.ID}
		if d.ID < b.ID {
			tie = []string{d.ID, b.ID}
		}
		all := append(append([]string{}, tie...), c.ID, a.ID)
		if got := q(memory.Query{}); !reflect.DeepEqual(got, all) {
			t.Fatalf("order = %v, want %v", got, all)
		}
		for i := 0; i < 5; i++ { // deterministic across calls
			if got := q(memory.Query{Text: "board"}); !reflect.DeepEqual(got, append(append([]string{}, tie...), a.ID)) {
				t.Fatalf("text search = %v", got)
			}
		}
		if got := q(memory.Query{Text: "BOARD monday"}); !reflect.DeepEqual(got, []string{a.ID}) {
			t.Fatalf("case-insensitive multi-term = %v", got)
		}
		if got := q(memory.Query{Text: "crane"}); !reflect.DeepEqual(got, []string{c.ID}) {
			t.Fatalf("subject/statement term = %v", got)
		}
		if got := q(memory.Query{Text: "dana"}); !reflect.DeepEqual(got, []string{c.ID}) {
			t.Fatalf("term in subject = %v", got)
		}
		if got := q(memory.Query{Types: []memory.Type{memory.Preference}}); !sameSet(got, []string{a.ID, b.ID}) {
			t.Fatalf("type filter = %v", got)
		}
		if got := q(memory.Query{Subjects: []string{"PROJECT:crane"}}); !reflect.DeepEqual(got, []string{c.ID}) {
			t.Fatalf("subject filter = %v", got)
		}
		if got := q(memory.Query{Sensitivities: []memory.Sensitivity{memory.Sensitive, memory.Restricted}}); !sameSet(got, []string{b.ID, d.ID}) {
			t.Fatalf("sensitivity filter = %v", got)
		}
		if got := q(memory.Query{Limit: 2}); !reflect.DeepEqual(got, all[:2]) {
			t.Fatalf("limit = %v", got)
		}
		// IncludeHistory toggles invalidated records.
		if err := s.Invalidate(ctx, a.ID, inv("no longer true")); err != nil {
			t.Fatal(err)
		}
		if got := q(memory.Query{Text: "monday"}); len(got) != 0 {
			t.Fatalf("invalidated record in search = %v", got)
		}
		if got := q(memory.Query{Text: "monday", IncludeHistory: true}); !reflect.DeepEqual(got, []string{a.ID}) {
			t.Fatalf("history search = %v", got)
		}
		// Current applies the same filters and ignores Text.
		cur, _ := s.Current(ctx, now, memory.Query{Text: "nothing matches this", Types: []memory.Type{memory.Preference}})
		if !reflect.DeepEqual(ids(cur), []string{b.ID}) {
			t.Fatalf("current with filters = %v", ids(cur))
		}
		lt, _ := s.ListByType(ctx, memory.Preference, now, false)
		if !reflect.DeepEqual(ids(lt), []string{b.ID}) {
			t.Fatalf("list by type = %v", ids(lt))
		}
		lt, _ = s.ListByType(ctx, memory.Preference, now, true)
		if !reflect.DeepEqual(ids(lt), []string{b.ID, a.ID}) {
			t.Fatalf("list by type with history = %v", ids(lt))
		}
		if _, err := s.ListByType(ctx, "gossip", now, false); !errors.Is(err, memory.ErrInvalidRecord) {
			t.Fatalf("list unknown type: %v", err)
		}
	})

	t.Run("NeverStoreAtWrite", func(t *testing.T) {
		s := fresh(t)
		pem := "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA\n-----END RSA PRIVATE KEY-----"
		r := Fixture(memory.Preference, pem)
		_, err := s.Write(ctx, r)
		var ns memory.ErrNeverStore
		if !errors.As(err, &ns) || ns.Category != memory.CatCredential || ns.Field != "statement" {
			t.Fatalf("write PEM: %v", err)
		}
		live := mustWrite(t, s, Fixture(memory.Preference, "live"))
		_, err = s.Supersede(ctx, live.ID, Fixture(memory.Preference, "card 4111 1111 1111 1111"), inv("r"))
		if !errors.As(err, &ns) || ns.Category != memory.CatPayment {
			t.Fatalf("supersede with card: %v", err)
		}
		bad := inv("SSN is 123-45-6789")
		if err := s.Invalidate(ctx, live.ID, bad); !errors.As(err, &ns) || ns.Category != memory.CatGovernmentID || ns.Field != "invalidation.reason" {
			t.Fatalf("invalidate with SSN reason: %v", err)
		}
		all, _ := s.Search(ctx, T0, memory.Query{IncludeHistory: true})
		if !reflect.DeepEqual(ids(all), []string{live.ID}) {
			t.Fatalf("refused writes left records behind: %v", ids(all))
		}
		if g, _ := s.Get(ctx, live.ID); g.Invalidation != nil {
			t.Fatal("refused invalidation was applied")
		}
	})

	t.Run("NeverStoreOnRead", func(t *testing.T) {
		b := h.New(t)
		s := bind(t, b, "ceo", memory.Limits{})
		w := mustWrite(t, s, Fixture(memory.Preference, "a clean fact"))
		secret := "token ya29.a0AfH6SMBx3kq9ZtLrQpWnE7vYuT2mCd"
		h.CorruptStatement(t, b, w.ID, secret)
		_, err := s.Get(ctx, w.ID)
		var ns memory.ErrNeverStore
		if !errors.As(err, &ns) || ns.Category != memory.CatCredential {
			t.Fatalf("get of a hand-edited record: %v", err)
		}
		if strings.Contains(err.Error(), "ya29") {
			t.Fatalf("load error echoes the forbidden text: %v", err)
		}
		for name, f := range map[string]func() error{
			"current": func() error { _, err := s.Current(ctx, T0, memory.Query{}); return err },
			"search":  func() error { _, err := s.Search(ctx, T0, memory.Query{IncludeHistory: true}); return err },
			"type":    func() error { _, err := s.ListByType(ctx, memory.Preference, T0, true); return err },
			"history": func() error { _, err := s.History(ctx, w.ID); return err },
			"supersede": func() error {
				_, err := s.Supersede(ctx, w.ID, Fixture(memory.Preference, "x"), inv("r"))
				return err
			},
		} {
			if err := f(); !errors.As(err, &ns) {
				t.Errorf("%s over a hand-edited record: %v", name, err)
			}
		}
	})

	t.Run("TwinIsolation", func(t *testing.T) {
		b := h.New(t)
		ceo := bind(t, b, "ceo", memory.Limits{})
		demo := bind(t, b, "ceo-demo", memory.Limits{})
		w := mustWrite(t, ceo, Fixture(memory.Preference, "the real CEO's preference"))
		if _, err := demo.Get(ctx, w.ID); !errors.Is(err, memory.ErrNotFound) {
			t.Fatalf("demo got the CEO's record: %v", err)
		}
		for _, f := range []func() ([]memory.Record, error){
			func() ([]memory.Record, error) { return demo.Current(ctx, T0, memory.Query{}) },
			func() ([]memory.Record, error) { return demo.Search(ctx, T0, memory.Query{IncludeHistory: true}) },
			func() ([]memory.Record, error) { return demo.ListByType(ctx, memory.Preference, T0, true) },
		} {
			if rs, err := f(); err != nil || len(rs) != 0 {
				t.Fatalf("demo saw %d CEO records (%v)", len(rs), err)
			}
		}
		if _, err := demo.History(ctx, w.ID); !errors.Is(err, memory.ErrNotFound) {
			t.Fatalf("demo history: %v", err)
		}
		if err := demo.Invalidate(ctx, w.ID, inv("r")); !errors.Is(err, memory.ErrNotFound) {
			t.Fatalf("demo invalidated the CEO's record: %v", err)
		}
		if _, err := demo.Supersede(ctx, w.ID, Fixture(memory.Preference, "x"), inv("r")); !errors.Is(err, memory.ErrNotFound) {
			t.Fatalf("demo superseded the CEO's record: %v", err)
		}
		if g, err := ceo.Get(ctx, w.ID); err != nil || g.Invalidation != nil {
			t.Fatalf("CEO record after demo attempts: %+v %v", g, err)
		}
	})

	t.Run("Bounds", func(t *testing.T) {
		s := bind(t, h.New(t), "ceo", memory.Limits{MaxEntries: 2, MaxBytes: 1 << 20})
		a := mustWrite(t, s, Fixture(memory.Preference, "one"))
		mustWrite(t, s, Fixture(memory.Preference, "two"))
		_, err := s.Write(ctx, Fixture(memory.Preference, "three"))
		if !errors.Is(err, memory.ErrBoundsExceeded) {
			t.Fatalf("third write: %v", err)
		}
		if strings.Contains(err.Error(), "prune") {
			t.Fatalf("bounds error names the deleted prune command: %v", err)
		}
		if all, _ := s.Search(ctx, T0, memory.Query{IncludeHistory: true}); len(all) != 2 {
			t.Fatalf("over-bounds write was stored or truncated: %d records", len(all))
		}
		// Supersede keeps the live count, so it fits.
		if _, err := s.Supersede(ctx, a.ID, Fixture(memory.Preference, "one, corrected"), inv("r")); err != nil {
			t.Fatalf("supersede at the bound: %v", err)
		}
		// Bounds count live records only: invalidating frees a slot even
		// though the history is kept.
		cur, _ := s.Current(ctx, T0, memory.Query{})
		if err := s.Invalidate(ctx, cur[0].ID, inv("r")); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, s, Fixture(memory.Preference, "three"))

		bs := bind(t, h.New(t), "ceo", memory.Limits{MaxBytes: 200})
		mustWrite(t, bs, Fixture(memory.Preference, strings.Repeat("a", 100)))
		if _, err := bs.Write(ctx, Fixture(memory.Preference, strings.Repeat("b ", 50))); !errors.Is(err, memory.ErrBoundsExceeded) {
			t.Fatalf("byte bound: %v", err)
		}
	})

	t.Run("ConcurrentWritersLoseNothing", func(t *testing.T) {
		b1, b2 := h.NewShared(t)
		l := memory.Limits{MaxEntries: 1000, MaxBytes: 1 << 20}
		stores := []memory.Store{bind(t, b1, "ceo", l), bind(t, b2, "ceo", l), bind(t, b1, "ceo", l)}
		const per = 30
		var wg sync.WaitGroup
		errs := make(chan error, len(stores)*per)
		for i, s := range stores {
			wg.Add(1)
			go func(i int, s memory.Store) {
				defer wg.Done()
				for j := 0; j < per; j++ {
					if _, err := s.Write(ctx, Fixture(memory.Preference, fmt.Sprintf("writer %d fact %d", i, j))); err != nil {
						errs <- err
					}
				}
			}(i, s)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("concurrent write: %v", err)
		}
		all, err := stores[1].Search(ctx, T0, memory.Query{IncludeHistory: true})
		if err != nil || len(all) != len(stores)*per {
			t.Fatalf("records after concurrent writers: %d (want %d) %v", len(all), len(stores)*per, err)
		}
	})
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}
