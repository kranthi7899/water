package decisions

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"water"
	"water/internal/backend"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
)

// TestPhraserCannotSpellOutInventedNumbers: a number written as words is
// still a number, and must be sourced like a digit literal.
func TestPhraserCannotSpellOutInventedNumbers(t *testing.T) {
	r := registry(t, map[string]string{"budget.yaml": budgetYAML})
	item := msg("m1", true, "dana@x.com", "Q3 budget of 1,500 for tools")
	g := &fakeGate{answers: map[string]func(gate.Call) (gate.Result, error){"gmail.list_messages": found(msg("h1", true, "dana@x.com", "Earlier ask"))}}
	p := fixedPhraser{
		Lead:           "Dana asks for two thousand dollars",
		Question:       "Approve a dozen laptops?",
		Options:        []Option{{"Approve", "Runway stays above six months"}, {"Decline", "Dana waits"}},
		Recommendation: "Approve: runway stays above six months",
	}
	c, err := (&Builder{Registry: r, Gate: g, Phraser: p}).Build(context.Background(), item, Classification{TypeID: "budget"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Readiness != Ready {
		t.Fatalf("precondition: card must be ready, got %s", c.Readiness)
	}
	if !strings.HasPrefix(c.Lead, "Budget request: ") {
		t.Fatalf("spelled-out invented amount reached the lead: %q", c.Lead)
	}
	if strings.Contains(c.Question, "dozen") {
		t.Fatalf("spelled-out invented count reached the question: %q", c.Question)
	}
	if len(c.Options) != 0 {
		t.Fatalf("options with an invented spelled number must be dropped: %+v", c.Options)
	}
	if c.Recommendation != "" {
		t.Fatalf("spelled-out invented runway reached the recommendation: %q", c.Recommendation)
	}

	// A number word the facts already contain is allowed; so is a digit
	// with a magnitude suffix only when that exact token is in the facts.
	item2 := msg("m2", true, "dana@x.com", "Two laptops for the team")
	p2 := fixedPhraser{Lead: "Dana wants two laptops", Question: "Approve 2k for laptops?"}
	c2, _ := (&Builder{Registry: r, Gate: g, Phraser: p2}).Build(context.Background(), item2, Classification{TypeID: "budget"})
	if c2.Lead != "Dana wants two laptops" {
		t.Fatalf("a sourced number word must be kept: %q", c2.Lead)
	}
	if strings.Contains(c2.Question, "2k") {
		t.Fatalf("an invented magnitude suffix reached the question: %q", c2.Question)
	}
}

// chargeFails is a Classifier that fails every call, like a ModelClassifier
// whose usage-cap charge is refused.
type chargeFails struct{ calls atomic.Int64 }

func (c *chargeFails) Classify(context.Context, store.Record) (Classification, error) {
	c.calls.Add(1)
	return Classification{}, errors.New("model-call cap reached")
}

// TestTriggerRunReturnsPartialResultsOnPerItemFailure: one item's failed
// classification must not throw away every other item's card.
func TestTriggerRunReturnsPartialResultsOnPerItemFailure(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	cached := attentionMsg("m1", "dana@x.com", "Budget approval?", "Can you approve this?")
	fresh := attentionMsg("m2", "sam@x.com", "Vendor renewal?", "Can you sign off?")
	for _, m := range []*store.Message{cached, fresh} {
		if err := st.Upsert(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetDecisionClassification(ctx, store.DecisionClassification{Source: "gmail", SourceID: "m1", NeedsDecision: true, TypeID: "budget", Confidence: 0.9}); err != nil {
		t.Fatal(err)
	}
	r := registry(t, map[string]string{"budget.yaml": budgetYAML})
	inner := &chargeFails{}
	tr, err := NewTriager(&StoreCache{Store: st, Inner: inner}, Candidate)
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeGate{answers: map[string]func(gate.Call) (gate.Result, error){"gmail.list_messages": none}}
	trig := &Trigger{Store: st, Triager: tr, Builder: &Builder{Registry: r, Gate: g}}
	cards, err := trig.Run(ctx, time.Now())
	if err != nil {
		t.Fatalf("a per-item classification failure must not fail the run: %v", err)
	}
	if len(cards) != 1 || cards[0].SourceItemIDs[0] != "gmail:m1" {
		t.Fatalf("the already-classified item's card must survive: %+v", cards)
	}
	rep, err := trig.RunReport(ctx, time.Now())
	if err != nil || len(rep.Cards) != 1 || len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0].Error(), "gmail:m2") {
		t.Fatalf("report: %+v %v", rep, err)
	}
}

// countingPhraser counts Phrase calls (each a charged model call in prod).
type countingPhraser struct{ n atomic.Int64 }

func (p *countingPhraser) Phrase(_ context.Context, c Card, _ Type) (Prose, error) {
	p.n.Add(1)
	return Prose{Question: c.Question}, nil
}

// TestBuilderDoesNotRephraseAnUnchangedCard: listing decisions or building
// the brief rebuilds every open card; an unchanged card must not cost
// another model call each time.
func TestBuilderDoesNotRephraseAnUnchangedCard(t *testing.T) {
	r := registry(t, map[string]string{"budget.yaml": budgetYAML})
	g := &fakeGate{answers: map[string]func(gate.Call) (gate.Result, error){"gmail.list_messages": found(msg("h1", true, "dana@x.com", "Earlier ask"))}}
	p := &countingPhraser{}
	b := &Builder{Registry: r, Gate: g, Phraser: p}
	item := msg("m1", true, "dana@x.com", "Budget")
	for i := 0; i < 3; i++ {
		if _, err := b.Build(context.Background(), item, Classification{TypeID: "budget"}); err != nil {
			t.Fatal(err)
		}
	}
	if p.n.Load() != 1 {
		t.Fatalf("phrased %d times, want once for an unchanged card", p.n.Load())
	}
	g.answers["gmail.list_messages"] = found(msg("h1", true, "dana@x.com", "Earlier ask"), msg("h2", true, "dana@x.com", "Newer ask"))
	if _, err := b.Build(context.Background(), item, Classification{TypeID: "budget"}); err != nil {
		t.Fatal(err)
	}
	if p.n.Load() != 2 {
		t.Fatalf("a card whose facts changed must be phrased again: %d", p.n.Load())
	}
}

// TestUnreadableClassificationIsNotPersisted: a garbled reply still yields
// a generic card, but is not written to the durable cache, so a restart
// (or the retry interval) gets a fresh look.
func TestUnreadableClassificationIsNotPersisted(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	r := registry(t, map[string]string{"budget.yaml": budgetYAML})
	item := attentionMsg("m1", "dana@x.com", "Budget approval?", "Can you approve this?")

	reply := `{"needs_decision": tru`
	f := backend.NewFake("fake")
	var calls atomic.Int64
	f.Reply = func(backend.Request) string { calls.Add(1); return reply }
	mc := &ModelClassifier{Registry: r, Backend: f}

	tr, _ := NewTriager(&StoreCache{Store: st, Inner: mc}, Candidate)
	c, ok, err := tr.Triage(ctx, item)
	if err != nil || !ok || !c.NeedsDecision || c.TypeID != GenericID || !c.Fallback {
		t.Fatalf("garbled reply: %+v %v %v", c, ok, err)
	}
	if _, ok, _ := st.GetDecisionClassification(ctx, "gmail", "m1"); ok {
		t.Fatal("a fallback verdict must not be persisted")
	}
	// Within the retry interval the same Triager does not re-ask.
	tr.Triage(ctx, item)
	if calls.Load() != 1 {
		t.Fatalf("classified %d times within the retry interval", calls.Load())
	}
	// After it, it does.
	tr.now = func() time.Time { return time.Now().Add(2 * DefaultFallbackRetry) }
	reply = `{"needs_decision": true, "type_id": "budget", "confidence": 0.9}`
	if c, _, _ := tr.Triage(ctx, item); c.TypeID != "budget" || calls.Load() != 2 {
		t.Fatalf("retry after interval: %+v calls %d", c, calls.Load())
	}

	// Restart: a fresh Triager + StoreCache now sees the good verdict.
	tr2, _ := NewTriager(&StoreCache{Store: st, Inner: mc}, Candidate)
	if c, _, _ := tr2.Triage(ctx, item); c.TypeID != "budget" || calls.Load() != 2 {
		t.Fatalf("after restart: %+v calls %d", c, calls.Load())
	}
}

// TestForgetClearsTheDurableCacheToo: Forget must actually cause a
// re-classification, not be answered by the persisted row.
func TestForgetClearsTheDurableCacheToo(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	r := registry(t, map[string]string{"budget.yaml": budgetYAML})
	var calls atomic.Int64
	mc := modelClassifier(r, `{"needs_decision": true, "type_id": "budget", "confidence": 0.9}`, &calls)
	tr, _ := NewTriager(&StoreCache{Store: st, Inner: mc}, Candidate)
	item := attentionMsg("m1", "dana@x.com", "Budget approval?", "Can you approve this?")
	tr.Triage(ctx, item)
	if err := tr.Forget(ctx, "gmail:m1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.GetDecisionClassification(ctx, "gmail", "m1"); ok {
		t.Fatal("Forget must delete the persisted verdict")
	}
	tr.Triage(ctx, item)
	if calls.Load() != 2 {
		t.Fatalf("Forget must cause a fresh classification: %d", calls.Load())
	}
}

// TestSenderPlaceholderIsABareAddress: a display name carrying search
// operators must never reach a from: query.
func TestSenderPlaceholderIsABareAddress(t *testing.T) {
	r := registry(t, map[string]string{"budget.yaml": budgetYAML})
	for from, want := range map[string]string{
		`"a" OR "invoice" <x@y>`:    "from:x@y",
		"Dana Smith <dana@x.com>":   "from:dana@x.com",
		"dana@x.com":                "from:dana@x.com",
		`"x OR y" <a{b}@c.com>`:     "",
		"not an address at all":     "",
		`evil@x.com (OR subject:a)`: "from:evil@x.com",
	} {
		g := &fakeGate{answers: map[string]func(gate.Call) (gate.Result, error){"gmail.list_messages": none}}
		if _, err := (&Builder{Registry: r, Gate: g}).Build(context.Background(), msg("m1", true, from, "Budget"), Classification{TypeID: "budget"}); err != nil {
			t.Fatal(err)
		}
		got := ""
		if len(g.calls) > 0 {
			got, _ = g.calls[0].Args["query"].(string)
		}
		if got != want {
			t.Fatalf("From %q: query %q, want %q", from, got, want)
		}
	}
}

func TestStagedActionsAreValidated(t *testing.T) {
	head := "title: X\ntrigger: x\ndefault_rule: r\nseverity_weight: 1\nneeds:\n  - {name: n, fetch: gmail.list_messages, kind: lookup}\n"
	for name, sa := range map[string]string{
		"malformed id":        "[Gmail.Send]",
		"unknown connector":   "[gmial.send_message]",
		"read as action":      "[gmail.list_messages]",
		"typo in function":    "[gmail.send_mesage]",
		"no connector prefix": "[send_message]",
	} {
		t.Run(name, func(t *testing.T) {
			fsys := fstest.MapFS{"twins/t/decisions/a.yaml": {Data: []byte("id: a\n" + head + "staged_actions: " + sa + "\n")}}
			if _, err := LoadRegistry(fsys, manifest(t, testManifest)); err == nil {
				t.Fatal("loaded a bad staged action")
			}
		})
	}
	fsys := fstest.MapFS{"twins/t/decisions/a.yaml": {Data: []byte("id: a\n" + head + "staged_actions: [gmail.send_email, gmail.send_message, gmail.draft_message]\n")}}
	if _, err := LoadRegistry(fsys, manifest(t, testManifest)); err != nil {
		t.Fatalf("declared-A and planned actions must load: %v", err)
	}
}

// TestShippedStagedActionsAreDeclaredOrPlanned keeps the shipped YAMLs on
// one spelling per action.
func TestShippedStagedActionsAreDeclaredOrPlanned(t *testing.T) {
	m, err := twins.Load(water.TwinsFS(), "ceo")
	if err != nil {
		t.Fatal(err)
	}
	r, err := LoadRegistry(water.TwinsFS(), m)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range r.Index() {
		typ, _ := r.Lookup(e.ID)
		for _, a := range typ.StagedActions {
			if _, ok := m.Function(a); !ok && !plannedActions[a] {
				t.Fatalf("%s: staged action %s is neither declared nor planned", e.ID, a)
			}
		}
	}
}
