package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/backend"
	"water/internal/runtime"
	"water/internal/store"
)

// invokeTool posts one model tool call to the daemon's bridge endpoint, the
// way the MCP bridge child does, and returns the approval id if it queued.
func invokeTool(t *testing.T, srv *httptest.Server, token, function string, args map[string]any) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"function": function, "args": args})
	r, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/tools/invoke", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Error(err)
		return ""
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	id, _ := out["approval_id"].(string)
	return id
}

func openSinks(d *Daemon) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.sinks)
}

// TestApprovalRequiredGoesOnlyToTheExecutingTurn: with one turn's model
// running and a second turn's stream open and waiting behind it, a tool
// call queued by the running turn is announced on that turn's stream only,
// carrying what a client needs to decide it.
func TestApprovalRequiredGoesOnlyToTheExecutingTurn(t *testing.T) {
	h := newHarness(t)
	inModel := make(chan struct{})
	queued := make(chan string, 1)
	calls := 0
	h.fake.Reply = func(req backend.Request) string {
		calls++
		if calls > 1 {
			return "second turn reply"
		}
		close(inModel)
		// Wait until the second turn's stream is open (and so waiting for
		// the model slot) before the model's tool call lands.
		deadline := time.Now().Add(5 * time.Second)
		for openSinks(h.d) < 2 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		queued <- invokeTool(t, h.srv, req.Tools.TwinToken, "fake_mail.send_email",
			map[string]any{"to": []any{"dana@acme.com"}, "subject": "Re: Hi", "body": "Confirmed."})
		return "drafted"
	}

	first := make(chan []runtime.Event, 1)
	go func() {
		first <- readEvents(t, h.post(t, "/v1/turns", `{"channel":"text-bar","prompt":"reply to dana"}`, h.token))
	}()
	<-inModel
	second := readEvents(t, h.post(t, "/v1/turns", `{"channel":"voice","prompt":"what else"}`, h.token))
	firstEvents := <-first
	queuedID := <-queued

	if queuedID == "" {
		t.Fatal("the tool call was not queued")
	}
	var got *runtime.Event
	for i, e := range firstEvents {
		if e.Kind == runtime.EventApprovalRequired {
			got = &firstEvents[i]
		}
	}
	if got == nil || got.ApprovalID != queuedID {
		t.Fatalf("executing turn's events = %+v, want approval_required(%s)", firstEvents, queuedID)
	}
	env, err := h.q.Get(context.Background(), queuedID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PayloadHash != env.PayloadHash || got.Action != "fake_mail.send_email" || got.Risk != env.Risk || got.Text != got.Action {
		t.Fatalf("approval_required = %+v, want action/risk/payload_hash of %+v", *got, env)
	}
	for _, e := range second {
		if e.Kind == runtime.EventApprovalRequired {
			t.Fatalf("the waiting turn's stream got another turn's approval: %+v", second)
		}
	}
}

// TestEmailDecisionReportDoesNotAnnounceOnAnOpenTurn: queuing a report from
// the decisions route is a plain request/response; it must not inject an
// approval_required into an unrelated turn that is streaming at the time.
func TestEmailDecisionReportDoesNotAnnounceOnAnOpenTurn(t *testing.T) {
	d, tok, q, st := newEmailTestDaemon(t)
	srv := httptest.NewServer(d.Mux())
	t.Cleanup(srv.Close)
	msg := &store.Message{
		Meta:    store.Meta{Source: "gmail", SourceID: "msg-1", External: true, CreatedAt: time.Now()},
		From:    "dana@example.com",
		Subject: "Speaking invite",
		Body:    "Could you speak at our conference? Please respond by Friday.",
	}
	if err := st.Upsert(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	cards := getDecisions(t, srv, tok)
	if len(cards) != 1 {
		t.Fatalf("got %d cards, want 1", len(cards))
	}

	fb := d.cfg.Backend.(*backend.Fake)
	classify := fb.Reply
	statusc := make(chan int, 1)
	fb.Reply = func(req backend.Request) string {
		if req.Tools == nil { // the decision classifier
			return classify(req)
		}
		body, _ := json.Marshal(map[string]any{"to": []string{"ceo@example.com"}})
		r, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/decisions/"+cards[0].ID+"/email", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Error(err)
			return "failed"
		}
		resp.Body.Close()
		statusc <- resp.StatusCode
		return "ok"
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/turns", strings.NewReader(`{"channel":"voice","prompt":"how is my day"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	events := readEvents(t, resp)
	if status := <-statusc; status != http.StatusOK {
		t.Fatalf("email route status = %d", status)
	}
	if pending, _ := q.Pending(context.Background()); len(pending) != 1 {
		t.Fatalf("pending = %d, want the report queued", len(pending))
	}
	for _, e := range events {
		if e.Kind == runtime.EventApprovalRequired {
			t.Fatalf("an unrelated turn got the decisions route's approval: %+v", events)
		}
	}
}

// TestGetDecisionsWithNoCardsIsAnEmptyArray: a configured trigger that
// builds no cards answers `[]`, never `null`.
func TestGetDecisionsWithNoCardsIsAnEmptyArray(t *testing.T) {
	d, tok, _, _ := newEmailTestDaemon(t)
	srv := httptest.NewServer(d.Mux())
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/decisions", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if got := strings.TrimSpace(string(b)); got != "[]" {
		t.Fatalf("body = %q, want []", got)
	}
}

// TestEmailDecisionReportRefusedWhenTheManifestDoesNotGrantSend: a
// registered gmail connector is not enough; a manifest that blocks or omits
// send_message must refuse before anything is queued for the CEO.
func TestEmailDecisionReportRefusedWhenTheManifestDoesNotGrantSend(t *testing.T) {
	manifests := map[string]string{
		"blocked": strings.Replace(emailTestManifest, "{name: send_message, level: A}", "{name: send_message, level: B}", 1),
		"absent":  strings.Replace(emailTestManifest, "      - {name: send_message, level: A}\n", "", 1),
	}
	for name, m := range manifests {
		t.Run(name, func(t *testing.T) {
			if m == emailTestManifest {
				t.Fatal("manifest edit did not apply")
			}
			d, tok, q, st := newEmailTestDaemonWith(t, m)
			srv := httptest.NewServer(d.Mux())
			t.Cleanup(srv.Close)
			msg := &store.Message{
				Meta:    store.Meta{Source: "gmail", SourceID: "msg-1", External: true, CreatedAt: time.Now()},
				From:    "dana@example.com",
				Subject: "Speaking invite",
				Body:    "Could you speak at our conference? Please respond by Friday.",
			}
			if err := st.Upsert(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			id := "any-card"
			if cards := getDecisions(t, srv, tok); len(cards) > 0 {
				id = cards[0].ID
			}
			body, _ := json.Marshal(map[string]any{"to": []string{"a@b.com"}})
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/decisions/"+id+"/email", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+tok)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", resp.StatusCode)
			}
			if pending, _ := q.Pending(context.Background()); len(pending) != 0 {
				t.Fatalf("pending = %d, want nothing queued", len(pending))
			}
		})
	}
}

// TestTurnChannelValidation: an unknown channel is a 400 before any stream
// starts; empty still means cli; matching ignores case.
func TestTurnChannelValidation(t *testing.T) {
	h := newHarness(t)
	h.fake.Reply = func(backend.Request) string { return "One. Two." }
	for _, ch := range []string{"text_bar", "chat", "speech"} {
		resp := h.post(t, "/v1/turns", `{"channel":"`+ch+`","prompt":"hi there"}`, h.token)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("channel %q: status = %d, want 400", ch, resp.StatusCode)
		}
	}
	if h.fake.Calls() != 0 {
		t.Fatalf("rejected channels made %d model calls", h.fake.Calls())
	}

	sentences := func(events []runtime.Event) int {
		n := 0
		for _, e := range events {
			if e.Kind == runtime.EventSentence {
				n++
			}
		}
		return n
	}
	resp := h.post(t, "/v1/turns", `{"prompt":"hi there"}`, h.token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("empty channel: status = %d, want 200", resp.StatusCode)
	}
	if n := sentences(readEvents(t, resp)); n != 0 {
		t.Fatalf("empty channel (cli) got %d sentence events", n)
	}
	resp = h.post(t, "/v1/turns", `{"channel":"Voice","prompt":"hi there"}`, h.token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("channel Voice: status = %d, want 200", resp.StatusCode)
	}
	if n := sentences(readEvents(t, resp)); n == 0 {
		t.Fatal("channel Voice got no sentence events")
	}
}

// TestDecisionLostRaceReportsTheCurrentEnvelope: a decide that errors
// because the envelope is no longer pending answers with the envelope's
// real current state, not an empty one.
func TestDecisionLostRaceReportsTheCurrentEnvelope(t *testing.T) {
	h := newHarness(t)
	env, err := h.q.Propose(context.Background(), approvals.Envelope{Action: "fake_mail.send_email",
		Payload: map[string]any{"to": []any{"a@x.com"}, "subject": "s", "body": "b"}, Origin: "p0"})
	if err != nil {
		t.Fatal(err)
	}
	post := func(reply string) DecisionResult {
		resp := h.post(t, "/v1/approvals/"+env.ID+"/decision", `{"payload_hash":"`+env.PayloadHash+`","reply":"`+reply+`"}`, h.token)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("reply %q: status = %d", reply, resp.StatusCode)
		}
		var out DecisionResult
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := post("no"); out.Envelope.Status != "denied" || out.Answer != "no" {
		t.Fatalf("first decision = %+v", out)
	}
	out := post("yes")
	if out.Error == "" || out.Envelope.ID != env.ID || out.Envelope.Status != "denied" || out.Executed {
		t.Fatalf("second decision = %+v, want the denied envelope with an error", out)
	}
}
