package t1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testDecls() []Decl {
	return []Decl{
		{Name: "schedule_on_date", Description: "Events on a given day", Params: map[string]ParamDecl{
			"when": {Type: "string", Description: "daterange", Required: false},
		}, intentID: "schedule.on_date"},
		{Name: "mail_latest", Description: "The most recent email(s)", Params: map[string]ParamDecl{
			"n": {Type: "string", Description: "count", Required: false},
		}, intentID: "mail.latest"},
	}
}

// fakeServer stands in for llama-server's OpenAI-compatible
// /v1/chat/completions endpoint, replying with whatever raw JSON body the
// test configures regardless of the request it receives.
func fakeServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("request body did not parse as JSON: %v", err)
		}
		if req["temperature"] != float64(0) {
			t.Errorf("temperature = %v, want 0", req["temperature"])
		}
		if req["max_tokens"] != float64(128) {
			t.Errorf("max_tokens = %v, want 128", req["max_tokens"])
		}
		if req["tool_choice"] != "auto" {
			t.Errorf("tool_choice = %v, want auto", req["tool_choice"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
}

func newTestClient(t *testing.T, srv *httptest.Server) Client {
	t.Helper()
	c, err := NewHTTP(srv.URL)
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}
	return c
}

func TestProposeSingleToolCall(t *testing.T) {
	srv := fakeServer(t, http.StatusOK, `{
		"choices": [{"message": {"content": "", "tool_calls": [
			{"function": {"name": "schedule_on_date", "arguments": "{\"when\":\"tomorrow\"}"}}
		]}}]
	}`)
	defer srv.Close()

	c := newTestClient(t, srv)
	calls, err := c.Propose(context.Background(), "what's on tomorrow", testDecls())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %+v, want exactly 1", calls)
	}
	if calls[0].Intent != "schedule.on_date" {
		t.Fatalf("Intent = %q, want schedule.on_date", calls[0].Intent)
	}
	if calls[0].Args["when"] != "tomorrow" {
		t.Fatalf("Args = %+v, want when=tomorrow", calls[0].Args)
	}
}

func TestProposeNumericArgumentStringified(t *testing.T) {
	srv := fakeServer(t, http.StatusOK, `{
		"choices": [{"message": {"content": "", "tool_calls": [
			{"function": {"name": "mail_latest", "arguments": "{\"n\":3}"}}
		]}}]
	}`)
	defer srv.Close()

	c := newTestClient(t, srv)
	calls, err := c.Propose(context.Background(), "show me my last 3 emails", testDecls())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(calls) != 1 || calls[0].Args["n"] != "3" {
		t.Fatalf("calls = %+v, want a stringified n=3", calls)
	}
}

func TestProposeZeroToolCallsNoText(t *testing.T) {
	srv := fakeServer(t, http.StatusOK, `{"choices": [{"message": {"content": "", "tool_calls": []}}]}`)
	defer srv.Close()

	c := newTestClient(t, srv)
	calls, err := c.Propose(context.Background(), "completely unrelated gibberish", testDecls())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("calls = %+v, want none", calls)
	}
}

func TestProposeTextReply(t *testing.T) {
	srv := fakeServer(t, http.StatusOK, `{"choices": [{"message": {"content": "I'm not sure what you mean.", "tool_calls": []}}]}`)
	defer srv.Close()

	c := newTestClient(t, srv)
	calls, err := c.Propose(context.Background(), "why is the board sync on friday", testDecls())
	if !errors.Is(err, ErrTextReply) {
		t.Fatalf("err = %v, want ErrTextReply", err)
	}
	if len(calls) != 0 {
		t.Fatalf("calls = %+v, want none alongside ErrTextReply", calls)
	}
}

func TestProposeMultipleToolCalls(t *testing.T) {
	srv := fakeServer(t, http.StatusOK, `{
		"choices": [{"message": {"content": "", "tool_calls": [
			{"function": {"name": "schedule_on_date", "arguments": "{\"when\":\"tomorrow\"}"}},
			{"function": {"name": "mail_latest", "arguments": "{}"}}
		]}}]
	}`)
	defer srv.Close()

	c := newTestClient(t, srv)
	calls, err := c.Propose(context.Background(), "what's on tomorrow and show me my mail", testDecls())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %+v, want 2", calls)
	}
}

func TestProposeUnknownToolName(t *testing.T) {
	srv := fakeServer(t, http.StatusOK, `{
		"choices": [{"message": {"content": "", "tool_calls": [
			{"function": {"name": "totally_unknown_tool", "arguments": "{}"}}
		]}}]
	}`)
	defer srv.Close()

	c := newTestClient(t, srv)
	calls, err := c.Propose(context.Background(), "do something weird", testDecls())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %+v, want 1", calls)
	}
	// Not translatable to a real dotted intent id, so it can never
	// accidentally collide with one; the adapter's own registry lookup is
	// what turns this into "unknown intent".
	if strings.Contains(calls[0].Intent, ".") {
		t.Fatalf("Intent = %q, want the raw unconverted wire name (no dot)", calls[0].Intent)
	}
}

func TestNewHTTPRejectsNonLoopback(t *testing.T) {
	if _, err := NewHTTP("http://example.com:1234"); err == nil {
		t.Fatal("NewHTTP accepted a non-loopback endpoint")
	}
}

func TestNewHTTPAcceptsLoopbackForms(t *testing.T) {
	for _, ep := range []string{"http://127.0.0.1:8080", "http://localhost:8080", "http://[::1]:8080"} {
		if _, err := NewHTTP(ep); err != nil {
			t.Errorf("NewHTTP(%q) = %v, want no error", ep, err)
		}
	}
}

func TestNewHTTPNeverRequestsForRejectedEndpoint(t *testing.T) {
	requested := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
	}))
	defer srv.Close()

	// A non-loopback URL that happens to share this server's port would
	// still be rejected at construction: NewHTTP inspects the host, not
	// whether it happens to be reachable.
	if _, err := NewHTTP("http://example.com" + srv.URL[strings.LastIndex(srv.URL, ":"):]); err == nil {
		t.Fatal("NewHTTP accepted a non-loopback host")
	}
	if requested {
		t.Fatal("NewHTTP made a request before/without a Propose call")
	}
}

func TestProposeHTTPErrorStatus(t *testing.T) {
	srv := fakeServer(t, http.StatusInternalServerError, `{"error":"boom"}`)
	defer srv.Close()

	c := newTestClient(t, srv)
	if _, err := c.Propose(context.Background(), "anything", testDecls()); err == nil {
		t.Fatal("Propose succeeded against a 500 response")
	}
}
