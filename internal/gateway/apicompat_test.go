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
	if got.PayloadHash != env.PayloadHash || got.Action != "fake_mail.send_email" || got.Risk != env.Risk || got.Text != got.Action ||
		got.ReadBack != approvals.ReadBack(env) {
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

// TestApprovalsWireShape pins the approvals API a client builds against:
// snake_case keys on GET /v1/approvals, GET /v1/approvals/{id} and the
// decision result's envelope (the same payload_hash key the decide body
// takes), and a read_back that is exactly approvals.ReadBack, so no client
// or model has to compose what the CEO hears.
func TestApprovalsWireShape(t *testing.T) {
	h := newHarness(t)
	env, err := h.q.Propose(context.Background(), approvals.Envelope{Action: "fake_mail.send_email",
		Payload: map[string]any{"to": []any{"a@x.com"}, "subject": "s", "body": "b"}, Origin: "p0", Risk: "high"})
	if err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"id", "action", "recipient", "payload", "evidence_refs", "risk", "origin", "expires_at",
		"payload_hash", "status", "reason", "created_at", "read_back", "summary"}
	check := func(where string, obj map[string]any) {
		t.Helper()
		for _, k := range wantKeys {
			if _, ok := obj[k]; !ok {
				t.Fatalf("%s: missing key %q in %v", where, k, obj)
			}
		}
		for _, k := range []string{"ID", "PayloadHash", "Envelope"} {
			if _, ok := obj[k]; ok {
				t.Fatalf("%s: PascalCase key %q in %v", where, k, obj)
			}
		}
		if obj["id"] != env.ID || obj["payload_hash"] != env.PayloadHash {
			t.Fatalf("%s: id/payload_hash = %v/%v", where, obj["id"], obj["payload_hash"])
		}
		if obj["read_back"] != approvals.ReadBack(env) {
			t.Fatalf("%s: read_back = %q, want %q", where, obj["read_back"], approvals.ReadBack(env))
		}
		if obj["summary"] != approvals.Summary(env) {
			t.Fatalf("%s: summary = %q", where, obj["summary"])
		}
	}
	decode := func(resp *http.Response, v any) {
		t.Helper()
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}

	var list []map[string]any
	decode(h.get(t, "/v1/approvals", h.token), &list)
	if len(list) != 1 {
		t.Fatalf("list = %v", list)
	}
	check("list", list[0])

	var one map[string]any
	decode(h.get(t, "/v1/approvals/"+env.ID, h.token), &one)
	check("get", one)

	if resp := h.get(t, "/v1/approvals/env_missing", h.token); resp.StatusCode != http.StatusNotFound {
		resp.Body.Close()
		t.Fatalf("unknown id: status %d, want 404", resp.StatusCode)
	} else {
		resp.Body.Close()
	}

	var res map[string]any
	decode(h.post(t, "/v1/approvals/"+env.ID+"/decision", `{"payload_hash":"`+env.PayloadHash+`","reply":"no"}`, h.token), &res)
	got, _ := res["envelope"].(map[string]any)
	if got == nil || got["status"] != "denied" || got["id"] != env.ID || got["read_back"] != approvals.ReadBack(env) {
		t.Fatalf("decision envelope = %v", res["envelope"])
	}
}

// TestMainPathToolCallStreamsOneStepPair (V-events acceptance): a
// main-path turn whose model calls one tool streams exactly one
// tool_start/tool_end pair, in order, before done; the call's status is
// the tool result's own.
func TestMainPathToolCallStreamsOneStepPair(t *testing.T) {
	h := newHarness(t)
	h.fake.Reply = func(req backend.Request) string {
		body, _ := json.Marshal(map[string]any{"function": "fake_mail.list_messages", "args": map[string]any{}})
		r, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/v1/tools/invoke", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+req.Tools.TwinToken)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Error(err)
			return "failed"
		}
		resp.Body.Close()
		return "You have one message from Dana."
	}
	events := readEvents(t, h.post(t, "/v1/turns", `{"channel":"text-bar","prompt":"anything from dana?"}`, h.token))
	var order []runtime.EventKind
	for _, e := range events {
		switch e.Kind {
		case runtime.EventToolStart, runtime.EventToolEnd, runtime.EventDone:
			order = append(order, e.Kind)
		}
	}
	want := []runtime.EventKind{runtime.EventToolStart, runtime.EventToolEnd, runtime.EventDone}
	if len(order) != len(want) || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
		t.Fatalf("event order = %v, want %v (all events %+v)", order, want, events)
	}
	checkPair(t, events, "fake_mail.list_messages", "Searching your email", runtime.StepOK)
}

// TestTier0TurnStreamsNoSteps (V-events acceptance): a Tier-0 answer calls
// no tool and no model, so its stream has no tool_start/tool_end.
func TestTier0TurnStreamsNoSteps(t *testing.T) {
	h := newRoutingHarness(t)
	events := readEvents(t, h.post(t, "/v1/turns", `{"channel":"voice","prompt":"status"}`, h.token))
	if h.fake.Calls() != 0 {
		t.Fatalf("provider calls = %d, want a Tier-0 answer", h.fake.Calls())
	}
	if steps := stepEvents(events); len(steps) != 0 {
		t.Fatalf("Tier-0 turn streamed steps: %+v", steps)
	}
}

// TestTurnSendsStyleBlockAndChannelHint (D5 acceptance): the system prompt
// a main-path turn sends carries style.yaml's prompt block, and the turn's
// own message carries its channel hint with that channel's max_chars.
func TestTurnSendsStyleBlockAndChannelHint(t *testing.T) {
	h := newHarness(t)
	h.d.cfg.StyleBlock = "Lead with the answer. No filler."
	h.d.cfg.MaxChars = map[runtime.Channel]int{runtime.ChannelVoice: 280, runtime.ChannelTextBar: 600, runtime.ChannelCLI: 2000}
	h.fake.Reply = func(backend.Request) string { return "Fine." }
	readEvents(t, h.post(t, "/v1/turns", `{"channel":"voice","prompt":"how is my day"}`, h.token))
	reqs := h.fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	if !strings.Contains(reqs[0].System, h.d.cfg.StyleBlock) || reqs[0].System != h.d.SystemPrompt() {
		t.Fatalf("system prompt = %q, want SystemPrompt() with the style block", reqs[0].System)
	}
	if !strings.Contains(reqs[0].Prompt, "## Channel: voice (reply in at most about 280 characters") {
		t.Fatalf("turn prompt lacks the voice hint:\n%s", reqs[0].Prompt)
	}
}

// TestOldClientsStillParseNewEvents: the new kinds and fields decode into
// the pre-V-events event shape (a client that knows only kind/text/
// approval_id) without error. (The Go CLI's switches and the web UI's
// already fall through on an unknown kind.)
func TestOldClientsStillParseNewEvents(t *testing.T) {
	type oldEvent struct {
		Kind       string `json:"kind"`
		Text       string `json:"text,omitempty"`
		ApprovalID string `json:"approval_id,omitempty"`
		Error      string `json:"error,omitempty"`
	}
	for _, e := range []runtime.Event{
		{Kind: runtime.EventToolStart, StepID: "stp_1", Tool: "gcal.list_events", Label: "Checking your calendar"},
		{Kind: runtime.EventToolEnd, StepID: "stp_1", Tool: "gcal.list_events", Label: "Checking your calendar", Status: runtime.StepQueued},
		runtime.ApprovalRequiredEvent(approvals.Envelope{ID: "env_1", Action: "gcal.create_event", Risk: "medium", PayloadHash: "h"}),
		{Kind: runtime.EventArtifact, StepID: "stp_2", Tool: "gmail.draft_message", Artifact: &runtime.Artifact{
			Type: runtime.ArtifactEmailDraft, To: []string{"dana@acme.com"}, Cc: []string{"sam@acme.com"}, Subject: "Hi", Body: "Hello"}},
	} {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		var old oldEvent
		if err := json.Unmarshal(b, &old); err != nil || old.Kind != string(e.Kind) {
			t.Fatalf("old client decode of %s = %+v, %v", b, old, err)
		}
	}
}

// TestArtifactEventWireShape pins the artifact event a client builds
// against: exactly kind, step_id, tool and artifact{type,to,cc,subject,
// body}, and none of the other event fields.
func TestArtifactEventWireShape(t *testing.T) {
	e := runtime.Event{Kind: runtime.EventArtifact, StepID: "stp_1", Tool: "gmail.draft_message", Artifact: &runtime.Artifact{
		Type: runtime.ArtifactEmailDraft, To: []string{"dana@acme.com"}, Cc: []string{"sam@acme.com"}, Subject: "Hi", Body: "Hello"}}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"kind":"artifact","step_id":"stp_1","tool":"gmail.draft_message","artifact":{"type":"email_draft","to":["dana@acme.com"],"cc":["sam@acme.com"],"subject":"Hi","body":"Hello"}}`
	if string(b) != want {
		t.Fatalf("wire = %s\nwant   %s", b, want)
	}
}

// TestNoteArtifactEventWireShape pins the note artifact event's wire shape
// (docs/slices/UI.md Phase 6, U1-A): kind, step_id, tool and artifact{type,
// title, body, source, simulated}, with Simulated surviving the Go -> JSON
// round trip as a real JSON boolean, not a string or a dropped field, so the
// Swift decoder's "(simulated)" badge is driven by exactly what the daemon
// sent.
func TestNoteArtifactEventWireShape(t *testing.T) {
	e := runtime.Event{Kind: runtime.EventArtifact, StepID: "stp_1", Tool: "linear.create_comment", Artifact: &runtime.Artifact{
		Type: runtime.ArtifactNote, Title: "CRA-3", Body: "(simulated) Relayed: Nina, via Slack: it's fixed", Source: "linear:CRA-3", Simulated: true}}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"kind":"artifact","step_id":"stp_1","tool":"linear.create_comment","artifact":{"type":"note","title":"CRA-3","body":"(simulated) Relayed: Nina, via Slack: it's fixed","source":"linear:CRA-3","simulated":true}}`
	if string(b) != want {
		t.Fatalf("wire = %s\nwant   %s", b, want)
	}
	// Round trip: unmarshal what was just marshaled, and Simulated must come
	// back true, not silently dropped or coerced.
	var back runtime.Event
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Artifact == nil || !back.Artifact.Simulated || back.Artifact.Type != runtime.ArtifactNote {
		t.Fatalf("round trip = %+v", back.Artifact)
	}
	// A non-simulated note omits "simulated" entirely (omitempty): the field
	// is absent, not false-as-a-string or any other stray shape, and an old
	// or lenient decoder that never heard of "simulated" still gets a
	// perfectly valid note.
	e2 := runtime.Event{Kind: runtime.EventArtifact, StepID: "stp_2", Tool: "linear.create_comment", Artifact: &runtime.Artifact{
		Type: runtime.ArtifactNote, Title: "CRA-3", Body: "Nina: fixed for real", Source: "linear:CRA-3"}}
	b2, err := json.Marshal(e2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b2), "simulated") {
		t.Fatalf("wire = %s, want no \"simulated\" key when false (omitempty)", b2)
	}
}

// TestMainPathDraftStreamsArtifactInsideItsStep: a main-path turn whose
// model drafts an email streams tool_start, artifact, tool_end (one shared
// step id) before done, the artifact carrying the call's own arguments.
func TestMainPathDraftStreamsArtifactInsideItsStep(t *testing.T) {
	h, _ := newDraftHarness(t)
	h.fake.Reply = func(req backend.Request) string {
		body, _ := json.Marshal(map[string]any{"function": "gmail.draft_message",
			"args": map[string]any{"to": []any{"dana@acme.com"}, "subject": "Q3", "body": "Numbers attached."}})
		r, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/v1/tools/invoke", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+req.Tools.TwinToken)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Error(err)
			return "failed"
		}
		resp.Body.Close()
		return "Drafted it."
	}
	events := readEvents(t, h.post(t, "/v1/turns", `{"channel":"voice","prompt":"draft dana a note about q3"}`, h.token))
	var order []runtime.EventKind
	var art runtime.Event
	for _, e := range events {
		switch e.Kind {
		case runtime.EventToolStart, runtime.EventArtifact, runtime.EventToolEnd, runtime.EventDone:
			order = append(order, e.Kind)
		}
		if e.Kind == runtime.EventArtifact {
			art = e
		}
	}
	want := []runtime.EventKind{runtime.EventToolStart, runtime.EventArtifact, runtime.EventToolEnd, runtime.EventDone}
	if len(order) != len(want) {
		t.Fatalf("event order = %v, want %v (all %+v)", order, want, events)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("event order = %v, want %v (all %+v)", order, want, events)
		}
	}
	steps := stepEvents(events)
	if art.StepID == "" || art.StepID != steps[0].StepID || art.Tool != "gmail.draft_message" {
		t.Fatalf("artifact %+v does not belong to step %+v", art, steps[0])
	}
	a := art.Artifact
	if a == nil || a.Type != runtime.ArtifactEmailDraft || len(a.To) != 1 || a.To[0] != "dana@acme.com" ||
		a.Subject != "Q3" || a.Body != "Numbers attached." || len(a.Cc) != 0 {
		t.Fatalf("artifact = %+v", a)
	}
}

// TestApprovalRequiredWarningsWireShape (docs/slices/W.md §4.1): the new
// approval_required fields, warnings and confirm_phrase, are omitted when
// empty, so every existing approval_required is byte-identical to before,
// and an older client decodes an event that carries them without error.
func TestApprovalRequiredWarningsWireShape(t *testing.T) {
	env := approvals.Envelope{ID: "env_1", Action: "gcal.create_event", Risk: "medium", PayloadHash: "h"}
	b, err := json.Marshal(runtime.ApprovalRequiredEvent(env))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"kind":"approval_required","text":"gcal.create_event","approval_id":"env_1","action":"gcal.create_event","risk":"medium","payload_hash":"h","read_back":` +
		string(mustJSON(t, approvals.ReadBack(env))) + `}`
	if string(b) != want {
		t.Fatalf("wire = %s\nwant   %s", b, want)
	}

	env = approvals.Envelope{ID: "env_2", Action: "gmail.send_message", Risk: "high", PayloadHash: "h2",
		Payload:  map[string]any{"to": []any{"dana@no-mail.io"}, "subject": "Hi", "body": "b"},
		Warnings: []string{"no-mail.io has no mail server; the message would bounce"}}
	e := runtime.ApprovalRequiredEvent(env)
	e.ConfirmPhrase = "confirm send"
	b, err = json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"warnings":["no-mail.io has no mail server; the message would bounce"],"confirm_phrase":"confirm send"`) {
		t.Fatalf("wire = %s", b)
	}
	var old struct {
		Kind       string `json:"kind"`
		ApprovalID string `json:"approval_id"`
		ReadBack   string `json:"read_back"`
	}
	if err := json.Unmarshal(b, &old); err != nil || old.Kind != "approval_required" || old.ApprovalID != "env_2" ||
		!strings.Contains(old.ReadBack, "Warning: no-mail.io has no mail server") {
		t.Fatalf("old client decode = %+v, %v", old, err)
	}
	// An event from before these fields decodes into today's Event.
	var cur runtime.Event
	if err := json.Unmarshal([]byte(`{"kind":"approval_required","approval_id":"env_3","payload_hash":"x"}`), &cur); err != nil ||
		cur.Warnings != nil || cur.ConfirmPhrase != "" {
		t.Fatalf("decode of an old event = %+v, %v", cur, err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
