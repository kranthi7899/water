package gateway

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"

	"water/internal/approvals"
	"water/internal/gate"
	"water/internal/runtime"
)

// mxResolver is a tiny approvals.MXResolver: names in mx have a mail
// server; everything else is not found.
type mxResolver struct{ mx map[string]bool }

func (r mxResolver) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if r.mx[name] {
		return []*net.MX{{Host: "mx." + name + "."}}, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (r mxResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// The 2026-09-26 incident replayed through the real tool bridge: the model
// queues gmail.send_message to kranthetjob@therightgmail.com. It is now
// denied with a message telling the model to confirm the address; no
// envelope is proposed and no approval_required is announced.
func TestModelQueuedSendToNearMissIsDenied(t *testing.T) {
	h, _ := newDraftHarness(t)
	rec, _ := activeSink(h, "task_a")
	out := h.invokeAsModel(t, gate.P0, gate.Clean, "gmail.send_message",
		map[string]any{"to": []any{"kranthetjob@therightgmail.com"}, "subject": "Job search", "body": "Hi"})
	reason, _ := out["reason"].(string)
	if out["status"] != "denied" || !strings.Contains(reason, "gmail.com") || !strings.Contains(reason, "confirm") {
		t.Fatalf("out = %+v, want denied with a confirm-the-address reason", out)
	}
	pend, err := h.q.Pending(context.Background())
	if err != nil || len(pend) != 0 {
		t.Fatalf("pending = %+v, %v; want none", pend, err)
	}
	for _, e := range rec.all() {
		if e.Kind == runtime.EventApprovalRequired {
			t.Fatalf("approval_required announced for a refused recipient: %+v", e)
		}
	}
}

// A send to a domain with no mail server is queued, but its
// approval_required event and GET /v1/approvals/{id} both carry the
// warning, and the read-back says it before the question.
func TestNoMXWarningReachesEventAndApprovalView(t *testing.T) {
	h, _ := newDraftHarness(t)
	h.q.SetRecipientChecker(approvals.NewRecipientChecker(mxResolver{mx: map[string]bool{"fenwick.io": true}}, nil))
	rec, _ := activeSink(h, "task_a")
	out := h.invokeAsModel(t, gate.P0, gate.Clean, "gmail.send_message",
		map[string]any{"to": []any{"dana@no-mail-here.io"}, "subject": "Hi", "body": "b"})
	if out["status"] != "queued" {
		t.Fatalf("out = %+v, want queued", out)
	}
	id, _ := out["approval_id"].(string)
	const want = "no-mail-here.io has no mail server; the message would bounce"

	var ev *runtime.Event
	for _, e := range rec.all() {
		if e.Kind == runtime.EventApprovalRequired {
			e := e
			ev = &e
		}
	}
	if ev == nil || ev.ApprovalID != id || !slices.Equal(ev.Warnings, []string{want}) ||
		!strings.Contains(ev.ReadBack, "Warning: "+want+".") {
		t.Fatalf("approval_required = %+v, want warnings [%q] and the warning in read_back", ev, want)
	}

	resp := h.get(t, "/v1/approvals/"+id, h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", resp.StatusCode)
	}
	var view map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	ws, _ := view["warnings"].([]any)
	if len(ws) != 1 || ws[0] != want || !strings.Contains(view["read_back"].(string), want) {
		t.Fatalf("GET /v1/approvals/%s = %v, want warnings [%q]", id, view, want)
	}

	// A clean send to a domain with a mail server carries no warnings key.
	out = h.invokeAsModel(t, gate.P0, gate.Clean, "gmail.send_message",
		map[string]any{"to": []any{"dana@fenwick.io"}, "subject": "Hi", "body": "b"})
	resp2 := h.get(t, "/v1/approvals/"+out["approval_id"].(string), h.token)
	defer resp2.Body.Close()
	var clean map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&clean); err != nil {
		t.Fatal(err)
	}
	if _, ok := clean["warnings"]; ok {
		t.Fatalf("clean envelope has a warnings key: %v", clean)
	}
}

// Every attempted call, including one that is only queued or refused,
// counts toward the in-flight main turn (docs/slices/W.md D6): a turn
// whose only call was a send must never be classed "general".
func TestToolInvokeRecordsAttemptsEvenWhenQueuedOrDenied(t *testing.T) {
	h, _ := newDraftHarness(t)
	tracer := h.d.cfg.Nervous.ToolTracer()
	tracer.BeginMain("turn-1")
	h.invokeAsModel(t, gate.P0, gate.Clean, "gmail.send_message",
		map[string]any{"to": []any{"dana@fenwick.io"}, "subject": "Hi", "body": "b"})
	h.invokeAsModel(t, gate.P0, gate.Clean, "gmail.send_message",
		map[string]any{"to": []any{"kranthetjob@therightgmail.com"}, "subject": "Hi", "body": "b"})
	used, attributed := tracer.EndMain("turn-1")
	if !attributed || len(used) != 0 {
		t.Fatalf("used = %v, attributed = %v; want nothing executed, attribution clean", used, attributed)
	}
	if got := tracer.TakeAttempts("turn-1"); !slices.Equal(got, []string{"gmail.send_message", "gmail.send_message"}) {
		t.Fatalf("attempts = %v, want both sends", got)
	}
}
