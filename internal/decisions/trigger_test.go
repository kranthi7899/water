package decisions

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"water"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
)

// attentionMsg builds a message with its own subject/body, unlike this
// package's msg() test helper (decisions_test.go), which always puts a "?"
// and "can you" into Body — fine for build/classify tests, but it makes
// every message a NeedsAttention candidate regardless of subject, which
// would defeat a test that needs a genuine non-candidate.
func attentionMsg(id, from, subject, body string) *store.Message {
	return &store.Message{Meta: store.Meta{Source: "gmail", SourceID: id, External: true}, From: from, Subject: subject, Body: body}
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// TestTriggerSkipsAlreadyClassifiedOnSecondRun is the trigger/cache test the
// task calls for: a candidate item is classified once, a non-candidate is
// never classified, and a second Run — against a brand-new Triager (a fresh
// in-memory cache, simulating a daemon restart) but the same store — must
// not classify the already-seen item again, because StoreCache's persisted
// verdict answers it instead.
func TestTriggerSkipsAlreadyClassifiedOnSecondRun(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	candidate := attentionMsg("m1", "dana@x.com", "Budget approval?", "Can you approve this by Friday?")
	nonCandidate := attentionMsg("m2", "notifications@x.com", "Weekly summary", "Here is your weekly summary.")
	for _, m := range []*store.Message{candidate, nonCandidate} {
		if err := st.Upsert(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	r := registry(t, map[string]string{"budget.yaml": budgetYAML})
	var calls atomic.Int64
	mc := modelClassifier(r, `{"needs_decision": true, "type_id": "budget", "confidence": 0.9}`, &calls)
	g := &fakeGate{answers: map[string]func(gate.Call) (gate.Result, error){"gmail.list_messages": none}}
	build := &Builder{Registry: r, Gate: g}

	run := func() []*Card {
		tr, err := NewTriager(&StoreCache{Store: st, Inner: mc}, Candidate)
		if err != nil {
			t.Fatal(err)
		}
		cards, err := (&Trigger{Store: st, Triager: tr, Builder: build}).Run(ctx, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return cards
	}

	cards := run()
	if len(cards) != 1 || cards[0].TypeID != "budget" {
		t.Fatalf("first run: %+v", cards)
	}
	if calls.Load() != 1 {
		t.Fatalf("classified %d time(s) on first run, want 1", calls.Load())
	}

	cards = run()
	if len(cards) != 1 || cards[0].TypeID != "budget" {
		t.Fatalf("second run: %+v", cards)
	}
	if calls.Load() != 1 {
		t.Fatalf("classified %d time(s) after a second run with a fresh Triager, want still 1 (the store cache must have answered)", calls.Load())
	}

	if _, ok, err := st.GetDecisionClassification(ctx, "gmail", "m1"); err != nil || !ok {
		t.Fatalf("classification not persisted: %v %v", ok, err)
	}
	if _, ok, err := st.GetDecisionClassification(ctx, "gmail", "m2"); err != nil || ok {
		t.Fatalf("a non-candidate must never be classified or cached: %v %v", ok, err)
	}
}

// TestStoreCacheFallsThroughWithoutStoreIdentity covers an item Ref can't
// key by: StoreCache must still classify it (via Inner), just without
// caching anything.
func TestStoreCacheFallsThroughWithoutStoreIdentity(t *testing.T) {
	st := openTestStore(t)
	r := registry(t, map[string]string{"budget.yaml": budgetYAML})
	var calls atomic.Int64
	mc := modelClassifier(r, `{"needs_decision": true, "type_id": "budget", "confidence": 0.9}`, &calls)
	sc := &StoreCache{Store: st, Inner: mc}
	item := &store.Message{From: "dana@x.com", Subject: "Budget?", Body: "Can you approve?"}
	for i := 0; i < 2; i++ {
		if _, err := sc.Classify(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("an item with no store identity must never be cached: classified %d time(s)", calls.Load())
	}
}

// TestStubTypeMatchesAndBuildsSparseCard is the shipped-stub test the task
// calls for: investor_request and inbound_decision (twins/ceo/decisions/)
// load into the real CEO registry, Classify can pick one, and Builder
// produces a valid (if sparse, since nothing is actually wired to real
// data) Card of that type rather than falling back to generic.
func TestStubTypeMatchesAndBuildsSparseCard(t *testing.T) {
	m, err := twins.Load(water.TwinsFS(), "ceo")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := LoadRegistry(water.TwinsFS(), m)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"investor_request", "inbound_decision"} {
		if _, ok := reg.Lookup(id); !ok {
			t.Fatalf("stub type %q did not load", id)
		}
	}

	var calls atomic.Int64
	mc := modelClassifier(reg, `{"needs_decision": true, "type_id": "investor_request", "confidence": 0.8}`, &calls)
	item := attentionMsg("inv1", "dana@investor.example", "Q3 update?", "Can you send the Q3 numbers by Friday?")

	c, err := mc.Classify(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	if c.TypeID != "investor_request" || !c.NeedsDecision {
		t.Fatalf("classify did not match the stub type: %+v", c)
	}

	g := &fakeGate{answers: map[string]func(gate.Call) (gate.Result, error){"gmail.list_messages": none, "gdrive.search_files": none}}
	card, err := (&Builder{Registry: reg, Gate: g}).Build(context.Background(), item, c)
	if err != nil {
		t.Fatal(err)
	}
	if card.TypeID != "investor_request" {
		t.Fatalf("card built under the wrong type: %+v", card)
	}
	if err := card.Validate(); err != nil {
		t.Fatalf("stub card must still be valid: %v", err)
	}
	if card.Readiness != MissingInfo {
		t.Fatalf("a stub type whose needs are unwired should be missing_info, got %s", card.Readiness)
	}
	if len(card.Gaps) == 0 {
		t.Fatal("a sparse card must say what's missing, not stay silent")
	}
}
