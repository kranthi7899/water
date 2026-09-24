package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/backend"
	"water/internal/connectors/fake"
	"water/internal/gate"
	"water/internal/runtime"
	"water/internal/store"
)

// TestEscalateTaintBeforeTheSessionTokenExists: on a fresh daemon the first
// turn escalates taint before anything has minted the session token. That
// escalation must not be lost: the token, once minted, must be Tainted.
func TestEscalateTaintBeforeTheSessionTokenExists(t *testing.T) {
	h := newHarness(t)
	h.d.escalateTaint(true)
	pol := h.d.TwinToolPolicy()
	ta, ok := h.d.lookupTurnToken(pol.TwinToken)
	if !ok || ta.Taint != gate.Tainted {
		t.Fatalf("session token after a first-turn escalation = %+v ok=%v, want Tainted", ta, ok)
	}
}

// TestFirstTurnWithExternalContextTaintsTheSession drives the same case
// through POST /v1/turns: today's calendar holds an External event, so the
// very first turn's tool calls must carry Tainted.
func TestFirstTurnWithExternalContextTaintsTheSession(t *testing.T) {
	h := newHarness(t)
	ev := &store.Event{Meta: store.Meta{Source: "gcal", SourceID: "ev-ext", External: true}, Title: "Invite from outside", StartAt: time.Now()}
	if err := h.st.Upsert(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	var seen string
	h.fake.Reply = func(req backend.Request) string {
		if req.Tools != nil {
			seen = req.Tools.TwinToken
		}
		return "ok"
	}
	readEvents(t, h.post(t, "/v1/turns", `{"channel":"cli","prompt":"tell me something"}`, h.token))
	if seen == "" {
		t.Fatal("the turn carried no tool token")
	}
	ta, ok := h.d.lookupTurnToken(seen)
	if !ok || ta.Taint != gate.Tainted {
		t.Fatalf("first turn's tool token = %+v ok=%v, want Tainted", ta, ok)
	}
}

// TestClearOnlyRequestRunsNoModelTurn: /clear posts an empty prompt with
// clear=true; that must reset the session and end the stream, never send a
// blank user message to the model.
func TestClearOnlyRequestRunsNoModelTurn(t *testing.T) {
	h := newHarness(t)
	resp := h.post(t, "/v1/turns", `{"channel":"cli","prompt":"","clear":true}`, h.token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	events := readEvents(t, resp)
	if len(events) == 0 || events[len(events)-1].Kind != runtime.EventDone {
		t.Fatalf("events = %+v, want a stream ending in done", events)
	}
	if h.fake.Calls() != 0 {
		t.Fatalf("clear-only request made %d model calls, want 0", h.fake.Calls())
	}

	empty := h.post(t, "/v1/turns", `{"channel":"cli","prompt":"   "}`, h.token)
	empty.Body.Close()
	if empty.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty prompt without clear: status = %d, want 400", empty.StatusCode)
	}
	if h.fake.Calls() != 0 {
		t.Fatalf("empty prompt made %d model calls, want 0", h.fake.Calls())
	}
}

// TestQueuedToolCallDuringATurnEmitsApprovalRequired: a tool call the model
// makes mid-turn that is queued for approval must appear on that turn's
// stream as approval_required, before done — not depend on the model's
// prose mentioning it.
func TestQueuedToolCallDuringATurnEmitsApprovalRequired(t *testing.T) {
	h := newHarness(t)
	var queuedID string
	h.fake.Reply = func(req backend.Request) string {
		body, _ := json.Marshal(map[string]any{"function": "fake_mail.send_email",
			"args": map[string]any{"to": []any{"dana@acme.com"}, "subject": "Re: Hi", "body": "Confirmed."}})
		r, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/v1/tools/invoke", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+req.Tools.TwinToken)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Error(err)
			return "failed"
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		queuedID, _ = out["approval_id"].(string)
		return "I've drafted that for you."
	}
	events := readEvents(t, h.post(t, "/v1/turns", `{"channel":"cli","prompt":"reply to dana confirming"}`, h.token))
	if queuedID == "" {
		t.Fatal("the tool call was not queued")
	}
	approvalIdx, doneIdx := -1, -1
	for i, e := range events {
		switch e.Kind {
		case runtime.EventApprovalRequired:
			if e.ApprovalID == queuedID {
				approvalIdx = i
			}
		case runtime.EventDone:
			doneIdx = i
		}
	}
	if approvalIdx == -1 || doneIdx == -1 || approvalIdx > doneIdx {
		t.Fatalf("events = %+v, want approval_required(%s) before done", events, queuedID)
	}
}

func decide(t *testing.T, h *harness, ctx context.Context, id, hash string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.srv.URL+"/v1/approvals/"+id+"/decision",
		strings.NewReader(`{"payload_hash":"`+hash+`","reply":"yes"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	return http.DefaultClient.Do(req)
}

// TestApprovalRefusedBeforeClaimIsNotStrandedApproved: when the gate
// refuses an approved envelope before claiming it (here: its credential is
// gone), the envelope must end in a clear final state, not sit Approved
// where nothing can ever execute it.
func TestApprovalRefusedBeforeClaimIsNotStrandedApproved(t *testing.T) {
	h := newHarness(t)
	out := h.invokeAsModel(t, gate.P0, gate.Clean, "fake_mail.send_email",
		map[string]any{"to": []any{"dana@acme.com"}, "subject": "Re: Hi", "body": "Confirmed."})
	id := out["approval_id"].(string)
	env, err := h.q.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.vault.Delete(fake.MailService, fake.MailAccount); err != nil {
		t.Fatal(err)
	}
	resp, err := decide(t, h, context.Background(), id, env.PayloadHash)
	if err != nil {
		t.Fatal(err)
	}
	var result DecisionResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if result.Executed || result.Error == "" {
		t.Fatalf("result = %+v, want a reported execution failure", result)
	}
	got, _ := h.q.Get(context.Background(), id)
	if got.Status == approvals.Approved {
		t.Fatalf("envelope stranded in %s after a refused execution", got.Status)
	}
	if got.Status != approvals.Denied {
		t.Fatalf("envelope status = %s, want denied", got.Status)
	}
}

// TestApprovedExecutionSurvivesClientDisconnect: once the gate has claimed
// an approved envelope, the client going away (Ctrl-C, a timeout) must not
// cancel the connector call partway — the envelope is already single-use.
func TestApprovedExecutionSurvivesClientDisconnect(t *testing.T) {
	h := newHarness(t)
	out := h.invokeAsModel(t, gate.P0, gate.Clean, "slow.act", map[string]any{"what": "x"})
	id := out["approval_id"].(string)
	env, err := h.q.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if resp, err := decide(t, h, ctx, id, env.PayloadHash); err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-h.slow.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("connector never ran")
	}
	cancel()
	time.Sleep(300 * time.Millisecond) // let the server notice the disconnect
	close(h.slow.release)
	select {
	case cerr := <-h.slow.finished:
		if cerr != nil {
			t.Fatalf("connector call was cancelled by the client disconnect: %v", cerr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("connector never finished")
	}
}
