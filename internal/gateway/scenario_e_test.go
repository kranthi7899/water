package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twinlink"
)

// TestScenarioEBudgetQuestionBetweenTwoTwins is Slice E's demo scenario, end
// to end over two real daemons with separate data directories talking over
// their real Unix sockets: the CEO's twin asks the counterparty twin a
// budget question, the counterparty's human approves a reply, and the CEO's
// twin receives it as untrusted data. At every step it checks the three
// invariants: nothing leaves either daemon without that daemon's own human
// approving the exact message; an inbound message is recorded as untrusted
// and triggers nothing (no approval, no model call, no action); and one
// request gets at most one response.
func TestScenarioEBudgetQuestionBetweenTwoTwins(t *testing.T) {
	ceo := newTwinNode(t, "ceo")
	cp := newTwinNode(t, "counterparty")
	peerWith(t, ceo, cp)
	ctx := context.Background()

	if ceo.taint(t) != gate.Clean || cp.taint(t) != gate.Clean {
		t.Fatal("both sessions must start clean")
	}

	// 1. The CEO asks for the question to be sent. It is only staged.
	question := map[string]any{
		"to_twin": "counterparty", "type": "request", "subject": "Q3 infrastructure budget",
		"payload":       "How much of the Q3 infrastructure budget is left, and can it absorb $18,000 for extra GPU capacity this quarter?",
		"reply_by":      "2026-09-30T17:00:00Z",
		"evidence_refs": []string{"gdrive:budget-fy26-q3"},
	}
	staged := ceo.stage(t, question)
	for _, want := range []string{"Send a request to twin 'counterparty'", "another party's agent", "Q3 infrastructure budget", "$18,000 for extra GPU capacity", "reply_by", "evidence_refs", "Say yes to send it to the other twin"} {
		if !strings.Contains(staged.ReadBack, want) {
			t.Fatalf("read-back missing %q:\n%s", want, staged.ReadBack)
		}
	}
	if strings.Contains(staged.ReadBack, "Send email") {
		t.Fatalf("a twin message must never read back as an email:\n%s", staged.ReadBack)
	}
	if got := cp.inbox(t, "in").Messages; len(got) != 0 {
		t.Fatalf("staging alone delivered something: %+v", got)
	}

	// The gate refuses the send without the approved envelope, however it
	// is called.
	if _, err := ceo.g.Invoke(ctx, gate.Call{Function: "twinlink.send_message", Args: question, Origin: gate.P0, Taint: gate.Clean}); !errors.Is(err, gate.ErrDenied) {
		t.Fatalf("send without an envelope: %v, want a gate denial", err)
	}
	if got := cp.inbox(t, "in").Messages; len(got) != 0 {
		t.Fatalf("an unapproved send reached the other twin: %+v", got)
	}

	// 2. The CEO approves the exact message; it is delivered, once.
	res := ceo.approve(t, staged.ApprovalID, staged.PayloadHash, "yes")
	if !res.Executed || res.Error != "" {
		t.Fatalf("approved send: %+v", res)
	}
	var sent twinlink.SendResult
	if err := json.Unmarshal(res.Output, &sent); err != nil || !sent.Delivered || !sent.Recorded || sent.ID != staged.MessageID {
		t.Fatalf("send result = %+v (%v), want delivered+recorded %s", sent, err, staged.MessageID)
	}

	// 3. The counterparty holds it as untrusted data, and nothing happened
	// because of it: no approval proposed, no model call, session tainted.
	in := cp.inbox(t, "in").Messages
	if len(in) != 1 {
		t.Fatalf("counterparty inbox = %+v, want exactly the one request", in)
	}
	req := in[0]
	if !req.Untrusted || req.FromTwin != "ceo" || req.Type != "request" || req.ID != staged.MessageID || !strings.Contains(req.Payload, "$18,000") {
		t.Fatalf("received request = %+v", req)
	}
	if req.ReplyBy == nil || !req.ReplyBy.Equal(time.Date(2026, 9, 30, 17, 0, 0, 0, time.UTC)) {
		t.Fatalf("reply_by = %v", req.ReplyBy)
	}
	if row, err := cp.st.GetTwinMessage(ctx, store.TwinInbound, req.ID); err != nil || !row.External() {
		t.Fatalf("stored inbound row = %+v, %v; want external", row, err)
	}
	if p := cp.pending(t); len(p) != 0 {
		t.Fatalf("an inbound message proposed an action on its own: %+v", p)
	}
	if n := len(cp.fake.Requests()); n != 0 {
		t.Fatalf("an inbound message caused %d model calls", n)
	}
	if cp.taint(t) != gate.Tainted {
		t.Fatal("receiving a twin message must taint the receiving session")
	}

	// The counterparty's own model can read it — through the gate, marked
	// untrusted — and anything it tries to send is only ever queued.
	tok := cp.d.stableSessionToken()
	code, body := cp.do(t, http.MethodPost, "/v1/tools/invoke", tok, map[string]any{"function": "twininbox.list_messages", "args": map[string]any{}})
	if code != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) || !strings.Contains(string(body), "untrusted") {
		t.Fatalf("model reading the inbox: %d %s", code, body)
	}
	code, body = cp.do(t, http.MethodPost, "/v1/tools/invoke", tok, map[string]any{"function": "twinlink.send_message", "args": map[string]any{
		"to_twin": "ceo", "type": "notice", "subject": "fyi", "payload": "model-initiated, must wait for approval"}})
	if code != http.StatusOK || !strings.Contains(string(body), `"status":"queued"`) {
		t.Fatalf("model-initiated send: %d %s, want queued for approval", code, body)
	}
	modelEnv := cp.pending(t)
	if len(modelEnv) != 1 {
		t.Fatalf("pending = %+v", modelEnv)
	}
	if _, err := cp.q.Decide(ctx, modelEnv[0].ID, approvals.No); err != nil {
		t.Fatal(err)
	}

	// 4. The counterparty's human stages the reply and approves it.
	reply := cp.stage(t, map[string]any{
		"to_twin": "ceo", "type": "response", "in_reply_to": req.ID, "subject": "Re: Q3 infrastructure budget",
		"payload": "$42,000 of the Q3 infrastructure budget is left. $18,000 for GPU capacity fits, leaving $24,000.",
	})
	if !strings.Contains(reply.ReadBack, "answering their request "+req.ID) {
		t.Fatalf("reply read-back: %s", reply.ReadBack)
	}
	if got := ceo.inbox(t, "in").Messages; len(got) != 0 {
		t.Fatalf("an unapproved reply reached the CEO's twin: %+v", got)
	}
	res = cp.approve(t, reply.ApprovalID, reply.PayloadHash, "yes")
	if !res.Executed || res.Error != "" {
		t.Fatalf("approved reply: %+v", res)
	}

	// 5. The CEO's twin receives it as untrusted data, and it triggers
	// nothing there either.
	got := ceo.inbox(t, "in").Messages
	if len(got) != 1 {
		t.Fatalf("CEO inbox = %+v, want exactly the one response", got)
	}
	resp := got[0]
	if !resp.Untrusted || resp.FromTwin != "counterparty" || resp.Type != "response" || resp.InReplyTo != req.ID || !strings.Contains(resp.Payload, "$42,000") {
		t.Fatalf("received response = %+v", resp)
	}
	if p := ceo.pending(t); len(p) != 0 {
		t.Fatalf("the response proposed an action on its own: %+v", p)
	}
	if n := len(ceo.fake.Requests()); n != 0 {
		t.Fatalf("the response caused %d model calls", n)
	}
	if ceo.taint(t) != gate.Tainted {
		t.Fatal("receiving the response must taint the CEO twin's session")
	}

	// 6. One request, one response: a second reply is refused before it is
	// sent, and a second response forced straight at the socket is refused
	// by the receiver.
	again := cp.stage(t, map[string]any{"to_twin": "ceo", "type": "response", "in_reply_to": req.ID, "subject": "Re: again", "payload": "second answer"})
	res = cp.approve(t, again.ApprovalID, again.PayloadHash, "yes")
	if res.Executed || !strings.Contains(res.Error, "already been answered") {
		t.Fatalf("second response: %+v, want refused as already answered", res)
	}
	forced := twinlink.Message{FromTwin: "counterparty", ToTwin: "ceo", Type: twinlink.Response, InReplyTo: req.ID,
		Subject: "Re: forced", Payload: "a second answer, bypassing the sender's own check", SentAt: time.Now()}
	forced.ID, _ = twinlink.DeriveID(forced)
	peers, _ := cp.v.Get(twinlink.VaultService, "counterparty")
	table, _ := twinlink.ParsePeers(peers.Reveal())
	if _, err := twinlink.Deliver(ctx, table["ceo"], forced); err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("forced second response: %v, want a 409 refusal", err)
	}
	if n := len(ceo.inbox(t, "in").Messages); n != 1 {
		t.Fatalf("CEO inbox holds %d messages, want still exactly 1", n)
	}

	// Every step is on both daemons' hash-chained audit logs.
	for _, n := range []*twinNode{ceo, cp} {
		if _, err := audit.Verify(filepath.Join(n.home, "audit", "audit.jsonl")); err != nil {
			t.Fatalf("%s audit: %v", n.id, err)
		}
	}
}
