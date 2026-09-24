package store

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "water.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func ts(h int) time.Time { return time.Date(2026, 9, 23, h, 0, 0, 0, time.UTC) }

func meta(id string, ext bool) Meta {
	return Meta{Source: "fake", SourceID: id, External: ext, CreatedAt: ts(8), UpdatedAt: ts(9)}
}

func roundTrip[T any, P interface {
	*T
	Record
}](t *testing.T, s *Store, rec *T) {
	t.Helper()
	ctx := context.Background()
	if err := s.Upsert(ctx, P(rec)); err != nil {
		t.Fatal(err)
	}
	m := P(rec).meta()
	got, err := Get[T, P](ctx, s, m.Source, m.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, rec) {
		t.Fatalf("%s round trip:\n got %+v\nwant %+v", P(rec).Table(), *got, *rec)
	}
}

func TestAllRecordTypesRoundTrip(t *testing.T) {
	s, path := openTemp(t)
	roundTrip(t, s, &Message{Meta: meta("m1", true), Channel: "email", Thread: "t1", From: "dana@x.com", To: []string{"ceo@x.com", "cfo@x.com"}, Subject: "Q3 budget", Body: "numbers", SentAt: ts(7)})
	roundTrip(t, s, &Meeting{Meta: meta("mt1", false), Title: "Board", StartAt: ts(10), EndAt: ts(11), Attendees: []string{"a", "b"}, Organizer: "ceo", TranscriptRef: "tr1", Summary: "ok"})
	roundTrip(t, s, &Event{Meta: meta("e1", false), Title: "1:1", StartAt: ts(12), EndAt: ts(13), Location: "Zoom", Organizer: "ceo", Status: "confirmed"})
	roundTrip(t, s, &Document{Meta: meta("d1", true), Title: "Plan", URL: "https://d/1", MimeType: "text/plain", Owner: "cto", Excerpt: "…", ModifiedAt: ts(6)})
	roundTrip(t, s, &Issue{Meta: meta("i1", true), Title: "Bug", State: "open", Assignee: "sam", Project: "core", Priority: "p1", URL: "https://i/1"})
	roundTrip(t, s, &Commit{Meta: meta("c1", true), Repo: "water", SHA: "abc123", Author: "sam", Message: "fix", CommittedAt: ts(5), URL: "https://c/1"})
	roundTrip(t, s, &Transaction{Meta: meta("x1", false), Account: "ops", AmountMinor: -125050, Currency: "USD", Counterparty: "AWS", Description: "cloud", PostedAt: ts(4)})
	roundTrip(t, s, &Contact{Meta: meta("p1", false), Name: "Dana Lee", Email: "dana@x.com", Phone: "+1", Org: "Acme", Title: "CFO"})

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("db mode %v, want 0600", info.Mode().Perm())
	}
}

func TestUpsertIsIdempotent(t *testing.T) {
	s, path := openTemp(t)
	ctx := context.Background()
	m := &Message{Meta: meta("m1", true), Subject: "v1"}
	for i := 0; i < 3; i++ {
		if err := s.Upsert(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	m.Subject = "v2"
	if err := s.Upsert(ctx, m); err != nil {
		t.Fatal(err)
	}
	all, err := List[Message](ctx, s, Query{Source: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Subject != "v2" {
		t.Fatalf("after repeated upserts: %+v", all)
	}
	other := &Message{Meta: Meta{Source: "other", SourceID: "m1"}}
	if err := s.Upsert(ctx, other); err != nil {
		t.Fatal(err)
	}
	if all, _ := List[Message](ctx, s, Query{}); len(all) != 2 {
		t.Fatalf("same source_id under another source must be distinct: %d", len(all))
	}

	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen (migrations must be idempotent): %v", err)
	}
	defer s2.Close()
	if all, _ := List[Message](ctx, s2, Query{}); len(all) != 2 {
		t.Fatalf("after reopen: %d", len(all))
	}
}

// TestEventsInRangeIgnoresCreatedAt is the A3 fast-path fix: an event
// ingested long ago (an old created_at, from a stale background sync run)
// that starts today must still be found. Filtering by created_at recency (as
// the old "500 newest, then filter in Go" approach did) would have missed
// it once enough other rows existed.
func TestEventsInRangeIgnoresCreatedAt(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	today := ts(10)
	old := &Event{
		Meta:    Meta{Source: "gcal", SourceID: "primary:old", CreatedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
		Title:   "Board sync",
		StartAt: today,
		EndAt:   ts(11),
	}
	other := &Event{
		Meta:    Meta{Source: "gcal", SourceID: "primary:other"},
		Title:   "Next week",
		StartAt: today.Add(7 * 24 * time.Hour),
	}
	if err := s.Upsert(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, other); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	got, err := EventsInRange(ctx, s, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "Board sync" {
		t.Fatalf("EventsInRange = %+v, want just the old-created, today-starting event", got)
	}
}

func TestApprovalTransitionIsCompareAndSet(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	row := ApprovalRow{ID: "env1", Action: "fake_mail.send_email", Payload: `{"to":"a"}`, PayloadHash: "h", Origin: "p0", Status: "pending", CreatedAt: ts(1), ExpiresAt: ts(2), EvidenceRefs: []string{"fake:m1"}}
	if err := s.InsertApproval(ctx, row); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.TransitionApproval(ctx, "env1", "pending", "approved", "yes", ts(1)); !ok || err != nil {
		t.Fatalf("approve: %v %v", ok, err)
	}
	if ok, _ := s.TransitionApproval(ctx, "env1", "approved", "executed", "", ts(1)); !ok {
		t.Fatal("first execute claim failed")
	}
	if ok, _ := s.TransitionApproval(ctx, "env1", "approved", "executed", "", ts(1)); ok {
		t.Fatal("second execute claim succeeded")
	}
	got, err := s.GetApproval(ctx, "env1")
	if err != nil || got.Status != "executed" || got.ExecutedAt.IsZero() || got.EvidenceRefs[0] != "fake:m1" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE approvals SET status = 'bogus'`); err == nil {
		t.Fatal("status check constraint missing")
	}
}
