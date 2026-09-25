package reflex

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/store"
)

// fixtureNow is a Thursday, matching internal/nervous/eval's DefaultFixture.
var fixtureNow = time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)

func nowFunc() time.Time { return fixtureNow }

func meta(id string, ext bool) store.Meta {
	return store.Meta{Source: "fake", SourceID: id, External: ext, CreatedAt: fixtureNow, UpdatedAt: fixtureNow}
}

// testFixture opens a writer store for seeding, then a separate read-only
// handle (the same one production code uses) wrapped as a StoreView, plus a
// live approvals queue. Returns Deps ready for a handler call.
func testFixture(t *testing.T) (Deps, *store.Store, *approvals.Queue) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "water.db")
	writer, err := store.Open(path)
	if err != nil {
		t.Fatalf("open writer store: %v", err)
	}
	t.Cleanup(func() { writer.Close() })

	ro, err := store.OpenReadOnly(path, 4)
	if err != nil {
		t.Fatalf("open read-only store: %v", err)
	}
	t.Cleanup(func() { ro.Close() })

	logPath := filepath.Join(t.TempDir(), "audit.jsonl")
	log, err := audit.Open(logPath)
	if err != nil {
		t.Fatalf("open audit log: %v", err)
	}
	t.Cleanup(func() { log.Close() })

	q := approvals.NewQueue(writer, log)
	q.Now = nowFunc

	deps := Deps{
		Store:     NewStoreView(ro),
		Approvals: q,
		Now:       nowFunc,
		Brief:     func(ctx context.Context, day string) (string, bool, bool, error) { return "", false, false, nil },
		Tasks:     &fakeTasks{},
		TaskID:    "t-self",
	}
	return deps, writer, q
}

type fakeTasks struct {
	running   int
	cancelled []string
}

func (f *fakeTasks) Running() int { return f.running }
func (f *fakeTasks) CancelAllExcept(taskID string) int {
	n := f.running
	if n > 0 {
		n--
	}
	f.cancelled = append(f.cancelled, taskID)
	return n
}

func TestScheduleOnDateTaintsFromExternalEvent(t *testing.T) {
	deps, writer, _ := testFixture(t)
	ctx := context.Background()

	tomorrow := fixtureNow.AddDate(0, 0, 1)
	tomorrowStart := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, 0, time.UTC)

	if err := writer.Upsert(ctx, &store.Event{
		Meta:  meta("e1", true), // External: true
		Title: "Board sync", StartAt: tomorrowStart.Add(10 * time.Hour), EndAt: tomorrowStart.Add(11 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	when := Args{"when": {
		Type: "daterange", Label: "tomorrow", Spoken: "Tomorrow, Fri 25 Sep",
		Start: tomorrowStart, End: tomorrowStart.AddDate(0, 0, 1),
	}}
	res, err := scheduleHandler(ctx, deps, when)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("Items = %v, want 1 event", res.Items)
	}
	if !res.Tainted {
		t.Fatal("an External event in the range must taint the result (regression: old fastpath's scheduleAnswer never did this for tomorrow)")
	}
	if res.Interpretation != "Tomorrow, Fri 25 Sep" {
		t.Fatalf("Interpretation = %q", res.Interpretation)
	}
}

func TestScheduleOnDateNotTaintedWhenAllInternal(t *testing.T) {
	deps, writer, _ := testFixture(t)
	ctx := context.Background()
	todayStart := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	if err := writer.Upsert(ctx, &store.Event{
		Meta: meta("e1", false), Title: "1:1", StartAt: todayStart.Add(10 * time.Hour), EndAt: todayStart.Add(11 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	when := Args{"when": {Start: todayStart, End: todayStart.AddDate(0, 0, 1), Spoken: "Today, Thu 24 Sep"}}
	res, err := scheduleHandler(ctx, deps, when)
	if err != nil {
		t.Fatal(err)
	}
	if res.Tainted {
		t.Fatal("no External event in range: must not taint")
	}
}

func TestFreeSlotsComputesGaps(t *testing.T) {
	deps, writer, _ := testFixture(t)
	ctx := context.Background()
	// Handlers format times with .Local(); build the fixture in the same
	// zone the test process runs in so the expected wall-clock strings
	// below don't depend on the machine's offset from UTC.
	today := time.Date(2026, 9, 24, 0, 0, 0, 0, time.Local)
	// A meeting 10:00-11:00 inside the 09:00-18:00 workday window.
	if err := writer.Upsert(ctx, &store.Event{
		Meta: meta("e1", false), Title: "Standup", StartAt: today.Add(10 * time.Hour), EndAt: today.Add(11 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	res, err := freeSlotsHandler(ctx, deps, Args{"when": {Start: today, Spoken: "today"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("Items = %+v, want 2 gaps (09:00-10:00 and 11:00-18:00)", res.Items)
	}
	if res.Items[0]["from"] != "09:00" || res.Items[0]["to"] != "10:00" {
		t.Errorf("first gap = %v", res.Items[0])
	}
	if res.Items[1]["from"] != "11:00" || res.Items[1]["to"] != "18:00" {
		t.Errorf("second gap = %v", res.Items[1])
	}
}

func TestUnreadCountStatesPlainlyAndSeparatesReceivedToday(t *testing.T) {
	deps, writer, _ := testFixture(t)
	ctx := context.Background()
	today := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	if err := writer.Upsert(ctx, &store.Message{Meta: meta("m1", false), From: "a@x.com", Subject: "hi", SentAt: today.Add(8 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	res, err := unreadCountHandler(ctx, deps, Args{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Facts["received_today"] != "1" {
		t.Fatalf("received_today = %q, want 1", res.Facts["received_today"])
	}
	if res.Text == "" {
		t.Fatal("unread_count must state plainly that unread tracking isn't synced yet")
	}
}

func TestApprovalsBindPendingZeroOneTwo(t *testing.T) {
	deps, _, q := testFixture(t)
	ctx := context.Background()

	// 0 pending.
	res, err := bindPendingHandler(ctx, deps, Args{})
	if err != nil {
		t.Fatal(err)
	}
	if res.ApprovalID != "" || res.Kind != "clarify" {
		t.Fatalf("0 pending: got %+v", res)
	}

	// 1 pending.
	e1, err := q.Propose(ctx, approvals.Envelope{Action: "fake.a", Payload: map[string]any{}, Origin: "P0"})
	if err != nil {
		t.Fatal(err)
	}
	beforePending, _ := q.Pending(ctx)
	res, err = bindPendingHandler(ctx, deps, Args{})
	if err != nil {
		t.Fatal(err)
	}
	if res.ApprovalID != e1.ID || res.Kind != "decision" {
		t.Fatalf("1 pending: got %+v, want ApprovalID=%s", res, e1.ID)
	}
	afterPending, _ := q.Pending(ctx)
	if len(beforePending) != len(afterPending) {
		t.Fatal("bindPendingHandler must never change the pending queue")
	}

	// 2 pending.
	if _, err := q.Propose(ctx, approvals.Envelope{Action: "fake.b", Payload: map[string]any{}, Origin: "P0"}); err != nil {
		t.Fatal(err)
	}
	res, err = bindPendingHandler(ctx, deps, Args{})
	if err != nil {
		t.Fatal(err)
	}
	if res.ApprovalID != "" || res.NeedsClarification == nil || len(res.NeedsClarification.Options) != 2 {
		t.Fatalf("2 pending: got %+v", res)
	}
}

func TestCancelTasksCallsThroughTaskControl(t *testing.T) {
	deps, _, _ := testFixture(t)
	ft := deps.Tasks.(*fakeTasks)
	ft.running = 3
	res, err := cancelTasksHandler(context.Background(), deps, Args{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ft.cancelled) != 1 || ft.cancelled[0] != "t-self" {
		t.Fatalf("CancelAllExcept not called with the right task id: %v", ft.cancelled)
	}
	if res.Facts["n"] != "2" {
		t.Fatalf("Facts[n] = %q, want 2", res.Facts["n"])
	}
}

func TestStatusOverviewNilHealthAndNoVault(t *testing.T) {
	deps, _, _ := testFixture(t)
	deps.Health = nil // must not panic or error
	res, err := statusOverviewHandler(context.Background(), deps, Args{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Facts["running_tasks"] == "" {
		t.Fatal("expected a running_tasks fact")
	}
	// Absence of a vault import/call is enforced by imports_test.go's
	// static check, not runtime behavior.
}

func TestCachedBriefHitAndMiss(t *testing.T) {
	deps, _, _ := testFixture(t)
	// Miss.
	_, err := cachedBriefHandler(context.Background(), deps, Args{})
	if err == nil {
		t.Fatal("expected ErrBriefCacheMiss on a cache miss")
	}
	// Hit.
	deps.Brief = func(ctx context.Context, day string) (string, bool, bool, error) {
		return "Today: 1 event.", true, true, nil
	}
	res, err := cachedBriefHandler(context.Background(), deps, Args{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Today: 1 event." || !res.Tainted {
		t.Fatalf("got %+v", res)
	}
}

// TestCachedBriefNilFunc reproduces a real panic found during Slice R's
// live Phase 4 verification: internal/nervous/eval/live.go builds a
// reflex.Deps without ever setting Brief (the eval harness has no real
// brief cache to query), and every existing test's shared testFixture
// papers over this by always setting a non-nil stub, so the bug went
// uncaught until a live "brief.today"-shaped case actually reached this
// handler through the real Tier 1 eval. A nil Deps.Brief must read as an
// honest cache miss, not a nil-func-call panic.
func TestCachedBriefNilFunc(t *testing.T) {
	deps, _, _ := testFixture(t)
	deps.Brief = nil
	_, err := cachedBriefHandler(context.Background(), deps, Args{})
	if !errors.Is(err, ErrBriefCacheMiss) {
		t.Fatalf("err = %v, want ErrBriefCacheMiss", err)
	}
}

func TestHelpIntentsListsRegistry(t *testing.T) {
	deps, _, _ := testFixture(t)
	deps.Registry = []IntentSummary{{ID: "schedule.on_date", Description: "Events on a given day"}}
	res, err := helpIntentsHandler(context.Background(), deps, Args{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0]["id"] != "schedule.on_date" {
		t.Fatalf("got %v", res.Items)
	}
}
