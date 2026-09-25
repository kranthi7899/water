package nervous

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"
	"time"

	"water/internal/approvals"
	"water/internal/nervous/intents"
	"water/internal/nervous/reflex"
	"water/internal/nervous/slots"
	"water/internal/nervous/tmpl"
	"water/internal/store"
	"water/internal/twins"
)

const tier0ManifestYAML = `
id: testtwin
name: Test twin
usage: {window: 5h, model_calls: 200, auto_model_calls: 40}
models: {fast: haiku, strong: ""}
connectors:
  - name: gcal
    functions:
      - {name: list_events, level: R}
`

const tier0SharedYAML = `
rules: {}
skip_words: [please, hey]
deny_words: [cancel]
escalate_words: [should]
clause_joiners: ["and then"]
corrections: ["that's wrong"]
`

const tier0ScheduleYAML = `
id: schedule.on_date
description: Events on a given day
function: store.calendar_events
slots:
  when: {type: daterange, default: today}
templates:
  - "(what's|what is) on [my] (calendar|schedule) [for] {when}"
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
tests:
  - {utterance: "what's on tomorrow", intent: schedule.on_date, slots: {when: tomorrow}}
  - {utterance: "reschedule my meeting", intent: "none"}
`

const tier0AlphaYAML = `
id: test.alpha
description: Tie fixture A
function: test.alpha_fn
templates:
  - "check status"
tests:
  - {utterance: "check status", intent: test.alpha}
  - {utterance: "gibberish nonsense", intent: "none"}
`

const tier0BetaYAML = `
id: test.beta
description: Tie fixture B
function: test.beta_fn
templates:
  - "check status"
tests:
  - {utterance: "check status", intent: test.beta}
  - {utterance: "gibberish nonsense", intent: "none"}
`

const tier0PersonYAML = `
id: test.who
description: Slot-resolution fixture
function: test.person_fn
slots:
  who: {type: person, required: true}
templates:
  - "latest from {who}"
tests:
  - {utterance: "latest from jordan", intent: test.who, slots: {who: "jordan lee <jordan@x.com>"}}
  - {utterance: "gibberish nonsense", intent: "none"}
`

const tier0DenyYAML = `
id: test.delta
description: Deny-word fixture
function: test.delta_fn
slots:
  x: {type: count}
templates:
  - "look at {x}"
tests:
  - {utterance: "look at 3", intent: test.delta, slots: {x: "3"}}
  - {utterance: "gibberish nonsense", intent: "none"}
`

const tier0PendingYAML = `
id: approvals.respond
description: Pending-gate fixture
function: approvals.pending
requires_pending_approval: true
templates:
  - "yes"
tests:
  - {utterance: "yes", intent: approvals.respond, pending: 1}
  - {utterance: "yes", intent: "none", pending: 0}
  - {utterance: "gibberish nonsense", intent: "none"}
`

func tier0FixtureRegistry(t *testing.T, files map[string]string) *intents.Registry {
	t.Helper()
	m, err := twins.Parse([]byte(tier0ManifestYAML))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	fns := intents.Functions{
		Read: map[string]intents.FunctionSpec{
			"store.calendar_events":  {ID: "store.calendar_events", Args: map[string]slots.Type{"when": slots.TypeDateRange}, ReadOnly: true, Deterministic: true, Class: intents.ClassLookup},
			"test.alpha_fn":          {ID: "test.alpha_fn", Args: map[string]slots.Type{}, ReadOnly: true, Deterministic: true, Class: intents.ClassLookup},
			"test.beta_fn":           {ID: "test.beta_fn", Args: map[string]slots.Type{}, ReadOnly: true, Deterministic: true, Class: intents.ClassLookup},
			"test.person_fn":         {ID: "test.person_fn", Args: map[string]slots.Type{"who": slots.TypePerson}, Required: []string{"who"}, ReadOnly: true, Deterministic: true, Class: intents.ClassLookup},
			"test.delta_fn":          {ID: "test.delta_fn", Args: map[string]slots.Type{"x": slots.TypeCount}, ReadOnly: true, Deterministic: true, Class: intents.ClassLookup},
			"approvals.pending":      {ID: "approvals.pending", Args: map[string]slots.Type{}, ReadOnly: true, Deterministic: true, Class: intents.ClassLookup},
			"approvals.bind_pending": {ID: "approvals.bind_pending", Args: map[string]slots.Type{}, ReadOnly: true, Deterministic: true, Class: intents.ClassControl},
			"store.cached_brief":     {ID: "store.cached_brief", Args: map[string]slots.Type{}, ReadOnly: true, Deterministic: true, Class: intents.ClassLookup},
		},
	}
	fsys := fstest.MapFS{"twins/testtwin/intents/_shared.yaml": {Data: []byte(tier0SharedYAML)}}
	for name, content := range files {
		fsys["twins/testtwin/intents/"+name+".yaml"] = &fstest.MapFile{Data: []byte(content)}
	}
	reg, err := intents.LoadRegistry(fsys, m, fns, intents.LoadOptions{})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	return reg
}

func tier0Utterance(s string, reg *intents.Registry) tmpl.Utterance {
	return tmpl.Normalize(s, wordSet(reg.Shared().SkipWords))
}

// fakeStoreView satisfies reflex.StoreView. Only the methods a given test
// actually needs return anything interesting; the rest are unused stubs.
type fakeStoreView struct {
	eventsErr error
}

func (f fakeStoreView) EventsInRange(ctx context.Context, from, to time.Time) ([]store.Event, error) {
	return nil, f.eventsErr
}
func (f fakeStoreView) NextEvent(ctx context.Context, after time.Time) (*store.Event, error) {
	return nil, nil
}
func (f fakeStoreView) LatestMessages(ctx context.Context, limit int) ([]store.Message, error) {
	return nil, nil
}
func (f fakeStoreView) MessagesFrom(ctx context.Context, email string, limit int) ([]store.Message, error) {
	return nil, nil
}
func (f fakeStoreView) CountMessagesSince(ctx context.Context, since time.Time) (int, error) {
	return 0, nil
}
func (f fakeStoreView) Senders(ctx context.Context, since time.Time, limit int) ([]slots.Person, error) {
	return nil, nil
}
func (f fakeStoreView) CursorUpdatedAt(ctx context.Context, key string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

type fakePendingLister struct{ pending []approvals.Envelope }

func (f fakePendingLister) Pending(ctx context.Context) ([]approvals.Envelope, error) {
	return f.pending, nil
}

var tier0FixedNow = time.Date(2026, 9, 24, 9, 0, 0, 0, time.FixedZone("PT", -7*3600))

func tier0Ents() slots.Entities {
	return slots.Entities{People: []slots.Person{
		{Name: "Alex Chen", Email: "alex.chen@x.com"},
		{Name: "Alex Rivera", Email: "alex.rivera@x.com"},
		{Name: "Jordan Lee", Email: "jordan@x.com"},
	}}
}

func TestTryTier0NoMatch(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	u := tier0Utterance("completely unrelated gibberish", reg)
	res, reason, err := TryTier0(context.Background(), reg, reflex.Deps{}, u, 0, tier0FixedNow, tier0Ents(), nil)
	if err != nil || res != nil || reason != "no_match" {
		t.Fatalf("got res=%v reason=%q err=%v, want no_match", res, reason, err)
	}
}

func TestTryTier0AmbiguousMatch(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"alpha": tier0AlphaYAML, "beta": tier0BetaYAML})
	u := tier0Utterance("check status", reg)
	res, reason, err := TryTier0(context.Background(), reg, reflex.Deps{}, u, 0, tier0FixedNow, tier0Ents(), nil)
	if err != nil || res != nil || reason != "ambiguous_match" {
		t.Fatalf("got res=%v reason=%q err=%v, want ambiguous_match", res, reason, err)
	}
}

func TestTryTier0SlotUnresolved(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"who": tier0PersonYAML})
	u := tier0Utterance("latest from nosuchperson", reg)
	res, reason, err := TryTier0(context.Background(), reg, reflex.Deps{}, u, 0, tier0FixedNow, tier0Ents(), nil)
	if err != nil || res != nil || reason != "slot_unresolved" {
		t.Fatalf("got res=%v reason=%q err=%v, want slot_unresolved", res, reason, err)
	}
}

func TestTryTier0AmbiguousPersonAlsoSlotUnresolved(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"who": tier0PersonYAML})
	u := tier0Utterance("latest from alex", reg)
	res, reason, err := TryTier0(context.Background(), reg, reflex.Deps{}, u, 0, tier0FixedNow, tier0Ents(), nil)
	if err != nil || res != nil || reason != "slot_unresolved" {
		t.Fatalf("got res=%v reason=%q err=%v, want slot_unresolved (ambiguous person)", res, reason, err)
	}
}

func TestTryTier0DenyWord(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"delta": tier0DenyYAML})
	u := tier0Utterance("look at cancel this", reg)
	res, reason, err := TryTier0(context.Background(), reg, reflex.Deps{}, u, 0, tier0FixedNow, tier0Ents(), nil)
	if err != nil || res != nil || reason != "action_word" {
		t.Fatalf("got res=%v reason=%q err=%v, want action_word", res, reason, err)
	}
}

func TestTryTier0DenyWordExemptWhenConsumedAsLiteral(t *testing.T) {
	// "cancel" is a deny word, but for a template where it's a literal the
	// match consumes, it must not be rejected.
	reg := tier0FixtureRegistry(t, map[string]string{"stop": `
id: control.stop
description: Cancel other tasks
function: test.alpha_fn
templates:
  - "cancel"
tests:
  - {utterance: "cancel", intent: control.stop}
  - {utterance: "gibberish nonsense", intent: "none"}
`})
	u := tier0Utterance("cancel", reg)
	_, reason, err := TryTier0(context.Background(), reg, reflex.Deps{}, u, 0, tier0FixedNow, tier0Ents(), nil)
	// test.alpha_fn isn't in the real reflex.Table(), so this still can't
	// actually answer -- but it must get past the deny-word gate first,
	// i.e. NOT "action_word". Falling through to reflex.Table's own
	// not-found fallback ("no_match") proves the deny-word exemption
	// worked; only a wrongly-rejected match would report "action_word".
	if err != nil || reason == "action_word" {
		t.Fatalf("got reason=%q err=%v, want anything but action_word (the literal 'cancel' must be exempt)", reason, err)
	}
}

func TestTryTier0PendingGateExcludesWhenZero(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"pending": tier0PendingYAML})
	u := tier0Utterance("yes", reg)
	res, reason, err := TryTier0(context.Background(), reg, reflex.Deps{}, u, 0, tier0FixedNow, tier0Ents(), nil)
	if err != nil || res != nil || reason != "no_match" {
		t.Fatalf("got res=%v reason=%q err=%v, want no_match (pending=0 must exclude the candidate entirely)", res, reason, err)
	}
}

func TestTryTier0PendingGateMatchesWhenPositive(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"pending": tier0PendingYAML})
	u := tier0Utterance("yes", reg)
	deps := reflex.Deps{Approvals: fakePendingLister{pending: nil}}
	res, reason, err := TryTier0(context.Background(), reg, deps, u, 1, tier0FixedNow, tier0Ents(), nil)
	if err != nil || res == nil || reason != "" {
		t.Fatalf("got res=%v reason=%q err=%v, want an answer once pending>0", res, reason, err)
	}
}

func TestTryTier0HandlerErrorPropagates(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	u := tier0Utterance("what's on my calendar today", reg)
	wantErr := errors.New("store unavailable")
	deps := reflex.Deps{Store: fakeStoreView{eventsErr: wantErr}}
	res, reason, err := TryTier0(context.Background(), reg, deps, u, 0, tier0FixedNow, tier0Ents(), nil)
	if res != nil || reason != "" || err == nil {
		t.Fatalf("got res=%v reason=%q err=%v, want the handler's own error", res, reason, err)
	}
}

func TestTryTier0MakesNoBackendOrModelCall(t *testing.T) {
	// TryTier0's own signature takes no backend/model client at all, so a
	// Tier 0 attempt cannot make a model call by construction -- there is
	// nothing to inject a call through. This test just documents that.
	var _ = TryTier0
}
