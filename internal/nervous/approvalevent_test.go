package nervous

import (
	"sync"
	"testing"

	"water/internal/approvals"
	"water/internal/runtime"
)

// approvalEvent returns the one approval_required event in events.
func approvalEvent(t *testing.T, events []runtime.Event) runtime.Event {
	t.Helper()
	var got []runtime.Event
	for _, e := range events {
		if e.Kind == runtime.EventApprovalRequired {
			got = append(got, e)
		}
	}
	if len(got) != 1 {
		t.Fatalf("events = %+v, want exactly one approval_required", events)
	}
	return got[0]
}

// checkComplete asserts e carries everything a client needs to show and
// decide env without a second request (V-events).
func checkComplete(t *testing.T, e runtime.Event, env approvals.Envelope) {
	t.Helper()
	if e.ApprovalID != env.ID || e.Action != env.Action || e.Risk != env.Risk || e.PayloadHash != env.PayloadHash ||
		e.PayloadHash == "" || e.ReadBack != approvals.ReadBack(env) || e.ReadBack == "" {
		t.Fatalf("approval_required = %+v, want the complete event for %+v (read_back %q)", e, env, approvals.ReadBack(env))
	}
}

// TestTier0WriteIntentApprovalRequiredIsComplete: a Tier-0 write intent
// that stages an envelope (no model, no tool call) announces it with
// action, risk, payload_hash and the code-built read-back, exactly like the
// model-queued path does.
func TestTier0WriteIntentApprovalRequiredIsComplete(t *testing.T) {
	for _, ch := range []runtime.Channel{runtime.ChannelVoice, runtime.ChannelTextBar} {
		t.Run(string(ch), func(t *testing.T) {
			h := newVoiceBindHarness(t, "medium", []string{"x.com"}, false)
			var events []runtime.Event
			h.n.Handle(h.ctx, h.env, Turn{Channel: ch, Text: "schedule a meeting with priya tomorrow at 3:15pm", TaskID: "w1"},
				collect(&events, new(sync.Mutex)))
			pend, err := h.env.Approvals.Pending(h.ctx)
			if err != nil || len(pend) != 1 {
				t.Fatalf("pending = %v, %v, want exactly 1", pend, err)
			}
			checkComplete(t, approvalEvent(t, events), pend[0])
		})
	}
}

// TestBindPendingApprovalRequiredIsComplete: a bare "yes" with voice
// approval off surfaces the one pending envelope (bindPendingHandler's
// ApprovalID); that event is complete too.
func TestBindPendingApprovalRequiredIsComplete(t *testing.T) {
	h := newVoiceBindHarness(t, "low", []string{"x.com"}, false)
	h.turn(t, "schedule a meeting with priya tomorrow at 3:15pm", "t1")
	pend, _ := h.env.Approvals.Pending(h.ctx)
	if len(pend) != 1 {
		t.Fatalf("pending = %d, want 1", len(pend))
	}
	checkComplete(t, approvalEvent(t, h.turn(t, "yes", "t2")), pend[0])
}

// TestTapRequiredApprovalRequiredIsComplete: a spoken yes on a
// tap-required envelope re-emits approval_required so a client can show
// its tap affordance; it now carries the read-back and hash the tap needs.
func TestTapRequiredApprovalRequiredIsComplete(t *testing.T) {
	h := newVoiceBindHarness(t, "high", []string{"x.com"}, true)
	env, err := h.env.Approvals.Propose(h.ctx, approvals.Envelope{Action: "gmail.send_message", Origin: "p0", Risk: "high",
		Payload: map[string]any{"to": []string{"a@x.com"}, "subject": "Hi", "body": "Confirmed."}})
	if err != nil {
		t.Fatal(err)
	}
	h.turn(t, "yes", "surface")
	events := h.turn(t, "yes", "decide")
	if h.approver.callCount() != 0 {
		t.Fatalf("DecideBound calls = %d, want 0 (tap required)", h.approver.callCount())
	}
	checkComplete(t, approvalEvent(t, events), env)
}

// TestApprovalRequiredLookupFailureStaysBare: when the envelope can't be
// read back (here the action sink hands back an id the queue never stored),
// the event still goes out with the id alone, as before.
func TestApprovalRequiredLookupFailureStaysBare(t *testing.T) {
	reg := actionsFixtureRegistry(t, map[string]string{"mail_send_reply": mailSendReplyYAML})
	env, ctx, _ := actionsTestEnv(t)
	sink := &fakeActionSink{envelope: approvals.Envelope{ID: "env_missing", Action: "gmail.send_message", Status: approvals.Pending}}
	n := nervousForActions(t, reg, sink)
	var events []runtime.Event
	n.Handle(ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "send priya a reply saying ok see you then", TaskID: "a1"},
		collect(&events, new(sync.Mutex)))
	e := approvalEvent(t, events)
	if e.ApprovalID != "env_missing" || e.ReadBack != "" || e.PayloadHash != "" {
		t.Fatalf("approval_required = %+v, want the bare id", e)
	}
}
