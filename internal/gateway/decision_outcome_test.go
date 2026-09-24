package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	"water/internal/approvals"
	"water/internal/connectors/google/gapi"
	"water/internal/gate"
)

func decideYes(t *testing.T, h *harness) (string, DecisionResult) {
	t.Helper()
	out := h.invokeAsModel(t, gate.P0, gate.Clean, "slow.act", map[string]any{"what": "x"})
	id := out["approval_id"].(string)
	env, err := h.q.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := decide(t, h, context.Background(), id, env.PayloadHash)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var res DecisionResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	return id, res
}

// A send whose outcome is unknown (the request may have reached Google) must
// not be reported as "not executed": a CEO told that would re-request it and
// risk sending twice. The envelope is spent either way.
func TestApprovedSendWithUnknownOutcomeIsReportedAsUnknown(t *testing.T) {
	h := newHarness(t)
	h.slow.set(fmt.Errorf("request failed: %w", gapi.ErrSendOutcomeUnknown), nil)
	id, res := decideYes(t, h)
	if !res.OutcomeUnknown || res.Executed || res.Error == "" {
		t.Fatalf("result %+v, want OutcomeUnknown with the error", res)
	}
	if res.Envelope.Status != approvals.Executed {
		t.Fatalf("status %s, want executed (spent)", res.Envelope.Status)
	}
	env, _ := h.q.Get(context.Background(), id)
	if resp, err := decide(t, h, context.Background(), id, env.PayloadHash); err == nil {
		resp.Body.Close()
	}
	if n := h.slow.calls(); n != 1 {
		t.Fatalf("connector invoked %d times, want 1", n)
	}
}

// A definite connector failure is still "not executed", not unknown.
func TestApprovedActionDefiniteFailureIsNotUnknown(t *testing.T) {
	h := newHarness(t)
	h.slow.set(&gapi.APIError{Status: 400, Message: "bad"}, nil)
	_, res := decideYes(t, h)
	if res.OutcomeUnknown || res.Executed || res.Error == "" {
		t.Fatalf("result %+v, want a plain failure", res)
	}
}

// The action ran but indexing its result failed: it must read as executed.
func TestApprovedActionThatRanButFailedToIndexIsExecuted(t *testing.T) {
	h := newHarness(t)
	h.slow.set(nil, errors.New("disk full"))
	_, res := decideYes(t, h)
	if !res.Executed || res.Error == "" || res.OutcomeUnknown {
		t.Fatalf("result %+v, want Executed with the indexing error", res)
	}
}

// makeAuditUnwritable arms a hook that, once the action has run, makes the
// audit log read-only, so the gate's execute record is what fails.
func makeAuditUnwritable(t *testing.T, h *harness) func() {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not bind root")
	}
	t.Cleanup(func() { os.Chmod(h.log.Path(), 0o600) })
	return func() { os.Chmod(h.log.Path(), 0o400) }
}

// The action ran and only its execute audit record failed: the decision must
// read as executed with its output, never as "not executed".
func TestApprovedActionWhoseAuditRecordFailedIsExecuted(t *testing.T) {
	h := newHarness(t)
	hook := makeAuditUnwritable(t, h)
	h.slow.mu.Lock()
	h.slow.onInvoke = hook
	h.slow.mu.Unlock()
	_, res := decideYes(t, h)
	if !res.Executed || res.Output == nil || res.Error == "" || res.OutcomeUnknown {
		t.Fatalf("result %+v, want Executed with the output and the audit error", res)
	}
	if n := h.slow.calls(); n != 1 {
		t.Fatalf("connector invoked %d times, want 1", n)
	}
}

// The inline tool path: an S-level call that ran but whose audit record
// failed is not "denied".
func TestInlineToolWhoseAuditRecordFailedIsNotDenied(t *testing.T) {
	h := newHarness(t)
	h.notes.onInvoke = makeAuditUnwritable(t, h)
	out := h.invokeAsModel(t, gate.P0, gate.Clean, "notes.save_note", map[string]any{"text": "x"})
	if out["status"] != "executed_with_error" || out["output"] == nil || out["error"] == nil {
		t.Fatalf("tool result %+v, want executed_with_error with output and error", out)
	}
	if len(h.notes.saved) != 1 {
		t.Fatalf("saved %d notes, want 1", len(h.notes.saved))
	}
}
