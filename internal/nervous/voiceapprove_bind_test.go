package nervous

import (
	"context"
	"sync"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/nervous/intents"
	"water/internal/nervous/render"
	"water/internal/runtime"
)

// approvalsRespondFixtureYAML is twins/ceo/intents/approvals_respond.yaml,
// copied verbatim so these fixture registries can bind a spoken yes/no
// exactly like the real ceo twin does.
const approvalsRespondFixtureYAML = `
id: approvals.respond
description: Answer yes or no to the one pending approval
function: approvals.bind_pending
requires_pending_approval: true
templates:
  - "yes"
  - "no"
  - "yeah"
  - "nope"
examples:
  - "yeah"
escalate_if: [slot_unresolved, ambiguous_match]
reflex_eligible: true
tests:
  - {utterance: "yeah", intent: approvals.respond, pending: 1}
  - {utterance: "yeah", intent: none, pending: 0}
`

const calendarCreateFixtureYAML = `
id: calendar.create_event
description: Create a calendar event and invite attendees
kind: write
action: gcal.create_event
proposer: calendar.create
slots:
  who: {type: person}
  when: {type: date, required: true}
  at: {type: time, required: true}
  dur: {type: duration, default: "30 minutes"}
  title: {type: text}
templates:
  - "schedule a meeting with {who} {when} at {at}"
escalate_if: [slot_unresolved, ambiguous_match]
reflex_eligible: true
tests:
  - {utterance: "schedule a meeting with priya tomorrow at 3:15pm", intent: calendar.create_event, slots: {when: tomorrow, at: "3:15pm", who: "priya nair <priya@x.com>"}}
  - {utterance: "gibberish nonsense", intent: "none"}
`

// queueActionSink is a real ActionSink backed by the test's own
// *approvals.Queue (env.Approvals from actionsTestEnv): it proposes exactly
// like internal/gateway's proposeEnvelope does, minus the manifest/registry
// lookups, so these tests get a real Pending()/Decide()/Edit() envelope
// lifecycle without needing a real gate.Gate or connector registry.
type queueActionSink struct {
	q    *approvals.Queue
	risk string
}

func (s *queueActionSink) ProposeAction(ctx context.Context, fn string, payload map[string]any, ch runtime.Channel) (approvals.Envelope, error) {
	return s.q.Propose(ctx, approvals.Envelope{Action: fn, Payload: payload, Origin: "p0", Risk: s.risk})
}

// fakeApprover implements nervous.Approver for these behavioral tests: it
// calls the real *approvals.Queue.Decide (never Queue.Decide directly from
// this package under test -- only from this test-only stand-in for
// internal/gateway's DecideBound), so Pending/Bound interplay across turns
// is exactly like production, and counts "executions" as a stand-in for the
// gate actually invoking a connector (these tests never build a real
// gate.Gate).
type fakeApprover struct {
	mu       sync.Mutex
	q        *approvals.Queue
	executed int
	calls    []string // "id:reply"
}

func (f *fakeApprover) DecideBound(ctx context.Context, id, payloadHash, reply string) (DecisionOutcome, error) {
	f.mu.Lock()
	f.calls = append(f.calls, id+":"+reply)
	f.mu.Unlock()

	current, err := f.q.Get(ctx, id)
	if err != nil {
		return DecisionOutcome{}, err
	}
	if payloadHash == "" || payloadHash != current.PayloadHash {
		return DecisionOutcome{}, approvals.ErrMismatch
	}
	e, err := f.q.Decide(ctx, id, approvals.Match(reply))
	if err != nil {
		if e.ID == "" {
			return DecisionOutcome{}, err
		}
		return DecisionOutcome{Status: string(e.Status), Error: err.Error()}, nil
	}
	if e.Status == approvals.Approved {
		// Stand in for the gate actually invoking the connector: Claim is
		// the same single-use consumption step a real gate.Invoke performs
		// once it runs the approved action.
		claimed, cerr := f.q.Claim(ctx, id, e.Action, e.PayloadHash)
		if cerr != nil {
			return DecisionOutcome{Status: string(e.Status), Error: cerr.Error()}, nil
		}
		f.mu.Lock()
		f.executed++
		f.mu.Unlock()
		return DecisionOutcome{Status: string(claimed.Status), Executed: true}, nil
	}
	return DecisionOutcome{Status: string(e.Status)}, nil
}

func (f *fakeApprover) executedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.executed
}

func (f *fakeApprover) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// voiceBindHarness wires a real approvals.Queue (via actionsTestEnv) to a
// Nervous with router.voice_approve on: calendar.create_event (gcal, level
// A, voice-eligible) and approvals.respond both loaded, a controllable
// clock (via env.Now), a queueActionSink and a fakeApprover both backed by
// the same real queue.
type voiceBindHarness struct {
	env      runtime.Env
	ctx      context.Context
	n        *Nervous
	sink     *queueActionSink
	approver *fakeApprover
	now      *time.Time
}

func newVoiceBindHarness(t *testing.T, risk string, internalDomains []string, enabled bool) *voiceBindHarness {
	t.Helper()
	reg := actionsFixtureRegistry(t, map[string]string{
		"calendar_create_event": calendarCreateFixtureYAML,
		"approvals_respond":     approvalsRespondFixtureYAML,
		"mail_send_reply":       mailSendReplyYAML,
	})
	env, ctx, _ := actionsTestEnv(t)
	cur := tier0FixedNow
	env.Now = func() time.Time { return cur }

	sink := &queueActionSink{q: env.Approvals, risk: risk}
	approver := &fakeApprover{q: env.Approvals}

	n, err := New(Config{
		Registry:     func() *intents.Registry { return reg },
		Style:        render.DefaultStyle(),
		Tier0Enabled: true, MainEnabled: true,
		Clock: realClock{}, AckAfter: DefaultAckAfter,
		Actions:  sink,
		Approver: approver,
		VoiceApprove: VoiceApproveConfig{
			Enabled: enabled, Window: 60 * time.Second, InternalDomains: internalDomains,
		},
		SenderWindow: 180 * 24 * time.Hour, SenderLimit: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &voiceBindHarness{env: env, ctx: ctx, n: n, sink: sink, approver: approver, now: &cur}
}

func (h *voiceBindHarness) advance(d time.Duration) { *h.now = h.now.Add(d) }

func (h *voiceBindHarness) turn(t *testing.T, text string, taskID string) []runtime.Event {
	t.Helper()
	var events []runtime.Event
	var mu sync.Mutex
	h.n.Handle(h.ctx, h.env, Turn{Channel: runtime.ChannelVoice, Text: text, TaskID: taskID}, collect(&events, &mu))
	return events
}

func hasApprovalRequired(events []runtime.Event) bool {
	for _, e := range events {
		if e.Kind == runtime.EventApprovalRequired {
			return true
		}
	}
	return false
}

// TestVoiceYesWithin60sDecides: a read-back is shown (creating an event with
// priya, an internal recipient, at low risk -- voice-eligible), then a "yes"
// within the window decides it -- exactly one decision, executed once.
func TestVoiceYesWithin60sDecides(t *testing.T) {
	h := newVoiceBindHarness(t, "low", []string{"x.com"}, true)

	h.turn(t, "schedule a meeting with priya tomorrow at 3:15pm", "t1")
	pend, err := h.env.Approvals.Pending(h.ctx)
	if err != nil || len(pend) != 1 {
		t.Fatalf("pending = %v, %v, want exactly 1", pend, err)
	}

	h.advance(30 * time.Second)
	events := h.turn(t, "yes", "t2")

	if h.approver.executedCount() != 1 {
		t.Fatalf("executed = %d, want exactly 1", h.approver.executedCount())
	}
	if h.approver.callCount() != 1 {
		t.Fatalf("DecideBound calls = %d, want exactly 1", h.approver.callCount())
	}
	if hasApprovalRequired(events) {
		t.Fatalf("events = %v, want no approval_required (already decided)", events)
	}
	latest, _ := h.env.Approvals.Get(h.ctx, pend[0].ID)
	if latest.Status != approvals.Executed {
		t.Fatalf("envelope status = %s, want executed", latest.Status)
	}
}

// TestVoiceYesAfter61sDoesNot: same setup, but the window has elapsed by the
// time "yes" arrives -- no decision, a fresh read-back is surfaced instead.
func TestVoiceYesAfter61sDoesNot(t *testing.T) {
	h := newVoiceBindHarness(t, "low", []string{"x.com"}, true)

	h.turn(t, "schedule a meeting with priya tomorrow at 3:15pm", "t1")
	pend, _ := h.env.Approvals.Pending(h.ctx)

	h.advance(61 * time.Second)
	h.turn(t, "yes", "t2")

	if h.approver.callCount() != 0 {
		t.Fatalf("DecideBound calls = %d, want 0 (window elapsed)", h.approver.callCount())
	}
	latest, _ := h.env.Approvals.Get(h.ctx, pend[0].ID)
	if latest.Status != approvals.Pending {
		t.Fatalf("envelope status = %s, want still pending", latest.Status)
	}
	// The stale reply re-surfaced a fresh binding: a THIRD "yes", right
	// away, now decides.
	h.turn(t, "yes", "t3")
	if h.approver.callCount() != 1 {
		t.Fatalf("DecideBound calls after resurface+yes = %d, want 1", h.approver.callCount())
	}
}

// TestEditVoidsBinding: the envelope is edited between the read-back and the
// reply (Queue.Edit voids the old one and proposes a new id/hash) -- a
// subsequent "yes" gets the stale-and-resurface path, no decision.
func TestEditVoidsBinding(t *testing.T) {
	h := newVoiceBindHarness(t, "low", []string{"x.com"}, true)

	h.turn(t, "schedule a meeting with priya tomorrow at 3:15pm", "t1")
	pend, _ := h.env.Approvals.Pending(h.ctx)
	edited, err := h.env.Approvals.Edit(h.ctx, pend[0].ID, map[string]any{
		"title": "Edited meeting", "start": pend[0].Payload["start"], "end": pend[0].Payload["end"], "attendees": pend[0].Payload["attendees"],
	})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}

	h.turn(t, "yes", "t2")

	if h.approver.callCount() != 0 {
		t.Fatalf("DecideBound calls = %d, want 0 (edited envelope, stale binding)", h.approver.callCount())
	}
	latest, _ := h.env.Approvals.Get(h.ctx, edited.ID)
	if latest.Status != approvals.Pending {
		t.Fatalf("edited envelope status = %s, want still pending", latest.Status)
	}
}

// TestTwoPendingGivesMenu: with 2 pending, no read-back binding is even
// consulted -- a menu is shown, no decision, both stay pending.
func TestTwoPendingGivesMenu(t *testing.T) {
	h := newVoiceBindHarness(t, "low", []string{"x.com"}, true)

	e1, err := h.env.Approvals.Propose(h.ctx, approvals.Envelope{Action: "gcal.create_event", Origin: "p0", Risk: "low", Payload: map[string]any{"title": "A"}})
	if err != nil {
		t.Fatal(err)
	}
	e2, err := h.env.Approvals.Propose(h.ctx, approvals.Envelope{Action: "gcal.create_event", Origin: "p0", Risk: "low", Payload: map[string]any{"title": "B"}})
	if err != nil {
		t.Fatal(err)
	}

	h.turn(t, "yes", "t1")

	if h.approver.callCount() != 0 {
		t.Fatalf("DecideBound calls = %d, want 0 (2 pending: never a voice decision)", h.approver.callCount())
	}
	for _, id := range []string{e1.ID, e2.ID} {
		latest, _ := h.env.Approvals.Get(h.ctx, id)
		if latest.Status != approvals.Pending {
			t.Fatalf("envelope %s status = %s, want still pending", id, latest.Status)
		}
	}
}

// TestHighRiskRequiresTap: a high-risk envelope, a not-voice-eligible action
// (gmail.send_message) and an external recipient each give tap_required
// with no decision, even though a spoken "yes" is otherwise well-formed and
// bound.
func TestHighRiskRequiresTap(t *testing.T) {
	cases := []struct {
		name    string
		action  string
		risk    string
		payload map[string]any
		domains []string
	}{
		{name: "high risk", action: "gcal.create_event", risk: "high", payload: map[string]any{"attendees": []string{"a@x.com"}}, domains: []string{"x.com"}},
		{name: "empty risk (unrated)", action: "gcal.create_event", risk: "", payload: map[string]any{"attendees": []string{"a@x.com"}}, domains: []string{"x.com"}},
		{name: "not voice-eligible action", action: "gmail.send_message", risk: "low", payload: map[string]any{"to": []string{"a@x.com"}}, domains: []string{"x.com"}},
		{name: "external recipient", action: "gcal.create_event", risk: "low", payload: map[string]any{"attendees": []string{"a@evil.example"}}, domains: []string{"x.com"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newVoiceBindHarness(t, c.risk, c.domains, true)
			env, err := h.env.Approvals.Propose(h.ctx, approvals.Envelope{Action: c.action, Origin: "p0", Risk: c.risk, Payload: c.payload})
			if err != nil {
				t.Fatal(err)
			}
			// Surface the read-back the way answerVoiceApprove's own
			// resurface path would on a first "yes" with no prior binding.
			h.turn(t, "yes", "surface")
			events := h.turn(t, "yes", "decide")

			if h.approver.callCount() != 0 {
				t.Fatalf("DecideBound calls = %d, want 0 (tap required)", h.approver.callCount())
			}
			if !hasApprovalRequired(events) {
				t.Fatalf("events = %v, want approval_required (re-surfaced for a tap)", events)
			}
			latest, _ := h.env.Approvals.Get(h.ctx, env.ID)
			if latest.Status != approvals.Pending {
				t.Fatalf("envelope status = %s, want still pending", latest.Status)
			}
		})
	}
}

// TestVoiceNoDeniesAtEveryTier: a spoken "no" denies immediately, even for
// an envelope that would require a tap for "yes" (high risk).
func TestVoiceNoDeniesAtEveryTier(t *testing.T) {
	h := newVoiceBindHarness(t, "high", []string{"x.com"}, true)
	env, err := h.env.Approvals.Propose(h.ctx, approvals.Envelope{Action: "gcal.create_event", Origin: "p0", Risk: "high", Payload: map[string]any{"attendees": []string{"a@x.com"}}})
	if err != nil {
		t.Fatal(err)
	}
	h.turn(t, "yes", "surface") // no prior binding: this just re-surfaces a fresh one

	h.turn(t, "no", "deny")

	if h.approver.callCount() != 1 {
		t.Fatalf("DecideBound calls = %d, want exactly 1 (no always decides)", h.approver.callCount())
	}
	latest, _ := h.env.Approvals.Get(h.ctx, env.ID)
	if latest.Status != approvals.Denied {
		t.Fatalf("envelope status = %s, want denied", latest.Status)
	}
}

// TestVoiceApproveFlagOffKeepsOldBehavior: with the flag off, a "yes" on
// voice against a single pending envelope never decides -- bindPendingHandler's
// pre-R-21 bind-and-surface behavior runs unchanged.
func TestVoiceApproveFlagOffKeepsOldBehavior(t *testing.T) {
	h := newVoiceBindHarness(t, "low", []string{"x.com"}, false) // enabled=false

	h.turn(t, "schedule a meeting with priya tomorrow at 3:15pm", "t1")
	pend, _ := h.env.Approvals.Pending(h.ctx)

	events := h.turn(t, "yes", "t2")

	if h.approver.callCount() != 0 {
		t.Fatalf("DecideBound calls = %d, want 0 (flag off)", h.approver.callCount())
	}
	if !hasApprovalRequired(events) {
		t.Fatalf("events = %v, want approval_required (bindPendingHandler's own ApprovalID, unchanged)", events)
	}
	latest, _ := h.env.Approvals.Get(h.ctx, pend[0].ID)
	if latest.Status != approvals.Pending {
		t.Fatalf("envelope status = %s, want still pending (flag off never decides)", latest.Status)
	}
}

// TestVoiceApproveDifferentChannelNotBound proves Readbacks are scoped per
// channel at the Nervous level too: a read-back shown on voice is not
// consulted for a decision on a non-voice channel (which never routes
// through answerVoiceApprove at all, flag or no flag).
func TestVoiceApproveDifferentChannelNotBound(t *testing.T) {
	h := newVoiceBindHarness(t, "low", []string{"x.com"}, true)
	h.turn(t, "schedule a meeting with priya tomorrow at 3:15pm", "t1")
	pend, _ := h.env.Approvals.Pending(h.ctx)

	// A "yes" on the CLI channel must never decide, even with the flag on:
	// voiceApproveActive requires the voice channel specifically.
	var events []runtime.Event
	var mu sync.Mutex
	h.n.Handle(h.ctx, h.env, Turn{Channel: runtime.ChannelCLI, Text: "yes", TaskID: "cli1"}, collect(&events, &mu))

	if h.approver.callCount() != 0 {
		t.Fatalf("DecideBound calls = %d, want 0 (cli channel never binds)", h.approver.callCount())
	}
	latest, _ := h.env.Approvals.Get(h.ctx, pend[0].ID)
	if latest.Status != approvals.Pending {
		t.Fatalf("envelope status = %s, want still pending", latest.Status)
	}
}
