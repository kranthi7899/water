package runtime

import (
	"sync"
	"testing"
	"time"

	"water/internal/backend"
	"water/internal/store"
)

func TestComputeAndCacheBriefReturnsCachedInstantlyWithNoBackendCall(t *testing.T) {
	env, ctx := testEnv(t)
	day := startOfDay(env.now()).Format("2006-01-02")
	if err := env.Store.SetBrief(ctx, day, "cached text"); err != nil {
		t.Fatal(err)
	}
	fk := backend.NewFake("fake")
	env.Backend = fk

	text, err := ComputeAndCacheBrief(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	if text != "cached text" {
		t.Fatalf("text = %q, want the cached text", text)
	}
	if fk.Calls() != 0 {
		t.Fatalf("backend called %d times on a cache hit, want 0", fk.Calls())
	}
}

func TestComputeAndCacheBriefComputesAndCachesOnFirstCall(t *testing.T) {
	env, ctx := testEnv(t)
	fk := backend.NewFake("fake")
	env.Backend = fk

	text, err := ComputeAndCacheBrief(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	if text == "" {
		t.Fatal("computed brief is empty")
	}
	if fk.Calls() != 1 {
		t.Fatalf("backend called %d times, want exactly 1", fk.Calls())
	}
	day := startOfDay(env.now()).Format("2006-01-02")
	cached, ok, err := env.Store.GetBrief(ctx, day)
	if err != nil || !ok || cached != text {
		t.Fatalf("cached=%q ok=%v err=%v, want %q", cached, ok, err, text)
	}
}

// TestComputeAndCacheBriefConcurrentCallersShareOneComputation is the
// compute-once guard: a background precompute and an on-demand ask racing
// for the same day must never both call the model.
func TestComputeAndCacheBriefConcurrentCallersShareOneComputation(t *testing.T) {
	env, ctx := testEnv(t)
	fk := backend.NewFake("fake")
	fk.Reply = func(backend.Request) string {
		time.Sleep(30 * time.Millisecond) // widen the race window
		return "the brief"
	}
	env.Backend = fk

	const n = 4
	var wg sync.WaitGroup
	texts := make([]string, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			texts[i], errs[i] = ComputeAndCacheBrief(ctx, env)
		}()
	}
	wg.Wait()

	for i := range texts {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if texts[i] != "the brief" {
			t.Fatalf("caller %d text = %q, want %q", i, texts[i], "the brief")
		}
	}
	if calls := fk.Calls(); calls != 1 {
		t.Fatalf("backend called %d times across %d concurrent callers, want exactly 1", calls, n)
	}
}

// TestBriefAnswerPropagatesTaintForExternalMessages checks that a fast-path
// brief answer escalates taint (via Env.OnTaint) exactly when its signals
// include an External record, same rule as StateSummary.
func TestBriefAnswerPropagatesTaintForExternalMessages(t *testing.T) {
	env, ctx := testEnv(t)
	var taintCalled, gotTaint bool
	env.OnTaint = func(tainted bool) { taintCalled, gotTaint = true, tainted }

	msg := &store.Message{
		Meta:    store.Meta{Source: "gmail", SourceID: "m1", External: true, CreatedAt: env.now().Add(-time.Hour)},
		From:    "dana@acme.com",
		Subject: "Question about the deck",
		Body:    "Can you review this by tomorrow?",
	}
	if err := env.Store.Upsert(ctx, msg); err != nil {
		t.Fatal(err)
	}

	if _, ok := FastPath(ctx, env, "what's my morning brief"); !ok {
		t.Fatal("expected a fast-path match")
	}
	if !taintCalled {
		t.Fatal("OnTaint was never called")
	}
	if !gotTaint {
		t.Fatal("taint should be true: an External message was in the brief's signals")
	}
}

func TestBriefAnswerNoTaintWhenNothingExternal(t *testing.T) {
	env, ctx := testEnv(t)
	var taintCalled, gotTaint bool
	env.OnTaint = func(tainted bool) { taintCalled, gotTaint = true, tainted }

	if _, ok := FastPath(ctx, env, "what's my morning brief"); !ok {
		t.Fatal("expected a fast-path match")
	}
	if !taintCalled {
		t.Fatal("OnTaint was never called")
	}
	if gotTaint {
		t.Fatal("taint should be false: nothing in the signals is External")
	}
}
