package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/store"
)

func testEnv(t *testing.T) (Env, context.Context) {
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
	now := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
	q.Now = func() time.Time { return now }
	return Env{Store: st, Approvals: q, Now: func() time.Time { return now }}, context.Background()
}

func TestFastPathMatchesAndNearMisses(t *testing.T) {
	cases := map[string]bool{
		"what's on my calendar today":                       true,
		"what's on my calendar tomorrow":                    true,
		"do I have any meetings today":                      true,
		"any pending approvals":                             true,
		"what needs my approval":                            true,
		"is my morning brief ready":                         true,
		"help me schedule a meeting with the calendar team": false,
		"please schedule a meeting with bob tomorrow":       false,
		"can you approve this for me":                       false,
		"tell me about the weather":                         false,
		"write a brief history of the company":              false,
	}
	env, ctx := testEnv(t)
	for prompt, wantOK := range cases {
		_, ok := FastPath(ctx, env, prompt)
		if ok != wantOK {
			t.Errorf("FastPath(%q) ok=%v, want %v", prompt, ok, wantOK)
		}
	}
}

func TestFastPathScheduleAnswersFromStore(t *testing.T) {
	env, ctx := testEnv(t)
	now := env.now()
	start := startOfDay(now)
	ev := &store.Event{
		Meta:    store.Meta{Source: "fake_calendar", SourceID: "ev1"},
		Title:   "Board sync",
		StartAt: start.Add(10 * time.Hour),
	}
	if err := env.Store.Upsert(ctx, ev); err != nil {
		t.Fatal(err)
	}
	text, ok := FastPath(ctx, env, "what's on my calendar today")
	if !ok {
		t.Fatal("expected a fast-path match")
	}
	if !contains(text, "Board sync") {
		t.Fatalf("answer = %q, want it to mention Board sync", text)
	}
}

func TestFastPathApprovalsEmptyQueue(t *testing.T) {
	env, ctx := testEnv(t)
	text, ok := FastPath(ctx, env, "any pending approvals")
	if !ok {
		t.Fatal("expected a fast-path match")
	}
	if text != "No approvals waiting." {
		t.Fatalf("text = %q", text)
	}
}

func TestFastPathBriefNotReady(t *testing.T) {
	env, ctx := testEnv(t)
	text, ok := FastPath(ctx, env, "is my brief ready")
	if !ok || text != "Your morning brief isn't ready yet." {
		t.Fatalf("text=%q ok=%v", text, ok)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
