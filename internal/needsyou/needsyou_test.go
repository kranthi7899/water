package needsyou

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/decisions"
	"water/internal/store"
)

// fakeSource is a canned DecisionSource for tests.
type fakeSource struct {
	cards []*decisions.Card
	err   error
}

func (f *fakeSource) Run(ctx context.Context, now time.Time) ([]*decisions.Card, error) {
	return f.cards, f.err
}

// harness builds a real temp-file store and a real approvals.Queue, the
// same pattern internal/approvals' own tests use (approvals_test.go).
func harness(t *testing.T) (*store.Store, *approvals.Queue) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	q := approvals.NewQueue(st, log)
	return st, q
}

func TestComputeOrdersDecisionsBySeverityThenPutsApprovalsAfter(t *testing.T) {
	st, q := harness(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	q.Now = func() time.Time { return now }

	src := &fakeSource{cards: []*decisions.Card{
		{ID: "c-low", Severity: 2, Lead: "low severity card"},
		{ID: "c-high", Severity: 3, Lead: "high severity card"},
	}}

	// An approval proposed well before now, so it clears the grace period.
	old := now.Add(-time.Hour)
	q.Now = func() time.Time { return old }
	if _, err := q.Propose(ctx, approvals.Envelope{Action: "gmail.send_message", Origin: "test", ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	q.Now = func() time.Time { return now }

	items, err := Compute(ctx, src, q, st, now, 2, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("len(items) = %d, want 3: %+v", len(items), items)
	}
	// Decisions block: severity descending.
	if items[0].Kind != KindDecision || items[0].ID != "c-high" {
		t.Fatalf("items[0] = %+v, want the severity-3 card first", items[0])
	}
	if items[1].Kind != KindDecision || items[1].ID != "c-low" {
		t.Fatalf("items[1] = %+v, want the severity-2 card second", items[1])
	}
	// Approvals block comes after every decision, regardless of severity.
	if items[2].Kind != KindApproval {
		t.Fatalf("items[2] = %+v, want the approval last", items[2])
	}
}

func TestComputeExcludesDismissedCard(t *testing.T) {
	st, q := harness(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := st.SetCardState(ctx, store.CardState{CardID: "c-dismissed", Status: "dismissed"}); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{cards: []*decisions.Card{
		{ID: "c-dismissed", Severity: 5, Lead: "should be excluded"},
		{ID: "c-kept", Severity: 5, Lead: "should remain"},
	}}

	items, err := Compute(ctx, src, q, st, now, 2, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "c-kept" {
		t.Fatalf("items = %+v, want only c-kept", items)
	}
}

func TestComputeSeverityThreshold(t *testing.T) {
	st, q := harness(t)
	ctx := context.Background()
	now := time.Now().UTC()

	src := &fakeSource{cards: []*decisions.Card{
		{ID: "c-2", Severity: 2, Lead: "at threshold"},
		{ID: "c-1", Severity: 1, Lead: "below threshold"},
	}}

	items, err := Compute(ctx, src, q, st, now, 2, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "c-2" {
		t.Fatalf("items = %+v, want only the severity-2 card", items)
	}
}

func TestComputeApprovalGraceExcludesYoungIncludesOld(t *testing.T) {
	st, q := harness(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

	q.Now = func() time.Time { return now.Add(-time.Minute) } // older than the 30s grace
	oldEnv, err := q.Propose(ctx, approvals.Envelope{Action: "gcal.create_event", Origin: "test"})
	if err != nil {
		t.Fatal(err)
	}
	q.Now = func() time.Time { return now.Add(-5 * time.Second) } // younger than the 30s grace
	if _, err := q.Propose(ctx, approvals.Envelope{Action: "gcal.move_event", Origin: "test"}); err != nil {
		t.Fatal(err)
	}
	q.Now = func() time.Time { return now }

	items, err := Compute(ctx, &fakeSource{}, q, st, now, 2, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != oldEnv.ID {
		t.Fatalf("items = %+v, want only the old envelope %s", items, oldEnv.ID)
	}
}

func TestComputeReturnsSourceErrorNotEmptyList(t *testing.T) {
	st, q := harness(t)
	ctx := context.Background()
	now := time.Now().UTC()

	wantErr := errors.New("boom")
	src := &fakeSource{err: wantErr}

	items, err := Compute(ctx, src, q, st, now, 2, 30*time.Second)
	if err == nil {
		t.Fatal("Compute returned nil error, want the source's error to surface")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want it to wrap %v", err, wantErr)
	}
	if items != nil {
		t.Fatalf("items = %+v, want nil (never a silently empty list on error)", items)
	}
}

func TestServiceTickTwiceInsertsExactlyOneNotification(t *testing.T) {
	st, q := harness(t)
	ctx := context.Background()
	now := time.Now().UTC()

	src := &fakeSource{cards: []*decisions.Card{{ID: "c-1", Severity: 5, Lead: "needs attention"}}}
	svc := NewService(src, q, st, 2, 30*time.Second)

	if err := svc.Tick(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := svc.Tick(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	notifs, err := st.ListUndeliveredNotifications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(notifs) != 1 {
		t.Fatalf("len(notifs) = %d, want exactly 1: %+v", len(notifs), notifs)
	}
	if notifs[0].RecordType != KindDecision || notifs[0].RecordID != "c-1" {
		t.Fatalf("notifs[0] = %+v", notifs[0])
	}
}

func TestServiceSnapshotReflectsLastTickAndIsACopy(t *testing.T) {
	st, q := harness(t)
	ctx := context.Background()
	now := time.Now().UTC()

	src := &fakeSource{cards: []*decisions.Card{{ID: "c-1", Severity: 5, Lead: "needs attention"}}}
	svc := NewService(src, q, st, 2, 30*time.Second)

	if got := svc.Snapshot(); len(got) != 0 {
		t.Fatalf("Snapshot before any Tick = %+v, want empty", got)
	}
	if err := svc.Tick(ctx, now); err != nil {
		t.Fatal(err)
	}
	snap := svc.Snapshot()
	if len(snap) != 1 || snap[0].ID != "c-1" {
		t.Fatalf("Snapshot after Tick = %+v", snap)
	}
	snap[0].ID = "corrupted"
	again := svc.Snapshot()
	if again[0].ID != "c-1" {
		t.Fatalf("mutating the returned slice corrupted the Service's own state: %+v", again)
	}
}
