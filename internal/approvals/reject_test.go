package approvals

import (
	"context"
	"testing"
	"time"
)

// rejectTestQueue is this file's own queue-and-store setup: newTestQueue
// (recipientcheck_test.go) also returns the audit file path, which none of
// these tests need.
func rejectTestQueue(t *testing.T) *Queue {
	t.Helper()
	q, _ := newTestQueue(t)
	return q
}

// TestRejectDeniesWithTheGivenReason: Reject transitions a pending envelope
// straight to Denied with the caller's own reason, not Decide's fixed
// "answered no"/"ambiguous answer" text.
func TestRejectDeniesWithTheGivenReason(t *testing.T) {
	q := rejectTestQueue(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	q.Now = func() time.Time { return now }

	env, err := q.Propose(ctx, Envelope{Action: "requests.respond", Origin: "test", Payload: map[string]any{"request_id": "r1", "answer": "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := q.Reject(ctx, env.ID, "changes requested")
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Status != Denied {
		t.Errorf("status = %s, want denied", rejected.Status)
	}
	if rejected.Reason != "changes requested" {
		t.Errorf("reason = %q, want %q", rejected.Reason, "changes requested")
	}
}

// TestRejectOnlyPending: an already-decided envelope can't be rejected
// again, and a race that loses the compare-and-swap reports an error rather
// than a denial that didn't actually apply.
func TestRejectOnlyPending(t *testing.T) {
	q := rejectTestQueue(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	q.Now = func() time.Time { return now }

	env, err := q.Propose(ctx, Envelope{Action: "requests.respond", Origin: "test", Payload: map[string]any{"request_id": "r1", "answer": "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Decide(ctx, env.ID, Yes); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Reject(ctx, env.ID, "changes requested"); err == nil {
		t.Fatal("Reject on an already-decided envelope should fail")
	}
}

// TestRejectExpiresStale mirrors Decide's own expiry behavior: an envelope
// past its expiry is expired, not denied with the caller's reason.
func TestRejectExpiresStale(t *testing.T) {
	q := rejectTestQueue(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	q.Now = func() time.Time { return now }

	env, err := q.Propose(ctx, Envelope{Action: "requests.respond", Origin: "test", Payload: map[string]any{"request_id": "r1", "answer": "ok"}, ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	q.Now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, err := q.Reject(ctx, env.ID, "changes requested"); err != ErrExpired {
		t.Fatalf("err = %v, want ErrExpired", err)
	}
}
