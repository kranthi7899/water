package store

import (
	"context"
	"testing"
)

func TestLatestMessagesOrder(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, &Message{Meta: meta("m1", true), From: "alex@x.com", Subject: "first", SentAt: ts(8)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, &Message{Meta: meta("m2", true), From: "alex@x.com", Subject: "third", SentAt: ts(12)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, &Message{Meta: meta("m3", true), From: "dana@x.com", Subject: "second", SentAt: ts(10)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.LatestMessages(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Subject != "third" || got[1].Subject != "second" || got[2].Subject != "first" {
		var subjects []string
		for _, m := range got {
			subjects = append(subjects, m.Subject)
		}
		t.Fatalf("LatestMessages order = %v, want [third second first]", subjects)
	}
	limited, err := s.LatestMessages(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 {
		t.Fatalf("limit=2 returned %d", len(limited))
	}
}

func TestMessagesFromMatchesSender(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, &Message{Meta: meta("m1", true), From: "Alex Chen <alex@x.com>", Subject: "a1", SentAt: ts(8)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, &Message{Meta: meta("m2", true), From: "Alex Chen <alex@x.com>", Subject: "a2", SentAt: ts(9)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, &Message{Meta: meta("m3", true), From: "Dana Lee <dana@x.com>", Subject: "d1", SentAt: ts(10)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.MessagesFrom(ctx, "alex@x.com", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Subject != "a2" || got[1].Subject != "a1" {
		t.Fatalf("MessagesFrom = %+v", got)
	}
}

// TestMessagesFromDoesNotMatchASenderThatContainsItAsASubstring is a
// regression test: MessagesFrom used an unanchored "%email%" LIKE pattern,
// so searching for jo@x.com could return mjo@x.com's messages too (the
// wrong sender's row), since "jo@x.com" is a literal substring of
// "mjo@x.com". Also covers a bare-address sender (no "Name <email>" form)
// and an underscore in the local part, which LIKE would otherwise treat as
// its own single-character wildcard.
func TestMessagesFromDoesNotMatchASenderThatContainsItAsASubstring(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, &Message{Meta: meta("m1", true), From: "Jo Lee <jo@x.com>", Subject: "wanted", SentAt: ts(8)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, &Message{Meta: meta("m2", true), From: "Mjo Park <mjo@x.com>", Subject: "wrong sender", SentAt: ts(9)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, &Message{Meta: meta("m3", true), From: "bare@x.com", Subject: "bare form", SentAt: ts(10)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, &Message{Meta: meta("m4", true), From: "under_score@x.com", Subject: "underscore", SentAt: ts(11)}); err != nil {
		t.Fatal(err)
	}

	got, err := s.MessagesFrom(ctx, "jo@x.com", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Subject != "wanted" {
		t.Fatalf("MessagesFrom(jo@x.com) = %+v, want exactly the message from jo@x.com, not mjo@x.com", got)
	}

	got, err = s.MessagesFrom(ctx, "bare@x.com", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Subject != "bare form" {
		t.Fatalf("MessagesFrom(bare@x.com) = %+v, want the bare-address sender", got)
	}

	// "under.score@x.com" must not match "under_score@x.com" via LIKE's
	// unescaped '_' wildcard (which matches any single character).
	got, err = s.MessagesFrom(ctx, "under.score@x.com", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("MessagesFrom(under.score@x.com) = %+v, want no match — '_' must be literal, not a LIKE wildcard", got)
	}
	got, err = s.MessagesFrom(ctx, "under_score@x.com", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Subject != "underscore" {
		t.Fatalf("MessagesFrom(under_score@x.com) = %+v, want the underscore sender", got)
	}
}

func TestCountMessagesSinceBoundary(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, &Message{Meta: meta("m1", true), From: "a@x.com", SentAt: ts(8)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, &Message{Meta: meta("m2", true), From: "a@x.com", SentAt: ts(10)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, &Message{Meta: meta("m3", true), From: "a@x.com", SentAt: ts(12)}); err != nil {
		t.Fatal(err)
	}
	n, err := s.CountMessagesSince(ctx, ts(10))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("CountMessagesSince(ts(10)) = %d, want 2 (inclusive boundary)", n)
	}
}

func TestSendersDedupAndParse(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, &Message{Meta: meta("m1", true), From: "Alex Chen <alex@x.com>", SentAt: ts(8)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, &Message{Meta: meta("m2", true), From: "Alex Chen <alex@x.com>", SentAt: ts(9)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, &Message{Meta: meta("m3", true), From: "Dana Lee <DANA@x.com>", SentAt: ts(10)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, &Message{Meta: meta("m4", true), From: "not-an-address", SentAt: ts(11)}); err != nil {
		t.Fatal(err)
	}
	people, err := s.Senders(ctx, ts(0), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 2 {
		t.Fatalf("Senders = %+v, want 2 distinct people (malformed sender skipped)", people)
	}
	// Newest first: Dana (ts11 skipped as malformed... wait m4 is malformed,
	// so newest valid is Dana at ts(10)).
	if people[0].Email != "DANA@x.com" && people[0].Email != "dana@x.com" {
		t.Fatalf("expected Dana first (most recent valid sender), got %+v", people[0])
	}
	if people[0].Name != "Dana Lee" {
		t.Fatalf("Name not parsed: %+v", people[0])
	}
	foundAlex := false
	for _, p := range people {
		if p.Name == "Alex Chen" && p.Email == "alex@x.com" {
			foundAlex = true
		}
	}
	if !foundAlex {
		t.Fatalf("Alex Chen not found deduped in %+v", people)
	}
}

func TestSendersLimit(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	for i, addr := range []string{"a@x.com", "b@x.com", "c@x.com"} {
		if err := s.Upsert(ctx, &Message{Meta: meta("m"+addr, true), From: addr, SentAt: ts(8 + i)}); err != nil {
			t.Fatal(err)
		}
	}
	people, err := s.Senders(ctx, ts(0), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 2 {
		t.Fatalf("Senders limit=2 returned %d", len(people))
	}
}

func TestNextEventPicksSoonestFuture(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, &Event{Meta: meta("e1", false), Title: "past", StartAt: ts(5), EndAt: ts(6)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, &Event{Meta: meta("e2", false), Title: "soonest", StartAt: ts(11), EndAt: ts(12)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, &Event{Meta: meta("e3", false), Title: "later", StartAt: ts(15), EndAt: ts(16)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.NextEvent(ctx, ts(10))
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "soonest" {
		t.Fatalf("NextEvent = %+v, want 'soonest'", got)
	}
}

func TestNextEventNoneFound(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, &Event{Meta: meta("e1", false), Title: "past", StartAt: ts(5), EndAt: ts(6)}); err != nil {
		t.Fatal(err)
	}
	_, err := s.NextEvent(ctx, ts(10))
	if err != ErrNotFound {
		t.Fatalf("NextEvent with none upcoming = %v, want ErrNotFound", err)
	}
}

func TestCursorUpdatedAt(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, ok, err := s.CursorUpdatedAt(ctx, "gmail:history_id"); ok || err != nil {
		t.Fatalf("unset cursor: ok=%v err=%v", ok, err)
	}
	if err := s.SetCursor(ctx, "gmail:history_id", "12345"); err != nil {
		t.Fatal(err)
	}
	at, ok, err := s.CursorUpdatedAt(ctx, "gmail:history_id")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || at.IsZero() {
		t.Fatalf("CursorUpdatedAt = %v, %v", at, ok)
	}
}
