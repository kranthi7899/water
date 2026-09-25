package nervous

import (
	"context"
	"errors"
	"sync"
	"testing"

	"water/internal/nervous/intents"
	"water/internal/nervous/reflex"
	"water/internal/nervous/render"
	"water/internal/nervous/t1"
	"water/internal/runtime"
)

// fakeT1Client is a t1.Client whose reply is fixed by the test, counting
// how many times Propose was actually invoked (so a test can assert Tier 1
// was never even tried for an ineligible utterance).
type fakeT1Client struct {
	mu      sync.Mutex
	calls   []t1.Call
	err     error
	invoked int
}

func (f *fakeT1Client) Propose(ctx context.Context, utterance string, decls []t1.Decl) ([]t1.Call, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invoked++
	return f.calls, f.err
}

func (f *fakeT1Client) invokedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.invoked
}

// ---- TryTier1 directly ----

func TestTryTier1AnswersGroundedCall(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	u := tier0Utterance("what's on tomorrow", reg)
	client := &fakeT1Client{calls: []t1.Call{{Intent: "schedule.on_date", Args: map[string]string{"when": "tomorrow"}}}}
	deps := reflex.Deps{Store: fakeStoreView{}}

	res, reason, err := TryTier1(context.Background(), reg, deps, client, u, tier0FixedNow, tier0Ents(), nil)
	if err != nil || res == nil || reason != "" {
		t.Fatalf("got res=%v reason=%q err=%v, want an answer", res, reason, err)
	}
	if res.Intent != "schedule.on_date" {
		t.Fatalf("Intent = %q, want schedule.on_date", res.Intent)
	}
	if client.invokedCount() != 1 {
		t.Fatalf("Propose invoked %d times, want 1", client.invokedCount())
	}
}

func TestTryTier1UngroundedValueEscalates(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	// The utterance says "thursday"; the model hallucinates "friday".
	u := tier0Utterance("what's on thursday", reg)
	client := &fakeT1Client{calls: []t1.Call{{Intent: "schedule.on_date", Args: map[string]string{"when": "friday"}}}}
	deps := reflex.Deps{Store: fakeStoreView{}}

	res, reason, err := TryTier1(context.Background(), reg, deps, client, u, tier0FixedNow, tier0Ents(), nil)
	if err != nil || res != nil || reason != "t1_ungrounded" {
		t.Fatalf("got res=%v reason=%q err=%v, want t1_ungrounded", res, reason, err)
	}
}

func TestTryTier1DefaultedArgExemptFromGrounding(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	u := tier0Utterance("what is happening", reg)
	// The model supplied no "when" argument at all; the intent's own
	// default ("today") must not be grounding-checked against the
	// utterance.
	client := &fakeT1Client{calls: []t1.Call{{Intent: "schedule.on_date", Args: map[string]string{}}}}
	deps := reflex.Deps{Store: fakeStoreView{}}

	res, reason, err := TryTier1(context.Background(), reg, deps, client, u, tier0FixedNow, tier0Ents(), nil)
	if err != nil || res == nil || reason != "" {
		t.Fatalf("got res=%v reason=%q err=%v, want an answer (default exempt from grounding)", res, reason, err)
	}
}

// tier1DeltaEligibleYAML is tier0DenyYAML plus reflex_eligible/escalate_if,
// so a Tier 1 test actually reaches TryTier1's grounding and deny-word
// checks instead of short-circuiting at the ReflexEligible gate. Its
// function (test.delta_fn) still isn't in the real reflex.Table(), so a
// case that gets past both checks lands on "t1_unknown_intent" rather than
// a real answer — enough to prove it got past them.
const tier1DeltaEligibleYAML = `
id: test.delta
description: Deny-word fixture
function: test.delta_fn
slots:
  x: {type: count}
templates:
  - "look at {x}"
escalate_if: [slot_unresolved, ambiguous_match]
reflex_eligible: true
tests:
  - {utterance: "look at 3", intent: test.delta, slots: {x: "3"}}
  - {utterance: "gibberish nonsense", intent: "none"}
`

func TestTryTier1CountGroundsOnDigitOrWord(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"delta": tier1DeltaEligibleYAML})
	u := tier0Utterance("look at 3", reg)
	client := &fakeT1Client{calls: []t1.Call{{Intent: "test.delta", Args: map[string]string{"x": "three"}}}}
	deps := reflex.Deps{}

	_, reason, err := TryTier1(context.Background(), reg, deps, client, u, tier0FixedNow, tier0Ents(), nil)
	if err != nil || reason == "t1_ungrounded" {
		t.Fatalf("got reason=%q err=%v, want anything but t1_ungrounded (3/three must be grounding-equivalent)", reason, err)
	}
}

func TestTryTier1DenyWordOutsideVocabulary(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"delta": tier1DeltaEligibleYAML})
	u := tier0Utterance("cancel look at 3", reg)
	client := &fakeT1Client{calls: []t1.Call{{Intent: "test.delta", Args: map[string]string{"x": "3"}}}}
	deps := reflex.Deps{}

	res, reason, err := TryTier1(context.Background(), reg, deps, client, u, tier0FixedNow, tier0Ents(), nil)
	if err != nil || res != nil || reason != "action_word" {
		t.Fatalf("got res=%v reason=%q err=%v, want action_word", res, reason, err)
	}
}

func TestTryTier1DenyWordExemptWhenInLiteralVocabulary(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"stop": `
id: control.stop
description: Cancel other tasks
function: test.alpha_fn
templates:
  - "cancel"
escalate_if: [slot_unresolved, ambiguous_match]
reflex_eligible: true
tests:
  - {utterance: "cancel", intent: control.stop}
  - {utterance: "gibberish nonsense", intent: "none"}
`})
	u := tier0Utterance("cancel", reg)
	client := &fakeT1Client{calls: []t1.Call{{Intent: "control.stop", Args: map[string]string{}}}}
	deps := reflex.Deps{}

	_, reason, err := TryTier1(context.Background(), reg, deps, client, u, tier0FixedNow, tier0Ents(), nil)
	// test.alpha_fn isn't in the real reflex.Table(), so this still can't
	// actually answer -- but "cancel" is control.stop's own literal, so it
	// must get past the deny-word gate (not "action_word"); it lands on
	// t1_unknown_intent instead (no such reflex handler), proving it got
	// past both the ReflexEligible gate and the deny-word check first.
	if err != nil || reason == "action_word" {
		t.Fatalf("got reason=%q err=%v, want anything but action_word", reason, err)
	}
	if reason != "t1_unknown_intent" {
		t.Fatalf("got reason=%q, want t1_unknown_intent (proves the deny-word check was actually reached and passed)", reason)
	}
}

func TestTryTier1ZeroCalls(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	u := tier0Utterance("completely unrelated gibberish", reg)
	client := &fakeT1Client{calls: nil}

	res, reason, err := TryTier1(context.Background(), reg, reflex.Deps{}, client, u, tier0FixedNow, tier0Ents(), nil)
	if err != nil || res != nil || reason != "t1_no_call" {
		t.Fatalf("got res=%v reason=%q err=%v, want t1_no_call", res, reason, err)
	}
}

func TestTryTier1MultipleCalls(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	u := tier0Utterance("what's on tomorrow and also mail", reg)
	client := &fakeT1Client{calls: []t1.Call{
		{Intent: "schedule.on_date", Args: map[string]string{"when": "tomorrow"}},
		{Intent: "schedule.on_date", Args: map[string]string{"when": "tomorrow"}},
	}}

	res, reason, err := TryTier1(context.Background(), reg, reflex.Deps{}, client, u, tier0FixedNow, tier0Ents(), nil)
	if err != nil || res != nil || reason != "t1_multi_call" {
		t.Fatalf("got res=%v reason=%q err=%v, want t1_multi_call", res, reason, err)
	}
}

func TestTryTier1TextReply(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	u := tier0Utterance("why is the board sync on friday", reg)
	client := &fakeT1Client{err: t1.ErrTextReply}

	res, reason, err := TryTier1(context.Background(), reg, reflex.Deps{}, client, u, tier0FixedNow, tier0Ents(), nil)
	if err != nil || res != nil || reason != "t1_text" {
		t.Fatalf("got res=%v reason=%q err=%v, want t1_text", res, reason, err)
	}
}

func TestTryTier1UnknownIntentName(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	u := tier0Utterance("do something odd", reg)
	client := &fakeT1Client{calls: []t1.Call{{Intent: "no_such_intent", Args: nil}}}

	res, reason, err := TryTier1(context.Background(), reg, reflex.Deps{}, client, u, tier0FixedNow, tier0Ents(), nil)
	if err != nil || res != nil || reason != "t1_unknown_intent" {
		t.Fatalf("got res=%v reason=%q err=%v, want t1_unknown_intent", res, reason, err)
	}
}

func TestTryTier1RequiresPendingIntentNeverAnswered(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"pending": tier0PendingYAML})
	u := tier0Utterance("yes", reg)
	// A misbehaving/fake client naming approvals.respond directly, even
	// though Declarations never offers it.
	client := &fakeT1Client{calls: []t1.Call{{Intent: "approvals.respond", Args: nil}}}

	res, reason, err := TryTier1(context.Background(), reg, reflex.Deps{}, client, u, tier0FixedNow, tier0Ents(), nil)
	if err != nil || res != nil || reason != "t1_unknown_intent" {
		t.Fatalf("got res=%v reason=%q err=%v, want t1_unknown_intent", res, reason, err)
	}
}

func TestTryTier1TransportErrorPropagates(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	u := tier0Utterance("what's on tomorrow", reg)
	wantErr := errors.New("sidecar unreachable")
	client := &fakeT1Client{err: wantErr}

	res, reason, err := TryTier1(context.Background(), reg, reflex.Deps{}, client, u, tier0FixedNow, tier0Ents(), nil)
	if res != nil || reason != "" || err == nil {
		t.Fatalf("got res=%v reason=%q err=%v, want the transport's own error", res, reason, err)
	}
}

func TestTryTier1HandlerErrorPropagates(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	u := tier0Utterance("what's on tomorrow", reg)
	client := &fakeT1Client{calls: []t1.Call{{Intent: "schedule.on_date", Args: map[string]string{"when": "tomorrow"}}}}
	wantErr := errors.New("store unavailable")
	deps := reflex.Deps{Store: fakeStoreView{eventsErr: wantErr}}

	res, reason, err := TryTier1(context.Background(), reg, deps, client, u, tier0FixedNow, tier0Ents(), nil)
	if res != nil || reason != "" || err == nil {
		t.Fatalf("got res=%v reason=%q err=%v, want the handler's own error", res, reason, err)
	}
}

// ---- through Handle ----

func nervousForTier1(t *testing.T, reg *intents.Registry, client t1.Client) *Nervous {
	t.Helper()
	n, err := New(Config{
		Registry:     func() *intents.Registry { return reg },
		Style:        render.DefaultStyle(),
		Tier0Enabled: true,
		Tier1Enabled: true,
		T1:           client,
		MainEnabled:  true,
		Clock:        realClock{},
		AckAfter:     DefaultAckAfter,
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestHandleTier1AnswersOnTier0NoMatch(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	env, ctx, fk := nervousTestEnv(t)
	client := &fakeT1Client{calls: []t1.Call{{Intent: "schedule.on_date", Args: map[string]string{"when": "tomorrow"}}}}
	n := nervousForTier1(t, reg, client)

	var events []runtime.Event
	var mu sync.Mutex
	// tier0ScheduleYAML's own templates require "calendar"/"schedule" in
	// the utterance; this phrasing cleanly misses Tier 0 (no_match) so
	// Tier 1 is actually tried.
	n.Handle(ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "what's happening tomorrow", TaskID: "t1"}, collect(&events, &mu))

	if fk.Calls() != 0 {
		t.Fatalf("backend calls = %d, want 0 (Tier 1 must never call the model backend)", fk.Calls())
	}
	if client.invokedCount() != 1 {
		t.Fatalf("Propose invoked %d times, want 1", client.invokedCount())
	}
	ks := kinds(events)
	if len(ks) < 2 || ks[0] != runtime.EventAck || ks[len(ks)-1] != runtime.EventDone {
		t.Fatalf("events = %v, want ack ... done", ks)
	}
}

func TestHandleTier1NeverTriedOnEligibilityEscalation(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	env, ctx, _ := nervousTestEnv(t)
	// A client that would ALWAYS answer, no matter the utterance -- proving
	// Tier 1 is never even invoked for a reasoning utterance is a call-site
	// property (Eligible rejects it before either quick tier runs), not
	// something Tier 1 itself has to re-derive.
	client := &fakeT1Client{calls: []t1.Call{{Intent: "schedule.on_date", Args: map[string]string{"when": "today"}}}}
	n := nervousForTier1(t, reg, client)

	var events []runtime.Event
	var mu sync.Mutex
	n.Handle(ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "what should I prioritize tomorrow", TaskID: "t2"}, collect(&events, &mu))

	if client.invokedCount() != 0 {
		t.Fatalf("Propose invoked %d times, want 0 (escalate_word must skip every quick tier)", client.invokedCount())
	}
}

func TestHandleTier1NeverTriedWhenTier0AlreadyAnswered(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	env, ctx, _ := nervousTestEnv(t)
	client := &fakeT1Client{calls: []t1.Call{{Intent: "schedule.on_date", Args: map[string]string{"when": "today"}}}}
	n := nervousForTier1(t, reg, client)

	var events []runtime.Event
	var mu sync.Mutex
	n.Handle(ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "what's on my calendar today", TaskID: "t3"}, collect(&events, &mu))

	if client.invokedCount() != 0 {
		t.Fatalf("Propose invoked %d times, want 0 (Tier 0 already answered)", client.invokedCount())
	}
}

func TestHandleTier1NeverTriedOnTier0DenyWord(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"delta": tier0DenyYAML})
	env, ctx, _ := nervousTestEnv(t)
	client := &fakeT1Client{calls: []t1.Call{{Intent: "test.delta", Args: map[string]string{"x": "3"}}}}
	n := nervousForTier1(t, reg, client)

	var events []runtime.Event
	var mu sync.Mutex
	n.Handle(ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "look at cancel this", TaskID: "t4"}, collect(&events, &mu))

	if client.invokedCount() != 0 {
		t.Fatalf("Propose invoked %d times, want 0 (Tier 0's action_word must not fall through to Tier 1)", client.invokedCount())
	}
}
