package decisions

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"water/internal/backend"
)

// TestTriagerConcurrentCallsClassifyOnce is an adversarial check for review
// point 5 (the classification-trigger cache): the existing tests only ever
// call Triage sequentially, which never actually exercises the inFlight
// dedup path under a real race. Here N goroutines race to classify the same
// item at once (simulating a CLI call racing the brief's background
// precompute, both against the one shared *Triager the daemon wires up in
// internal/cli/twin.go's buildDecisionsTrigger) and the model must be
// called exactly once.
func TestTriagerConcurrentCallsClassifyOnce(t *testing.T) {
	r := registry(t, map[string]string{"budget.yaml": budgetYAML})
	var calls atomic.Int64
	f := backend.NewFake("fake")
	f.Reply = func(backend.Request) string {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond) // widen the race window
		return `{"needs_decision": true, "type_id": "budget", "confidence": 0.9}`
	}
	mc := &ModelClassifier{Registry: r, Backend: f, Model: "haiku"}
	tr, err := NewTriager(mc, Candidate)
	if err != nil {
		t.Fatal(err)
	}
	item := attentionMsg("m1", "dana@x.com", "Budget approval?", "Can you approve this by Friday?")

	const n = 20
	var wg sync.WaitGroup
	results := make([]Classification, n)
	errs := make([]error, n)
	oks := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], oks[i], errs[i] = tr.Triage(context.Background(), item)
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil || !oks[i] || results[i].TypeID != "budget" {
			t.Fatalf("goroutine %d: %+v ok=%v err=%v", i, results[i], oks[i], errs[i])
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("classified %d time(s) under concurrent racing callers, want exactly 1", calls.Load())
	}
}
