package store

import (
	"context"
	"testing"
)

// TestMessageBulkSignalsRoundTrip proves migration 0016's new columns
// (labels, list_unsubscribe, list_id, precedence, auto_submitted) round-trip
// through Upsert/List like every other Message field.
func TestMessageBulkSignalsRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	in := &Message{
		Meta:            meta("bulk1", true),
		From:            "updates@mail.example.com",
		Subject:         "Digest",
		Body:            "snippet",
		Labels:          []string{"CATEGORY_PROMOTIONS", "INBOX"},
		ListUnsubscribe: "<mailto:unsub@example.com>",
		ListID:          "<digest.example.com>",
		Precedence:      "bulk",
		AutoSubmitted:   "no",
	}
	if err := s.Upsert(ctx, in); err != nil {
		t.Fatal(err)
	}
	out, err := Get[Message, *Message](ctx, s, "fake", "bulk1")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Labels) != 2 || out.Labels[0] != "CATEGORY_PROMOTIONS" || out.Labels[1] != "INBOX" {
		t.Errorf("Labels = %v, want [CATEGORY_PROMOTIONS INBOX]", out.Labels)
	}
	if out.ListUnsubscribe != in.ListUnsubscribe {
		t.Errorf("ListUnsubscribe = %q, want %q", out.ListUnsubscribe, in.ListUnsubscribe)
	}
	if out.ListID != in.ListID {
		t.Errorf("ListID = %q, want %q", out.ListID, in.ListID)
	}
	if out.Precedence != in.Precedence {
		t.Errorf("Precedence = %q, want %q", out.Precedence, in.Precedence)
	}
	if out.AutoSubmitted != in.AutoSubmitted {
		t.Errorf("AutoSubmitted = %q, want %q", out.AutoSubmitted, in.AutoSubmitted)
	}
}

// TestMessageBulkSignalsDefaultEmpty proves an old-shaped row (no bulk
// signals set) reads back the migration's defaults, not zero values that
// happen to look different (e.g. a nil vs empty slice distinction would
// silently break mailnoise.Signals construction).
func TestMessageBulkSignalsDefaultEmpty(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, &Message{Meta: meta("bulk2", true), Subject: "plain"}); err != nil {
		t.Fatal(err)
	}
	out, err := Get[Message, *Message](ctx, s, "fake", "bulk2")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Labels) != 0 {
		t.Errorf("Labels = %v, want empty", out.Labels)
	}
	if out.ListUnsubscribe != "" || out.ListID != "" || out.Precedence != "" || out.AutoSubmitted != "" {
		t.Errorf("bulk signal fields not empty: %+v", out)
	}
}

func TestSentToDomain(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	// A message the agent sent, from one of the CEO's own configured
	// addresses, to a recipient at vendor.example.
	if err := s.Upsert(ctx, &Message{
		Meta: meta("sent1", true), From: "water.twin@gmail.com", To: []string{"Billing <billing@vendor.example>"},
		Subject: "Re: invoice", Body: "thanks",
	}); err != nil {
		t.Fatal(err)
	}
	// An unrelated inbound message from vendor.example, which must not by
	// itself satisfy SentToDomain (From is the vendor, not the CEO).
	if err := s.Upsert(ctx, &Message{
		Meta: meta("inbound1", true), From: "billing@vendor.example", To: []string{"water.twin@gmail.com"},
		Subject: "Invoice", Body: "hi",
	}); err != nil {
		t.Fatal(err)
	}

	own := []string{"water.twin@gmail.com", "forward@ceo-real.example"}
	got, err := s.SentToDomain(ctx, own, "vendor.example")
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("SentToDomain(vendor.example) = false, want true")
	}

	got, err = s.SentToDomain(ctx, own, "never-contacted.example")
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("SentToDomain(never-contacted.example) = true, want false")
	}

	// Empty domain/fromAddrs never match everything.
	if got, err := s.SentToDomain(ctx, own, ""); err != nil || got {
		t.Errorf("SentToDomain(empty domain) = %v, %v, want false, nil", got, err)
	}
	if got, err := s.SentToDomain(ctx, nil, "vendor.example"); err != nil || got {
		t.Errorf("SentToDomain(nil fromAddrs) = %v, %v, want false, nil", got, err)
	}
}
