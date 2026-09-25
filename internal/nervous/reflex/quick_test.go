package reflex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"water/internal/store"
)

func testDeps(t *testing.T) (Deps, *store.Store) {
	t.Helper()
	deps, writer, _ := testFixture(t)
	return deps, writer
}

func newTestQuickService(deps Deps) *QuickService {
	return NewQuickService(func() Deps { return deps })
}

func TestQuickServiceExposesOnlyOptedInFunctions(t *testing.T) {
	deps, _ := testDeps(t)
	q := newTestQuickService(deps)
	fns := q.Functions()
	if len(fns) == 0 {
		t.Fatal("expected at least one quick function")
	}
	byID := map[string]bool{}
	for _, f := range fns {
		byID[f.ID] = true
		if !strings.HasPrefix(f.Tool, "quick__") {
			t.Errorf("tool name %q missing quick__ prefix", f.Tool)
		}
		if len(f.Schema) == 0 {
			t.Errorf("%s: empty schema", f.ID)
		}
	}
	// store.free_slots is a documented, deliberate exclusion (ClassCompute,
	// not a raw lookup) — must never be exposed as a quick tool.
	if byID["store.free_slots"] {
		t.Error("store.free_slots must not be exposed as a quick tool (ClassCompute, not QuickEligible)")
	}
	// control.cancel_tasks and status.overview have side effects / are
	// non-deterministic — must never be exposed either.
	if byID["control.cancel_tasks"] || byID["status.overview"] {
		t.Error("side-effecting or non-deterministic functions must not be quick tools")
	}
	if !byID["quick.calendar"] {
		t.Error("expected quick.calendar (store.calendar_events) to be a quick tool")
	}
}

func TestQuickInvokeUnknownID(t *testing.T) {
	deps, _ := testDeps(t)
	q := newTestQuickService(deps)
	if _, err := q.Run(context.Background(), "store.nonexistent", nil); err == nil {
		t.Fatal("expected an error for an unknown quick function")
	}
}

func TestQuickInvokeCalendarWithDefault(t *testing.T) {
	deps, writer := testDeps(t)
	ctx := context.Background()
	today := fixtureNow.Truncate(24 * time.Hour)
	if err := writer.Upsert(ctx, &store.Event{
		Meta: meta("e1", false), Title: "Standup", StartAt: today.Add(9 * time.Hour), EndAt: today.Add(9*time.Hour + 30*time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	q := newTestQuickService(deps)
	res, err := q.Run(ctx, "quick.calendar", map[string]any{})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if res.Tainted {
		t.Error("expected untainted result from an internal-only event")
	}
	var decoded struct {
		Items []map[string]string `json:"items"`
	}
	if err := json.Unmarshal(res.Output, &decoded); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if len(decoded.Items) != 1 || decoded.Items[0]["title"] != "Standup" {
		t.Fatalf("items = %+v", decoded.Items)
	}
}

func TestQuickInvokeTaintPropagates(t *testing.T) {
	deps, writer := testDeps(t)
	ctx := context.Background()
	today := fixtureNow.Truncate(24 * time.Hour)
	if err := writer.Upsert(ctx, &store.Event{
		Meta: meta("e2", true), Title: "External sync", StartAt: today.Add(10 * time.Hour), EndAt: today.Add(11 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	q := newTestQuickService(deps)
	res, err := q.Run(ctx, "quick.calendar", map[string]any{})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !res.Tainted {
		t.Error("expected a tainted result from an External event")
	}
}

func TestQuickInvokeCountArgument(t *testing.T) {
	deps, writer := testDeps(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := writer.Upsert(ctx, &store.Message{
			Meta: meta(fmt.Sprintf("m%d", i), false), From: "a@x.com", Subject: fmt.Sprintf("subj %d", i),
			SentAt: fixtureNow.Add(time.Duration(-i) * time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	q := newTestQuickService(deps)
	res, err := q.Run(ctx, "quick.latest_mail", map[string]any{"n": 2})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var decoded struct {
		Items []map[string]string `json:"items"`
	}
	if err := json.Unmarshal(res.Output, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Items) != 2 {
		t.Fatalf("expected n=2 to cap results, got %d", len(decoded.Items))
	}
}

func TestQuickInvokeUnresolvedArgument(t *testing.T) {
	deps, _ := testDeps(t)
	q := newTestQuickService(deps)
	// "who" doesn't match any known sender: no candidates at all -> Unresolved.
	if _, err := q.Run(context.Background(), "quick.mail_from", map[string]any{"who": "nobody at all"}); err == nil {
		t.Fatal("expected an error for an unresolved person argument")
	}
}

func TestQuickInvokeAmbiguousArgumentNamesCandidates(t *testing.T) {
	deps, writer := testDeps(t)
	ctx := context.Background()
	for i, from := range []string{"Alex Chen <alex.chen@x.com>", "Alex Rivera <alex.rivera@x.com>"} {
		if err := writer.Upsert(ctx, &store.Message{
			Meta: meta(fmt.Sprintf("s%d", i), false), From: from, Subject: "hi", SentAt: fixtureNow.Add(time.Duration(-i) * time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	q := newTestQuickService(deps)
	_, err := q.Run(ctx, "quick.mail_from", map[string]any{"who": "alex"})
	if err == nil {
		t.Fatal("expected an ambiguous-argument error")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error = %q, want it to mention ambiguity and candidates", err.Error())
	}
}

func TestQuickInvokeMissingRequiredArgument(t *testing.T) {
	deps, _ := testDeps(t)
	q := newTestQuickService(deps)
	if _, err := q.Run(context.Background(), "quick.mail_from", map[string]any{}); err == nil {
		t.Fatal("expected an error for a missing required argument")
	}
}

// TestQuickParallel proves the read-only pool tolerates heavy concurrent
// quick-tool traffic alongside a writer, mirroring the store package's own
// read-pool test (R-4) but through the QuickService seam.
func TestQuickParallel(t *testing.T) {
	deps, writer := testDeps(t)
	ctx := context.Background()
	q := newTestQuickService(deps)

	stop := make(chan struct{})
	var writerWG sync.WaitGroup
	writerWG.Add(1)
	go func() {
		defer writerWG.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = writer.Upsert(ctx, &store.Event{
				Meta: meta(fmt.Sprintf("w%d", i), false), Title: "x",
				StartAt: fixtureNow.Add(time.Duration(i) * time.Minute), EndAt: fixtureNow.Add(time.Duration(i+30) * time.Minute),
			})
			i++
		}
	}()

	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				// t.Errorf (unlike Fatalf) is safe to call concurrently from
				// multiple goroutines.
				if _, err := q.Run(ctx, "quick.calendar", map[string]any{}); err != nil {
					t.Errorf("concurrent store.calendar_events: %v", err)
				}
				if _, err := q.Run(ctx, "quick.next_event", map[string]any{}); err != nil {
					t.Errorf("concurrent store.next_event: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	writerWG.Wait()
}
