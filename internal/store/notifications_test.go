package store

import (
	"context"
	"errors"
	"testing"
)

func TestInsertNotificationIfNewOnlyOncePerRecord(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	n := Notification{RecordType: "decision", RecordID: "d-1", Title: "Budget decision needs you", Body: "Q3 budget"}
	created, err := s.InsertNotificationIfNew(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first InsertNotificationIfNew: created = false, want true")
	}

	n2 := Notification{RecordType: "decision", RecordID: "d-1", Title: "duplicate attempt", Body: "should be ignored"}
	created, err = s.InsertNotificationIfNew(ctx, n2)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("second InsertNotificationIfNew for the same record: created = true, want false")
	}

	undelivered, err := s.ListUndeliveredNotifications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(undelivered) != 1 {
		t.Fatalf("ListUndeliveredNotifications = %+v, want exactly 1 row", undelivered)
	}
	if undelivered[0].Title != "Budget decision needs you" {
		t.Fatalf("stored row = %+v, want the first insert's title kept", undelivered[0])
	}
}

func TestMarkNotificationDeliveredRemovesFromUndeliveredAndIsIdempotent(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	n := Notification{RecordType: "approval", RecordID: "a-1", Title: "Approval pending"}
	if _, err := s.InsertNotificationIfNew(ctx, n); err != nil {
		t.Fatal(err)
	}
	// InsertNotificationIfNew takes Notification by value and only reports
	// created/err, not the generated id — a caller (like this test) recovers
	// it the same way a real delivery poller would: by listing undelivered
	// notifications.
	before, err := s.ListUndeliveredNotifications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("ListUndeliveredNotifications before delivery = %+v, want exactly 1", before)
	}
	id := before[0].ID

	if err := s.MarkNotificationDelivered(ctx, id); err != nil {
		t.Fatal(err)
	}
	undelivered, err := s.ListUndeliveredNotifications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(undelivered) != 0 {
		t.Fatalf("ListUndeliveredNotifications after delivery = %+v, want empty", undelivered)
	}

	// Marking delivered twice must not error.
	if err := s.MarkNotificationDelivered(ctx, id); err != nil {
		t.Fatalf("second MarkNotificationDelivered: %v", err)
	}

	// Marking an unknown id delivered must not error either.
	if err := s.MarkNotificationDelivered(ctx, "ntf_does_not_exist"); err != nil {
		t.Fatalf("MarkNotificationDelivered on unknown id: %v", err)
	}
}

func TestGetNotificationReturnsDeliveredStateAndErrNotFound(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, err := s.InsertNotificationIfNew(ctx, Notification{ID: "ntf_a", RecordType: "decision", RecordID: "card-1", Title: "T", Body: "B"}); err != nil {
		t.Fatal(err)
	}
	n, err := s.GetNotification(ctx, "ntf_a")
	if err != nil || n.RecordID != "card-1" || n.Title != "T" || n.DeliveredAt != nil {
		t.Fatalf("GetNotification = %+v, %v", n, err)
	}
	if err := s.MarkNotificationDelivered(ctx, "ntf_a"); err != nil {
		t.Fatal(err)
	}
	n, err = s.GetNotification(ctx, "ntf_a")
	if err != nil || n.DeliveredAt == nil {
		t.Fatalf("after delivery: %+v, %v; want DeliveredAt set", n, err)
	}
	if _, err := s.GetNotification(ctx, "ntf_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: err = %v, want ErrNotFound", err)
	}
}
