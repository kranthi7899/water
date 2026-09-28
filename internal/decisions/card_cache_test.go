package decisions

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"water/internal/gate"
)

// countingGate is a concurrency-safe Invoker that counts every evidence
// fetch and answers each with no records. onInvoke, when set, runs inside
// the fetch (to cancel a context or invalidate the cache mid-build).
type countingGate struct {
	n        atomic.Int64
	mu       sync.Mutex
	onInvoke func()
}

func (g *countingGate) Invoke(context.Context, gate.Call) (gate.Result, error) {
	g.n.Add(1)
	g.mu.Lock()
	f := g.onInvoke
	g.mu.Unlock()
	if f != nil {
		f()
	}
	return gate.Result{}, nil
}

func (g *countingGate) set(f func()) {
	g.mu.Lock()
	g.onInvoke = f
	g.mu.Unlock()
}

// cachedTrigger stores two candidate messages that both classify as budget
// cards, and returns a Trigger over them with the given card TTL plus the
// gate that counts its evidence fetches (one gmail.list_messages per card).
func cachedTrigger(t *testing.T, ttl time.Duration) (*Trigger, *countingGate) {
	t.Helper()
	st := openTestStore(t)
	ctx := context.Background()
	for _, m := range []string{"m1", "m2"} {
		if err := st.Upsert(ctx, attentionMsg(m, "dana@x.com", "Budget approval?", "Can you approve this by Friday?")); err != nil {
			t.Fatal(err)
		}
	}
	r := registry(t, map[string]string{"budget.yaml": budgetYAML})
	var calls atomic.Int64
	tr, err := NewTriager(modelClassifier(r, `{"needs_decision": true, "type_id": "budget", "confidence": 0.9}`, &calls), Candidate)
	if err != nil {
		t.Fatal(err)
	}
	g := &countingGate{}
	return &Trigger{Store: st, Triager: tr, Builder: &Builder{Registry: r, Gate: g}, CardTTL: ttl}, g
}

func runCards(t *testing.T, tr *Trigger, ctx context.Context, now time.Time) []*Card {
	t.Helper()
	cards, err := tr.Run(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 2 {
		t.Fatalf("want 2 cards, got %d", len(cards))
	}
	return cards
}

// TestTriggerReusesCardsWithinTTL is the rate-cap fix itself: a second Run
// inside CardTTL serves the last pass's cards without fetching any
// evidence through the gate again; at CardTTL (and for a clock that went
// backwards) it rebuilds.
func TestTriggerReusesCardsWithinTTL(t *testing.T) {
	tr, g := cachedTrigger(t, 10*time.Minute)
	ctx := context.Background()
	base := time.Now()

	first := runCards(t, tr, ctx, base)
	perBuild := g.n.Load()
	if perBuild == 0 {
		t.Fatal("the first run fetched no evidence; the test would prove nothing")
	}
	second := runCards(t, tr, ctx, base.Add(10*time.Minute-time.Second))
	if g.n.Load() != perBuild {
		t.Fatalf("a run inside the TTL fetched evidence again: %d gate calls, want %d", g.n.Load(), perBuild)
	}
	if first[0].ID != second[0].ID || first[1].ID != second[1].ID {
		t.Fatalf("cached run returned different cards")
	}

	runCards(t, tr, ctx, base.Add(10*time.Minute))
	if g.n.Load() != 2*perBuild {
		t.Fatalf("a run at the TTL must rebuild: %d gate calls, want %d", g.n.Load(), 2*perBuild)
	}
	runCards(t, tr, ctx, base) // earlier than the cached build: never trusted
	if g.n.Load() != 3*perBuild {
		t.Fatalf("a run with an earlier clock must rebuild: %d gate calls, want %d", g.n.Load(), 3*perBuild)
	}
}

// TestTriggerZeroTTLRebuildsEveryRun keeps the zero value's behaviour:
// CardTTL 0 (a Trigger built without the daemon's config) caches nothing.
func TestTriggerZeroTTLRebuildsEveryRun(t *testing.T) {
	tr, g := cachedTrigger(t, 0)
	ctx := context.Background()
	now := time.Now()
	runCards(t, tr, ctx, now)
	perBuild := g.n.Load()
	runCards(t, tr, ctx, now)
	if g.n.Load() != 2*perBuild {
		t.Fatalf("CardTTL 0 must rebuild every run: %d gate calls, want %d", g.n.Load(), 2*perBuild)
	}
}

// TestTriggerCachedSliceIsTheCallersOwn: callers sort (decisions.Rank) and
// filter the slice Run returns in place; that must never reorder or blank
// the cached pass another caller gets next.
func TestTriggerCachedSliceIsTheCallersOwn(t *testing.T) {
	tr, _ := cachedTrigger(t, time.Hour)
	ctx := context.Background()
	now := time.Now()
	first := runCards(t, tr, ctx, now)
	ids := []string{first[0].ID, first[1].ID}
	first[0], first[1] = nil, nil // the pass that filled the cache
	served := runCards(t, tr, ctx, now)
	served[0], served[1] = served[1], nil // a pass served from the cache
	again := runCards(t, tr, ctx, now)
	if again[0] == nil || again[1] == nil || again[0].ID != ids[0] || again[1].ID != ids[1] {
		t.Fatalf("mutating a returned slice changed the cache: %v", again)
	}
}

// TestTriggerNeverCachesACancelledPass: a caller whose context ends
// mid-build (an HTTP client that hung up) gets a pass whose failed fetches
// became gaps; that degraded pass must not be served to everyone else for
// the TTL.
func TestTriggerNeverCachesACancelledPass(t *testing.T) {
	tr, g := cachedTrigger(t, time.Hour)
	now := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel during the LAST card's fetch: the loop never sees ctx.Err()
	// again, so the pass comes back with no error, one card degraded.
	var fetches atomic.Int64
	g.set(func() {
		if fetches.Add(1) == 2 {
			cancel()
		}
	})
	_, _ = tr.Run(ctx, now)
	g.set(nil)
	before := g.n.Load()
	runCards(t, tr, context.Background(), now)
	if g.n.Load() == before {
		t.Fatal("a pass whose context was cancelled was cached and served again")
	}
}

// TestTriggerInvalidateForcesARebuild covers Invalidate, including one
// that lands while a pass is being built: that pass is returned to its own
// caller but not cached, so the next Run sees the state Invalidate
// announced rather than the pass that started before it.
func TestTriggerInvalidateForcesARebuild(t *testing.T) {
	tr, g := cachedTrigger(t, time.Hour)
	ctx := context.Background()
	now := time.Now()
	runCards(t, tr, ctx, now)
	perBuild := g.n.Load()
	tr.Invalidate()
	runCards(t, tr, ctx, now)
	if g.n.Load() != 2*perBuild {
		t.Fatalf("Invalidate did not force a rebuild: %d gate calls, want %d", g.n.Load(), 2*perBuild)
	}

	var once sync.Once
	g.set(func() { once.Do(tr.Invalidate) })
	runCards(t, tr, ctx, now) // cache was fresh: no build, no invalidation yet
	if g.n.Load() != 2*perBuild {
		t.Fatalf("a fresh cache rebuilt: %d gate calls", g.n.Load())
	}
	tr.Invalidate()
	runCards(t, tr, ctx, now) // builds; Invalidate fires mid-build
	g.set(nil)
	before := g.n.Load()
	runCards(t, tr, ctx, now)
	if g.n.Load() == before {
		t.Fatal("a pass invalidated mid-build was cached anyway")
	}
}

// TestTriggerConcurrentRunsBuildOnce: the needs-you ticker and HTTP
// handlers call Run from different goroutines. Racing callers on a cold
// cache share one build instead of each fetching every card's evidence.
func TestTriggerConcurrentRunsBuildOnce(t *testing.T) {
	tr, g := cachedTrigger(t, time.Hour)
	g.set(func() { time.Sleep(5 * time.Millisecond) }) // widen the race window
	now := time.Now()
	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	counts := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cards, err := tr.Run(context.Background(), now)
			errs[i], counts[i] = err, len(cards)
		}(i)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil || counts[i] != 2 {
			t.Fatalf("caller %d: %d cards, err %v", i, counts[i], errs[i])
		}
	}
	// One build fetches one gmail.list_messages per card.
	if got := g.n.Load(); got != 2 {
		t.Fatalf("%d concurrent callers made %d evidence fetches, want one build's 2", n, got)
	}
}
