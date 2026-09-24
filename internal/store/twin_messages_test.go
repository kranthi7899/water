package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func twinRow(direction, id, typ, inReplyTo, hash string) TwinMessageRow {
	t0 := time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)
	return TwinMessageRow{Direction: direction, ID: id, FromTwin: "counterparty", ToTwin: "ceo", Type: typ, InReplyTo: inReplyTo,
		Subject: "s", Payload: "p", EvidenceRefs: []string{"ref"}, ReplyBy: t0.Add(time.Hour), SentAt: t0, RecordedAt: t0, ContentHash: hash}
}

// TestTwinMessages covers the table's guarantees: an identical re-delivery
// is a no-op, a conflicting reuse of an id is refused, one request gets at
// most one response per direction, and inbound rows are external by
// construction.
func TestTwinMessages(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	req := twinRow(TwinInbound, "tl_req", "request", "", "h1")
	if ok, err := s.InsertTwinMessage(ctx, req); !ok || err != nil {
		t.Fatalf("first insert: %v %v", ok, err)
	}
	if ok, err := s.InsertTwinMessage(ctx, req); ok || err != nil {
		t.Fatalf("identical re-delivery: inserted=%v err=%v, want a silent duplicate", ok, err)
	}
	conflict := req
	conflict.ContentHash = "h2"
	if _, err := s.InsertTwinMessage(ctx, conflict); !errors.Is(err, ErrTwinMessageConflict) {
		t.Fatalf("conflicting reuse: %v", err)
	}
	got, err := s.GetTwinMessage(ctx, TwinInbound, "tl_req")
	if err != nil || !got.External() || got.ReplyBy.IsZero() || len(got.EvidenceRefs) != 1 || got.ContentHash != "h1" {
		t.Fatalf("stored = %+v, %v", got, err)
	}

	// The same id in the other direction is a different message.
	if ok, err := s.InsertTwinMessage(ctx, twinRow(TwinOutbound, "tl_req", "request", "", "h1")); !ok || err != nil {
		t.Fatalf("outbound with the same id: %v %v", ok, err)
	}

	if ok, err := s.InsertTwinMessage(ctx, twinRow(TwinInbound, "tl_r1", "response", "tl_out", "h3")); !ok || err != nil {
		t.Fatalf("first response: %v %v", ok, err)
	}
	if _, err := s.InsertTwinMessage(ctx, twinRow(TwinInbound, "tl_r2", "response", "tl_out", "h4")); !errors.Is(err, ErrTwinResponseExists) {
		t.Fatalf("second response: %v, want ErrTwinResponseExists", err)
	}
	if r, err := s.TwinResponseTo(ctx, TwinInbound, "tl_out"); err != nil || r.ID != "tl_r1" {
		t.Fatalf("TwinResponseTo = %+v, %v", r, err)
	}
	if _, err := s.TwinResponseTo(ctx, TwinOutbound, "tl_out"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("TwinResponseTo other direction: %v", err)
	}

	in, err := s.ListTwinMessages(ctx, TwinInbound, 10)
	if err != nil || len(in) != 2 {
		t.Fatalf("inbound list = %d, %v", len(in), err)
	}
	all, _ := s.ListTwinMessages(ctx, "", 10)
	if len(all) != 3 {
		t.Fatalf("all = %d", len(all))
	}

	// external is pinned to the direction by the schema itself.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO twin_messages (direction, id, from_twin, to_twin, type, subject, payload,
		sent_at, recorded_at, content_hash, external) VALUES ('in', 'tl_x', 'a', 'b', 'notice', 's', 'p', 0, 0, 'h', 0)`); err == nil {
		t.Fatal("an inbound twin message was stored as trusted")
	}
}
