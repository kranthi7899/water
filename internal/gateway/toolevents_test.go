package gateway

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"water/internal/gate"
	"water/internal/runtime"
)

// stepRecorder stands in for one open POST /v1/turns stream: it is
// registered as a turn's sink and made the active (model-running) turn, the
// same state beginModel leaves while the model's tool calls arrive.
type stepRecorder struct {
	mu     sync.Mutex
	events []runtime.Event
}

func (r *stepRecorder) write(e runtime.Event) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

func (r *stepRecorder) all() []runtime.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]runtime.Event(nil), r.events...)
}

// activeSink registers a recording sink for task id and makes it the
// active turn. The returned sink can be closed to simulate a finished
// stream.
func activeSink(h *harness, id string) (*stepRecorder, *turnSink) {
	rec := &stepRecorder{}
	s := &turnSink{write: rec.write, channel: runtime.ChannelTextBar}
	h.d.registerSink(id, s)
	h.d.mu.Lock()
	h.d.activeTask = id
	h.d.mu.Unlock()
	return rec, s
}

func stepEvents(events []runtime.Event) []runtime.Event {
	var out []runtime.Event
	for _, e := range events {
		if e.Kind == runtime.EventToolStart || e.Kind == runtime.EventToolEnd {
			out = append(out, e)
		}
	}
	return out
}

// checkPair asserts exactly one tool_start then one tool_end for the same
// step, with the given tool, label and end status.
func checkPair(t *testing.T, events []runtime.Event, tool, label string, status runtime.StepStatus) {
	t.Helper()
	steps := stepEvents(events)
	if len(steps) != 2 || steps[0].Kind != runtime.EventToolStart || steps[1].Kind != runtime.EventToolEnd {
		t.Fatalf("step events = %+v, want exactly tool_start then tool_end", steps)
	}
	start, end := steps[0], steps[1]
	if !strings.HasPrefix(start.StepID, "stp_") || start.StepID != end.StepID {
		t.Fatalf("step ids = %q / %q, want one shared stp_ id", start.StepID, end.StepID)
	}
	if start.Status != "" {
		t.Fatalf("tool_start carries a status %q", start.Status)
	}
	for _, e := range steps {
		if e.Tool != tool || e.Label != label {
			t.Fatalf("%s: tool/label = %q/%q, want %q/%q", e.Kind, e.Tool, e.Label, tool, label)
		}
	}
	if end.Status != status {
		t.Fatalf("tool_end status = %q, want %q", end.Status, status)
	}
}

// TestToolInvokeEmitsStepPairForEachStatus: every outcome of a model tool
// call ends its step on the active turn's stream, with a label built from
// the connector's spec and never from the call's arguments.
func TestToolInvokeEmitsStepPairForEachStatus(t *testing.T) {
	const secret = "PRIVATE-ARGUMENT-TEXT"
	cases := []struct {
		name     string
		function string
		args     map[string]any
		setup    func(t *testing.T, h *harness)
		tool     string
		label    string
		status   runtime.StepStatus
	}{
		{name: "ok", function: "fake_mail.list_messages",
			tool: "fake_mail.list_messages", label: "Searching your email", status: runtime.StepOK},
		{name: "queued", function: "fake_mail.send_email",
			args: map[string]any{"to": []any{"dana@acme.com"}, "subject": secret, "body": secret},
			tool: "fake_mail.send_email", label: "Send an email.", status: runtime.StepQueued},
		{name: "denied unknown function", function: "nope.fn", args: map[string]any{"x": secret},
			tool: "", label: genericStepLabel, status: runtime.StepDenied},
		{name: "denied quick id on the tools route", function: "quick.calendar",
			tool: "", label: genericStepLabel, status: runtime.StepDenied},
		{name: "error: ran, audit failed", function: "notes.save_note", args: map[string]any{"text": secret},
			setup: func(t *testing.T, h *harness) { h.notes.onInvoke = makeAuditUnwritable(t, h) },
			tool:  "notes.save_note", label: "notes.save_note", status: runtime.StepError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			if c.setup != nil {
				c.setup(t, h)
			}
			rec, _ := activeSink(h, "task_a")
			h.invokeAsModel(t, gate.P0, gate.Clean, c.function, c.args)
			events := rec.all()
			checkPair(t, events, c.tool, c.label, c.status)
			for _, e := range stepEvents(events) {
				if strings.Contains(e.Label, secret) || strings.Contains(e.Tool, secret) {
					t.Fatalf("step event carries argument text: %+v", e)
				}
			}
			if c.status == runtime.StepQueued {
				// The approval is announced between the step's start and end.
				if len(events) != 3 || events[1].Kind != runtime.EventApprovalRequired {
					t.Fatalf("events = %+v, want tool_start, approval_required, tool_end", events)
				}
			}
		})
	}
}

// TestToolInvokeNoStepWithoutAnOpenActiveTurn: with no active turn, with
// the active turn's stream already closed, and for a request the daemon
// rejects before it knows the call (bad token, bad body), nothing is
// emitted anywhere, and the call itself behaves exactly as before.
func TestToolInvokeNoStepWithoutAnOpenActiveTurn(t *testing.T) {
	h := newHarness(t)
	// A sink that is open but not the active turn (a turn still waiting
	// for the model slot) must not receive another turn's steps.
	idle := &stepRecorder{}
	h.d.registerSink("task_waiting", &turnSink{write: idle.write})
	if out := h.invokeAsModel(t, gate.P0, gate.Clean, "fake_mail.list_messages", nil); out["status"] != "ok" {
		t.Fatalf("out = %+v, want ok", out)
	}
	if got := idle.all(); len(got) != 0 {
		t.Fatalf("a non-active stream got %+v", got)
	}

	rec, s := activeSink(h, "task_closed")
	s.close()
	h.invokeAsModel(t, gate.P0, gate.Clean, "fake_mail.list_messages", nil)
	if got := rec.all(); len(got) != 0 {
		t.Fatalf("a closed stream got %+v", got)
	}

	rec, _ = activeSink(h, "task_open")
	resp := h.post(t, "/v1/tools/invoke", `{"function":"fake_mail.list_messages"}`, "not-a-token")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: status = %d", resp.StatusCode)
	}
	resp = h.post(t, "/v1/tools/invoke", `{not json`, h.d.mintTurnToken(gate.P0, gate.Clean, 0))
	resp.Body.Close()
	resp = h.post(t, "/v1/quick/invoke", `{"function":"quick.next_event"}`, "not-a-token")
	resp.Body.Close()
	if got := rec.all(); len(got) != 0 {
		t.Fatalf("rejected requests emitted %+v", got)
	}
}

// TestQuickInvokeEmitsStepPair: a quick.* call is a step too, labelled from
// the quick tools' own table; an unknown quick id ends as denied with the
// generic label and no tool name.
func TestQuickInvokeEmitsStepPair(t *testing.T) {
	h := newRoutingHarness(t)
	rec, _ := activeSink(h, "task_q")
	if out := h.invokeQuick(t, gate.P0, gate.Clean, "quick.next_event", map[string]any{}); out["status"] != "ok" {
		t.Fatalf("out = %+v", out)
	}
	checkPair(t, rec.all(), "quick.next_event", "Checking your next meeting", runtime.StepOK)

	rec, _ = activeSink(h, "task_q2")
	h.invokeQuick(t, gate.P0, gate.Clean, "quick.does_not_exist", map[string]any{})
	checkPair(t, rec.all(), "", genericStepLabel, runtime.StepDenied)
}
