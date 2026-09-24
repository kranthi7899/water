package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
