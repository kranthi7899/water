package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"water/internal/approvals"
	"water/internal/runtime"
)

// ---- V-ui2: approval edit re-points the staged card; decisions carry card_state ----

// decisionsRaw returns GET /v1/decisions as raw JSON objects, so a test can
// see exactly which keys each card carries.
func decisionsRaw(t *testing.T, srvURL, tok string) []map[string]json.RawMessage {
	t.Helper()
	var out []map[string]json.RawMessage
	decodeInto(t, do(t, srvURL, "GET", "/v1/decisions", "", tok), http.StatusOK, &out)
	return out
}

func cardStateOf(t *testing.T, card map[string]json.RawMessage) *cardStateView {
	t.Helper()
	raw, ok := card["card_state"]
	if !ok {
		return nil
	}
	var cs cardStateView
	if err := json.Unmarshal(raw, &cs); err != nil {
		t.Fatalf("card_state %s: %v", raw, err)
	}
	return &cs
}

// TestDecisionsListCarriesTheStagedCardState: an unstaged card has no
// card_state key at all; once staged, GET /v1/decisions says so, with the
// envelope id and its live status, so the UI shows "staged, awaiting your
// yes" on reload instead of offering to stage again. The existing Go-name
// card fields are unchanged (getDecisions still decodes a decisions.Card).
func TestDecisionsListCarriesTheStagedCardState(t *testing.T) {
	srv, tok, q, _ := newStageTestDaemon(t, decisionType("[gmail.send_message]"), "inbound")
	cards := decisionsRaw(t, srv.URL, tok)
	if len(cards) != 1 {
		t.Fatalf("cards = %d, want 1", len(cards))
	}
	if cs := cardStateOf(t, cards[0]); cs != nil {
		t.Fatalf("unstaged card carries card_state %+v", cs)
	}
	var id string
	_ = json.Unmarshal(cards[0]["ID"], &id)
	if id == "" {
		t.Fatalf("card lost its ID field: %v", cards[0])
	}

	var staged decisionStageResponse
	decodeInto(t, do(t, srv.URL, "POST", "/v1/decisions/"+id+"/stage",
		`{"payload":{"to":["dana@example.com"],"subject":"Re: Speaking invite","body":"Yes."}}`, tok), http.StatusOK, &staged)

	cs := cardStateOf(t, decisionsRaw(t, srv.URL, tok)[0])
	if cs == nil || cs.Status != "staged" || cs.ApprovalID != staged.ApprovalID || cs.ApprovalStatus != string(approvals.Pending) || cs.StagedAt.IsZero() {
		t.Fatalf("card_state = %+v, want staged on %s, pending", cs, staged.ApprovalID)
	}
	if typed := getDecisions(t, srv, tok); len(typed) != 1 || typed[0].ID != id {
		t.Fatalf("old-shape decode = %+v", typed)
	}

	// Once the envelope is answered, card_state reports that status, so the
	// UI stops saying it is waiting.
	var dr DecisionResult
	decodeInto(t, do(t, srv.URL, "POST", "/v1/approvals/"+staged.ApprovalID+"/decision",
		`{"payload_hash":"`+staged.Envelope.PayloadHash+`","reply":"no"}`, tok), http.StatusOK, &dr)
	if cs := cardStateOf(t, decisionsRaw(t, srv.URL, tok)[0]); cs == nil || cs.ApprovalStatus != string(approvals.Denied) {
		t.Fatalf("card_state after a denial = %+v, want approval_status denied", cs)
	}
	if p, _ := q.Pending(context.Background()); len(p) != 0 {
		t.Fatalf("pending = %d", len(p))
	}
}

// TestEditingAStagedEnvelopeRepointsTheCard is §7.4's acceptance check
// "editing a staged envelope leaves the card pointing at the new one": the
// edit voids the old envelope, card_states.approval_id moves to the new
// one, GET /v1/decisions shows the new id pending, and staging again hands
// back the new envelope rather than queueing a second.
func TestEditingAStagedEnvelopeRepointsTheCard(t *testing.T) {
	srv, tok, q, st := newStageTestDaemon(t, decisionType("[gmail.send_message]"), "inbound")
	ctx := context.Background()
	id := getDecisions(t, srv, tok)[0].ID
	stagePath := "/v1/decisions/" + id + "/stage"
	var staged decisionStageResponse
	decodeInto(t, do(t, srv.URL, "POST", stagePath,
		`{"payload":{"to":["dana@example.com"],"subject":"Re: Speaking invite","body":"Yes."}}`, tok), http.StatusOK, &staged)

	var edited approvalEditResponse
	decodeInto(t, do(t, srv.URL, "POST", "/v1/approvals/"+staged.ApprovalID+"/edit",
		`{"payload_hash":"`+staged.Envelope.PayloadHash+`","payload":{"to":["dana@example.com"],"subject":"Re: Speaking invite","body":"Yes, gladly."}}`, tok),
		http.StatusOK, &edited)
	newID := edited.Envelope.ID
	if newID == "" || newID == staged.ApprovalID || edited.Voided.Status != approvals.Denied {
		t.Fatalf("edit = %+v", edited)
	}

	if cs, err := st.GetCardState(ctx, id); err != nil || cs.Status != "staged" || cs.ApprovalID != newID {
		t.Fatalf("card state after edit = %+v, %v; want staged on %s", cs, err, newID)
	}
	if cs := cardStateOf(t, decisionsRaw(t, srv.URL, tok)[0]); cs == nil || cs.ApprovalID != newID || cs.ApprovalStatus != string(approvals.Pending) {
		t.Fatalf("GET /v1/decisions card_state = %+v, want the new envelope, pending", cs)
	}
	var again decisionStageResponse
	decodeInto(t, do(t, srv.URL, "POST", stagePath,
		`{"payload":{"to":["dana@example.com"],"subject":"x","body":"y"}}`, tok), http.StatusOK, &again)
	if again.Status != "already_staged" || again.ApprovalID != newID {
		t.Fatalf("restage after edit = %+v, want already_staged on %s", again, newID)
	}
	if p, _ := q.Pending(ctx); len(p) != 1 || p[0].ID != newID {
		t.Fatalf("pending = %+v, want only the edited envelope", p)
	}
}

// TestEditingAnUnstagedEnvelopeTouchesNoCard: an envelope no card was
// staged into (a model tool call's) edits exactly as before, and no
// card_state row appears.
func TestEditingAnUnstagedEnvelopeTouchesNoCard(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	env, err := h.q.Propose(ctx, approvals.Envelope{Action: "slow.act", Payload: map[string]any{"what": "old"}, Origin: "p0"})
	if err != nil {
		t.Fatal(err)
	}
	var out approvalEditResponse
	decodeInto(t, do(t, h.srv.URL, "POST", "/v1/approvals/"+env.ID+"/edit", `{"payload_hash":"`+env.PayloadHash+`","payload":{"what":"new"}}`, h.token), http.StatusOK, &out)
	if staged, err := h.st.StagedCardStates(ctx); err != nil || len(staged) != 0 {
		t.Fatalf("staged card states = %+v, %v; want none", staged, err)
	}
}

// ---- V-ui2: POST /v1/turns with thread_id (the workspace's held mic) ----

func threadMessages(t *testing.T, h *harness, id string) []threadMessageView {
	t.Helper()
	var detail threadDetail
	decodeInto(t, do(t, h.srv.URL, "GET", "/v1/threads/"+id, "", h.token), http.StatusOK, &detail)
	return detail.Messages
}

// TestTurnWithThreadIDLandsInTheThread: a voice turn posted to /v1/turns
// with a thread_id is answered by the same one turn path and both messages
// are stored in that thread as voice messages, exactly as a thread message
// post would store them; the thread's history rides along; the response
// names the thread.
func TestTurnWithThreadIDLandsInTheThread(t *testing.T) {
	h := newHarness(t)
	var th threadView
	decodeInto(t, do(t, h.srv.URL, "POST", "/v1/threads", `{"title":"voice"}`, h.token), http.StatusCreated, &th)
	readEvents(t, do(t, h.srv.URL, "POST", "/v1/threads/"+th.ID+"/messages", `{"text":"first typed question"}`, h.token))

	resp := h.post(t, "/v1/turns", `{"prompt":"what about the budget","channel":"voice","thread_id":"`+th.ID+`"}`, h.token)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Water-Thread-Id") != th.ID || resp.Header.Get("X-Water-Task-Id") == "" {
		t.Fatalf("status %d headers %v", resp.StatusCode, resp.Header)
	}
	events := readEvents(t, resp)
	if len(events) == 0 || events[0].Kind != runtime.EventAck || events[len(events)-1].Kind != runtime.EventDone {
		t.Fatalf("events = %+v", events)
	}
	done := events[len(events)-1].Text

	reqs := h.fake.Requests()
	if len(reqs) != 2 || !strings.Contains(reqs[1].Prompt, "CEO: first typed question") {
		t.Fatalf("the voice turn did not get the thread history:\n%s", reqs[len(reqs)-1].Prompt)
	}
	msgs := threadMessages(t, h, th.ID)
	if len(msgs) != 4 {
		t.Fatalf("messages = %+v, want 4", msgs)
	}
	if m := msgs[2]; m.Role != "ceo" || m.Channel != "voice" || m.Text != "what about the budget" {
		t.Fatalf("messages[2] = %+v", m)
	}
	if m := msgs[3]; m.Role != "twin" || m.Channel != "voice" || m.Text != done || m.TaskID != resp.Header.Get("X-Water-Task-Id") {
		t.Fatalf("messages[3] = %+v, want the done text", m)
	}
}

// TestTurnWithThreadIDRefusesBadThreads: an unknown thread is a 404, a
// malformed id or a thread_id combined with meeting_id is a 400, and in
// every case nothing runs and nothing is stored. A turn with no thread_id
// is unchanged and stores nothing in any thread.
func TestTurnWithThreadIDRefusesBadThreads(t *testing.T) {
	h := newHarness(t)
	var th threadView
	decodeInto(t, do(t, h.srv.URL, "POST", "/v1/threads", `{"title":"t"}`, h.token), http.StatusCreated, &th)

	cases := []struct {
		name, body string
		want       int
	}{
		{"unknown thread", `{"prompt":"hi","channel":"voice","thread_id":"thr_0123abcd"}`, http.StatusNotFound},
		{"not a thread id", `{"prompt":"hi","thread_id":"env_0123"}`, http.StatusBadRequest},
		{"uppercase hex", `{"prompt":"hi","thread_id":"thr_ABCD"}`, http.StatusBadRequest},
		{"traversal", `{"prompt":"hi","thread_id":"thr_00/../x"}`, http.StatusBadRequest},
		{"bare prefix", `{"prompt":"hi","thread_id":"thr_"}`, http.StatusBadRequest},
		{"with meeting", `{"prompt":"hi","thread_id":"` + th.ID + `","meeting_id":"ms_1"}`, http.StatusBadRequest},
		{"clear only", `{"clear":true,"thread_id":"` + th.ID + `"}`, http.StatusBadRequest},
		{"too long", `{"prompt":"` + strings.Repeat("x", maxThreadText+1) + `","thread_id":"` + th.ID + `"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		resp := h.post(t, "/v1/turns", tc.body, h.token)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s: status %d (%s), want %d", tc.name, resp.StatusCode, b, tc.want)
		}
	}
	if n := len(h.fake.Requests()); n != 0 {
		t.Fatalf("refused turns reached the model %d times", n)
	}
	if msgs := threadMessages(t, h, th.ID); len(msgs) != 0 {
		t.Fatalf("refused turns stored %+v", msgs)
	}

	resp := h.post(t, "/v1/turns", `{"prompt":"plain turn","channel":"voice"}`, h.token)
	if resp.Header.Get("X-Water-Thread-Id") != "" {
		t.Fatalf("a plain turn named a thread: %v", resp.Header)
	}
	readEvents(t, resp)
	if msgs := threadMessages(t, h, th.ID); len(msgs) != 0 {
		t.Fatalf("a turn without thread_id stored %+v in a thread", msgs)
	}
}
