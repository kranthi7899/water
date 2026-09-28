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

// TestApprovalPhase1bMetadataRoundTrips: the Phase 1b columns (migration
// 0018_approval_trail.sql) round-trip through InsertApproval/GetApproval,
// with the nullable ones (deadline) reading back as a zero time.Time when
// never set, the same convention decided_at/executed_at already use.
func TestApprovalPhase1bMetadataRoundTrips(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	deadline := ts(20)
	row := ApprovalRow{
		ID: "env2", Action: "gmail.send_message", Payload: `{"to":"a"}`, PayloadHash: "h2", Origin: "p0", Status: "pending",
		CreatedAt: ts(1), ExpiresAt: ts(2),
		OriginKind: "person_request", RequestedBy: "lee", Kind: "money", SourceCardID: "card_1", Priority: "urgent",
		Deadline: deadline, Provenance: "demo_seed",
	}
	if err := s.InsertApproval(ctx, row); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetApproval(ctx, "env2")
	if err != nil {
		t.Fatal(err)
	}
	if got.OriginKind != "person_request" || got.RequestedBy != "lee" || got.Kind != "money" ||
		got.SourceCardID != "card_1" || got.Priority != "urgent" || got.Provenance != "demo_seed" {
		t.Fatalf("metadata did not round-trip: %+v", got)
	}
	if !got.Deadline.Equal(deadline) {
		t.Fatalf("deadline = %v, want %v", got.Deadline, deadline)
	}
	if got.ThreadRef != "" || !got.SentAt.IsZero() || !got.RepliedAt.IsZero() || got.ReplyRef != "" {
		t.Fatalf("trail fields not at their defaults: %+v", got)
	}

	// A bare ApprovalRow with no Phase 1b fields set inserts OriginKind's Go
	// zero value literally ("") -- InsertApproval always lists the column
	// explicitly, so SQLite's own column-level DEFAULT ('agent_draft') never
	// applies to a fresh INSERT; it only backfills rows that existed before
	// migration 0018 ran. Defaulting OriginKind to "agent_draft" for a new
	// envelope is approvals.Queue.Propose's job (see
	// TestApprovalMetadataRoundTrips in package approvals), one layer up
	// from here.
	bare := ApprovalRow{ID: "env3", Action: "notes.save_note", Payload: `{}`, PayloadHash: "h3", Origin: "p0", Status: "pending", CreatedAt: ts(1), ExpiresAt: ts(2)}
	if err := s.InsertApproval(ctx, bare); err != nil {
		t.Fatal(err)
	}
	gotBare, err := s.GetApproval(ctx, "env3")
	if err != nil {
		t.Fatal(err)
	}
	if gotBare.OriginKind != "" {
		t.Fatalf("bare row origin_kind = %q, want the Go zero value (\"\"), unmapped through the SQL default", gotBare.OriginKind)
	}
	if !gotBare.Deadline.IsZero() {
		t.Fatalf("bare row deadline = %v, want zero", gotBare.Deadline)
	}
}

// TestMarkApprovalSentAndReplied: MarkApprovalSent sets sent_at and, when
// given one, thread_ref; MarkApprovalReplied then finds that approval by
// thread_ref and sets replied_at/reply_ref. A thread_ref that matches no
// approval is a no-op, not an error.
func TestMarkApprovalSentAndReplied(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	row := ApprovalRow{ID: "env4", Action: "gmail.send_message", Payload: `{}`, PayloadHash: "h4", Origin: "p0", Status: "approved", CreatedAt: ts(1), ExpiresAt: ts(2)}
	if err := s.InsertApproval(ctx, row); err != nil {
		t.Fatal(err)
	}
	sentAt := ts(3)
	if err := s.MarkApprovalSent(ctx, "env4", "thread_abc", sentAt); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetApproval(ctx, "env4")
	if err != nil {
		t.Fatal(err)
	}
	if got.ThreadRef != "thread_abc" || !got.SentAt.Equal(sentAt) {
		t.Fatalf("after MarkApprovalSent: %+v", got)
	}
	if !got.RepliedAt.IsZero() || got.ReplyRef != "" {
		t.Fatalf("MarkApprovalSent set reply fields: %+v", got)
	}

	// A reply on an unrelated thread touches nothing.
	if err := s.MarkApprovalReplied(ctx, "thread_unrelated", "msg_x", ts(4)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetApproval(ctx, "env4"); !got.RepliedAt.IsZero() {
		t.Fatalf("an unrelated thread's reply matched: %+v", got)
	}

	repliedAt := ts(5)
	if err := s.MarkApprovalReplied(ctx, "thread_abc", "msg_1", repliedAt); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetApproval(ctx, "env4")
	if err != nil {
		t.Fatal(err)
	}
	if !got.RepliedAt.Equal(repliedAt) || got.ReplyRef != "msg_1" {
		t.Fatalf("after MarkApprovalReplied: %+v", got)
	}

	// An empty thread_ref (never sent, or a non-Gmail action) matches
	// nothing -- it must never be treated as "no filter".
	if err := s.MarkApprovalReplied(ctx, "", "msg_2", ts(6)); err != nil {
		t.Fatal(err)
	}
}
