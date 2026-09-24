package gapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPostJSONSucceeds(t *testing.T) {
	ts := newTokenServer(t)
	var got map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/gmail/v1/users/me/messages/send" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("content-type %q", ct)
		}
		var body map[string]any
		if err := readJSON(r, &body); err != nil {
			t.Fatal(err)
		}
		got = body
		fmt.Fprint(w, `{"id":"m1"}`)
	}))
	defer api.Close()
	c := newClient(t, ts, api, nil)
	var out struct{ ID string }
	err := c.PostJSON(context.Background(), GmailBase+"/users/me/messages/send", nil, map[string]any{"raw": "abc"}, &out)
	if err != nil || out.ID != "m1" {
		t.Fatalf("err %v out %+v", err, out)
	}
	if got["raw"] != "abc" {
		t.Fatalf("body not sent: %v", got)
	}
}

// A 5xx that arrives after the server fully received the request body is an
// ambiguous outcome: PostJSON must not retry it and must return a
// distinctly-typed error the caller can check for.
func TestPostJSONAmbiguousServerErrorAfterBodySent(t *testing.T) {
	ts := newTokenServer(t)
	var calls atomic.Int32
	var bodySeen atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if err := readJSON(r, &body); err == nil && body["to"] == "dana@example.com" {
			bodySeen.Store(true)
		}
		w.WriteHeader(500)
		fmt.Fprint(w, `{"error":{"code":500,"message":"Backend Error"}}`)
	}))
	defer api.Close()
	c := newClient(t, ts, api, nil)
	err := c.PostJSON(context.Background(), GmailBase+"/users/me/messages/send", nil, map[string]any{"to": "dana@example.com"}, nil)
	if !bodySeen.Load() {
		t.Fatal("test bug: server never saw the body")
	}
	if !errors.Is(err, ErrSendOutcomeUnknown) {
		t.Fatalf("got %v, want ErrSendOutcomeUnknown", err)
	}
	if Status(err) != 500 {
		t.Fatalf("Status(err) = %d, want 500 (APIError still reachable via errors.As)", Status(err))
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want exactly 1 (no blind retry on a send)", calls.Load())
	}
}

// A clean 4xx (Google refusing the call before doing anything) is a
// definite, safe-to-report failure: it must not be ErrSendOutcomeUnknown.
func TestPostJSONDefiniteFailureBeforeProcessing(t *testing.T) {
	ts := newTokenServer(t)
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":{"code":400,"message":"Invalid To header"}}`)
	}))
	defer api.Close()
	c := newClient(t, ts, api, nil)
	err := c.PostJSON(context.Background(), GmailBase+"/users/me/messages/send", nil, map[string]any{"to": "not-an-address"}, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if errors.Is(err, ErrSendOutcomeUnknown) {
		t.Fatalf("a clean 400 must not be ErrSendOutcomeUnknown: %v", err)
	}
	if Status(err) != 400 {
		t.Fatalf("Status(err) = %d, want 400", Status(err))
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want exactly 1", calls.Load())
	}

	// A plain 403 is the same story.
	calls.Store(0)
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(403)
		fmt.Fprint(w, `{"error":{"code":403,"message":"Insufficient Permission"}}`)
	}))
	defer forbidden.Close()
	c = newClient(t, ts, forbidden, nil)
	err = c.PostJSON(context.Background(), GmailBase+"/users/me/messages/send", nil, map[string]any{"to": "dana@example.com"}, nil)
	if errors.Is(err, ErrSendOutcomeUnknown) || Status(err) != 403 || calls.Load() != 1 {
		t.Fatalf("err %v calls %d", err, calls.Load())
	}
}

// A network failure (no response at all) is exactly the case
// ErrSendOutcomeUnknown exists for: the request may or may not have landed.
func TestPostJSONNetworkErrorIsAmbiguous(t *testing.T) {
	ts := newTokenServer(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	c := newClient(t, ts, dead, nil)
	err := c.PostJSON(context.Background(), GmailBase+"/users/me/messages/send?secret="+url.QueryEscape(testSecret), nil, map[string]any{"to": "dana@example.com"}, nil)
	if !errors.Is(err, ErrSendOutcomeUnknown) {
		t.Fatalf("got %v, want ErrSendOutcomeUnknown", err)
	}
	assertClean(t, err, "tok1")
}

// A 401 is a pre-send auth failure: still safe to refresh and retry once,
// exactly like GetJSON's existing behavior.
func TestPostJSONUnauthorizedRefreshesOnceAndRetries(t *testing.T) {
	ts := newTokenServer(t)
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") == "Bearer ya29.tok1" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"code":401,"message":"Invalid Credentials"}}`)
			return
		}
		fmt.Fprint(w, `{"id":"m1"}`)
	}))
	defer api.Close()
	c := newClient(t, ts, api, nil)
	var out struct{ ID string }
	err := c.PostJSON(context.Background(), GmailBase+"/users/me/messages/send", nil, map[string]any{"to": "dana@example.com"}, &out)
	if err != nil || out.ID != "m1" {
		t.Fatalf("err %v out %+v", err, out)
	}
	if ts.n.Load() != 2 || calls.Load() != 2 {
		t.Fatalf("refreshes %d calls %d", ts.n.Load(), calls.Load())
	}

	// A 401 that persists after the refresh is a definite failure, not
	// looped on and not ErrSendOutcomeUnknown.
	ts2 := newTokenServer(t)
	calls.Store(0)
	always := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":{"code":401,"message":"bad token %s"}}`, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	}))
	defer always.Close()
	c = newClient(t, ts2, always, nil)
	err = c.PostJSON(context.Background(), GmailBase+"/users/me/messages/send", nil, map[string]any{"to": "dana@example.com"}, nil)
	if Status(err) != 401 || errors.Is(err, ErrSendOutcomeUnknown) || ts2.n.Load() != 2 || calls.Load() != 2 {
		t.Fatalf("err %v refreshes %d calls %d", err, ts2.n.Load(), calls.Load())
	}
	assertClean(t, err, "tok1", "tok2")
}

// Extends the existing secret-scrub coverage to every PostJSON error path.
func TestPostJSONErrorsNeverCarrySecrets(t *testing.T) {
	ts := newTokenServer(t)

	api400 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		fmt.Fprintf(w, `{"error":{"code":400,"message":"bad %s / %s / %s"}}`, tok, testRefresh, testSecret)
	}))
	defer api400.Close()
	c := newClient(t, ts, api400, nil)
	err := c.PostJSON(context.Background(), GmailBase+"/users/me/messages/send", nil, map[string]any{"to": "dana@example.com"}, nil)
	assertClean(t, err, "tok1")

	api500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		fmt.Fprintf(w, `{"error":{"code":500,"message":"oops %s"}}`, tok)
	}))
	defer api500.Close()
	c = newClient(t, ts, api500, nil)
	err = c.PostJSON(context.Background(), GmailBase+"/users/me/messages/send", nil, map[string]any{"to": "dana@example.com"}, nil)
	if !errors.Is(err, ErrSendOutcomeUnknown) {
		t.Fatalf("got %v", err)
	}
	assertClean(t, err, "tok1")

	// Junk (non-JSON) success body fails to decode without echoing content.
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.Header.Get("Authorization"))
	}))
	defer junk.Close()
	c = newClient(t, ts, junk, nil)
	var out map[string]any
	err = c.PostJSON(context.Background(), GmailBase+"/users/me/messages/send", nil, map[string]any{}, &out)
	if err == nil {
		t.Fatal("junk decoded")
	}
	assertClean(t, err, "tok1")
}

// PostJSON refuses a non-Google host exactly like GetJSON, before ever
// building a request (so nothing is ever "maybe sent" to a wrong host).
func TestPostJSONRefusesNonGoogleHosts(t *testing.T) {
	ts := newTokenServer(t)
	c, err := New(testCred(t), &Options{TokenURL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	err = c.PostJSON(context.Background(), "https://evil.example/x", nil, map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("got %v", err)
	}
	if ts.n.Load() != 0 {
		t.Fatal("token fetched for a refused host")
	}
}

func readJSON(r *http.Request, out any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(out)
}
