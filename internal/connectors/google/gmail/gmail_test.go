package gmail

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/connectors"
	"water/internal/connectors/google/gapi"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

const (
	testSecret  = "GOCSPX-test-secret-xyz"
	testRefresh = "1//test-refresh-token-abc"
)

func noSleep(context.Context, time.Duration) error { return nil }

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// tokenServer issues ya29.tokN access tokens and counts refreshes.
type tokenServer struct {
	*httptest.Server
	n atomic.Int32
}

func newTokenServer(t *testing.T) *tokenServer {
	ts := &tokenServer{}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		n := ts.n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"ya29.tok%d","expires_in":3600,"token_type":"Bearer"}`, n)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func testCredential(t *testing.T) vault.Secret {
	t.Helper()
	cred := gapi.Credential{ClientID: "cid.apps.googleusercontent.com", ClientSecret: testSecret, RefreshToken: testRefresh}
	sec, err := cred.Secret()
	if err != nil {
		t.Fatal(err)
	}
	return sec
}

const testManifest = `
id: test
name: Test twin
usage: {window: 1h, model_calls: 10}
connectors:
  - name: gmail
    functions:
      - {name: list_messages, level: R}
      - {name: get_message, level: R}
`

// harness wires one gmail connector through a real gate, the only way to
// mint the permit Invoke needs.
type harness struct {
	g   *gate.Gate
	st  *store.Store
	log *audit.Log
}

func newHarness(t *testing.T, conn *Gmail) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log, err := audit.Open(filepath.Join(dir, "audit", "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	q := approvals.NewQueue(st, log)
	reg, err := connectors.NewRegistry(conn)
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(testManifest))
	if err != nil {
		t.Fatal(err)
	}
	v := vault.NewMemory()
	v.Set(gapi.Service, gapi.DefaultAccount, testCredential(t))
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: v, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{g: g, st: st, log: log}
}

func listMessages(t *testing.T, h *harness, args map[string]any) (gate.Result, error) {
	t.Helper()
	return h.g.Invoke(context.Background(), gate.Call{Function: "gmail.list_messages", Args: args, Origin: gate.P0, Taint: gate.Clean})
}

func getMessage(t *testing.T, h *harness, args map[string]any) (gate.Result, error) {
	t.Helper()
	return h.g.Invoke(context.Background(), gate.Call{Function: "gmail.get_message", Args: args, Origin: gate.P0, Taint: gate.Clean})
}

func TestFunctionDeclaration(t *testing.T) {
	fns := New().Functions()
	if len(fns) != 2 {
		t.Fatalf("functions %d, want 2", len(fns))
	}
	for _, fn := range fns {
		if fn.Level != twins.R || fn.Risk != connectors.RiskLow || !fn.External {
			t.Fatalf("%s declaration: %+v", fn.Name, fn)
		}
	}
	if svc, acct := New().Credential(); svc != gapi.Service || acct != gapi.DefaultAccount {
		t.Fatalf("credential: %s/%s", svc, acct)
	}
	if New().Name() != "gmail" {
		t.Fatal("connector name")
	}
}

func TestSchemaValidation(t *testing.T) {
	fns := New().Functions()
	var listSchema, getSchema connectors.Schema
	for _, fn := range fns {
		switch fn.Name {
		case "list_messages":
			listSchema = fn.Schema
		case "get_message":
			getSchema = fn.Schema
		}
	}
	cases := []struct {
		name   string
		schema connectors.Schema
		args   map[string]any
		ok     bool
	}{
		{"list: empty args", listSchema, map[string]any{}, true},
		{"list: query and max", listSchema, map[string]any{"query": "from:dana", "max": json.Number("10")}, true},
		{"list: unexpected argument", listSchema, map[string]any{"bogus": "x"}, false},
		{"list: max wrong type", listSchema, map[string]any{"max": "lots"}, false},
		{"list: query wrong type", listSchema, map[string]any{"query": 5}, false},
		{"get: missing id", getSchema, map[string]any{}, false},
		{"get: valid id", getSchema, map[string]any{"id": "m1"}, true},
		{"get: id wrong type", getSchema, map[string]any{"id": 5}, false},
		{"get: unexpected argument", getSchema, map[string]any{"id": "m1", "extra": true}, false},
	}
	for _, tc := range cases {
		err := tc.schema.Validate(tc.args)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err=%v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

// gmailAPI serves the message-list and message-get endpoints from a small
// routing table keyed by (path, format query param).
func gmailAPI(t *testing.T, byPath map[string][]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		if f := r.URL.Query().Get("format"); f != "" {
			key += "?format=" + f
		}
		if pt := r.URL.Query().Get("pageToken"); pt != "" {
			key += "&pageToken=" + pt
		}
		body, ok := byPath[key]
		if !ok {
			t.Fatalf("unexpected request %s", key)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
}

func TestNormalizesPaginatesAndMarksExternal(t *testing.T) {
	ts := newTokenServer(t)
	api := gmailAPI(t, map[string][]byte{
		"/gmail/v1/users/me/messages":                    fixture(t, "messages_page1.json"),
		"/gmail/v1/users/me/messages&pageToken=p2":       fixture(t, "messages_page2.json"),
		"/gmail/v1/users/me/messages/m1?format=metadata": fixture(t, "meta_m1.json"),
		"/gmail/v1/users/me/messages/m2?format=metadata": fixture(t, "meta_m2.json"),
		"/gmail/v1/users/me/messages/m3?format=metadata": fixture(t, "meta_m3.json"),
	})
	defer api.Close()

	h := newHarness(t, NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep}))
	res, err := listMessages(t, h, map[string]any{"max": json.Number("3")})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Untrusted {
		t.Fatal("expected Untrusted output: mail is written by other people")
	}

	var msgs []message
	if err := json.Unmarshal(res.Output, &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 || msgs[0].ID != "m1" || msgs[2].ID != "m3" {
		t.Fatalf("messages %+v", msgs)
	}
	if got := msgs[1].To; len(got) != 2 || got[0] != "ceo@water.dev" || got[1] != "assistant@water.dev" {
		t.Fatalf("To split: %v", got)
	}
	if msgs[0].Body != "Can you confirm the Q3 numbers?" {
		t.Fatalf("list body should be the snippet, got %q", msgs[0].Body)
	}

	if len(res.Records) != 3 {
		t.Fatalf("records %d, want 3", len(res.Records))
	}
	for i, r := range res.Records {
		m, ok := r.(*store.Message)
		if !ok {
			t.Fatalf("record %d: wrong type %T", i, r)
		}
		if !m.External {
			t.Fatalf("record %d: not External", i)
		}
		if m.Source != "gmail" {
			t.Fatalf("record %d: source %q", i, m.Source)
		}
		if m.Channel != "email" {
			t.Fatalf("record %d: channel %q", i, m.Channel)
		}
	}
	m1 := res.Records[0].(*store.Message)
	if m1.SourceID != "m1" || m1.Thread != "t1" || m1.From != "Dana Lee <dana@acme.com>" || m1.Subject != "Q3 budget" {
		t.Fatalf("m1: %+v", m1)
	}
	if want := time.UnixMilli(1758623400000).UTC(); !m1.SentAt.Equal(want) {
		t.Fatalf("m1 sent_at = %v, want %v", m1.SentAt, want)
	}

	stored, err := store.List[store.Message](context.Background(), h.st, store.Query{Source: "gmail"})
	if err != nil || len(stored) != 3 {
		t.Fatalf("store list: %v %d", err, len(stored))
	}
}

func TestMaxCapsResultsAndStopsPaginating(t *testing.T) {
	ts := newTokenServer(t)
	var listReqs atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/gmail/v1/users/me/messages":
			listReqs.Add(1)
			w.Write(fixture(t, "messages_page1.json"))
		case r.URL.Path == "/gmail/v1/users/me/messages/m1":
			w.Write(fixture(t, "meta_m1.json"))
		default:
			t.Fatalf("unexpected request %s", r.URL)
		}
	}))
	defer api.Close()

	h := newHarness(t, NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep}))
	res, err := listMessages(t, h, map[string]any{"max": json.Number("1")})
	if err != nil {
		t.Fatal(err)
	}
	var msgs []message
	if err := json.Unmarshal(res.Output, &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].ID != "m1" {
		t.Fatalf("messages %+v, want just m1 (max cap)", msgs)
	}
	if listReqs.Load() != 1 {
		t.Fatalf("list requests %d, want 1: max was reached so nextPageToken must not be followed", listReqs.Load())
	}
}

func TestUnauthorizedRefreshesOnceThenRetries(t *testing.T) {
	ts := newTokenServer(t)
	full := fixture(t, "full_flat_plain.json")
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"code":401,"message":"Invalid Credentials"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(full)
	}))
	defer api.Close()
	h := newHarness(t, NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep}))
	res, err := getMessage(t, h, map[string]any{"id": "m-flat"})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("api calls %d, want 2 (401 then retry)", calls.Load())
	}
	if ts.n.Load() != 2 {
		t.Fatalf("token refreshes %d, want 2", ts.n.Load())
	}
	var m message
	if err := json.Unmarshal(res.Output, &m); err != nil || m.ID != "m-flat" {
		t.Fatalf("message %+v %v", m, err)
	}
}

func TestBackoffOn429ThenSucceeds(t *testing.T) {
	ts := newTokenServer(t)
	full := fixture(t, "full_flat_plain.json")
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"code":429,"message":"slow down"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(full)
	}))
	defer api.Close()
	var mu sync.Mutex
	var slept []time.Duration
	sleep := func(_ context.Context, d time.Duration) error {
		mu.Lock()
		slept = append(slept, d)
		mu.Unlock()
		return nil
	}
	h := newHarness(t, NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: sleep}))
	res, err := getMessage(t, h, map[string]any{"id": "m-flat"})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("api calls %d, want 2", calls.Load())
	}
	mu.Lock()
	n := len(slept)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("backoff sleeps %d, want 1", n)
	}
	var m message
	if err := json.Unmarshal(res.Output, &m); err != nil || m.ID != "m-flat" {
		t.Fatalf("message %+v %v", m, err)
	}
}

func TestSecretsNeverLeakInErrorsOrOutput(t *testing.T) {
	ts := newTokenServer(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":{"code":400,"message":"bad request for %s / %s / %s"}}`, tok, testRefresh, testSecret)
	}))
	defer api.Close()
	h := newHarness(t, NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep}))
	_, err := listMessages(t, h, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, s := range []string{testSecret, testRefresh, "ya29."} {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error leaks %q: %v", s, err)
		}
	}
	_, err = getMessage(t, h, map[string]any{"id": "m1"})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, s := range []string{testSecret, testRefresh, "ya29."} {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error leaks %q: %v", s, err)
		}
	}
	b, rerr := os.ReadFile(h.log.Path())
	if rerr != nil {
		t.Fatal(rerr)
	}
	for _, s := range []string{testSecret, testRefresh, "ya29."} {
		if strings.Contains(string(b), s) {
			t.Fatalf("audit log leaks %q", s)
		}
	}
}

// TestMimeWalking exercises the get_message MIME tree walk on nested
// multipart fixtures, including base64url with and without padding, and the
// text/html fallback when there is no text/plain part.
func TestMimeWalking(t *testing.T) {
	cases := []struct {
		file     string
		wantBody string
		wantFrom string
		wantTo   []string
		wantSubj string
	}{
		{"full_nested.json", "Confirmed for Q3.", "Dana Lee <dana@acme.com>", []string{"ceo@water.dev", "assistant@water.dev"}, "Q3 numbers"},
		{"full_html_only.json", "See attached for Q3 .", "dana@acme.com", []string{"ceo@water.dev"}, "Re: Q3 numbers"},
		{"full_flat_plain.json", "Confirmed for Q3.", "dana@acme.com", []string{"ceo@water.dev"}, "Q3, flat"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			ts := newTokenServer(t)
			body := fixture(t, tc.file)
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("format") != "full" {
					t.Fatalf("want format=full, got %s", r.URL.Query().Get("format"))
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(body)
			}))
			defer api.Close()
			h := newHarness(t, NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep}))
			res, err := getMessage(t, h, map[string]any{"id": "x"})
			if err != nil {
				t.Fatal(err)
			}
			var m message
			if err := json.Unmarshal(res.Output, &m); err != nil {
				t.Fatal(err)
			}
			if m.Body != tc.wantBody {
				t.Fatalf("body = %q, want %q", m.Body, tc.wantBody)
			}
			if m.From != tc.wantFrom || m.Subject != tc.wantSubj {
				t.Fatalf("from/subject = %q/%q, want %q/%q", m.From, m.Subject, tc.wantFrom, tc.wantSubj)
			}
			if len(m.To) != len(tc.wantTo) {
				t.Fatalf("to = %v, want %v", m.To, tc.wantTo)
			}
			for i := range tc.wantTo {
				if m.To[i] != tc.wantTo[i] {
					t.Fatalf("to[%d] = %q, want %q", i, m.To[i], tc.wantTo[i])
				}
			}
			rec, err := (&Gmail{}).Normalize("get_message", res.Output)
			if err != nil || len(rec) != 1 {
				t.Fatalf("normalize: %v %d", err, len(rec))
			}
			sm := rec[0].(*store.Message)
			if sm.Body != tc.wantBody || sm.Source != "gmail" || !sm.External {
				t.Fatalf("normalized record: %+v", sm)
			}
		})
	}
}

func TestNormalizeUnknownFunction(t *testing.T) {
	rec, err := New().Normalize("something_else", json.RawMessage(`{}`))
	if err != nil || rec != nil {
		t.Fatalf("unknown function: %v %v", rec, err)
	}
}

func TestBodyIsCapped(t *testing.T) {
	if maxBodyBytes <= 0 {
		t.Fatal("maxBodyBytes must be positive")
	}
	long := strings.Repeat("a", maxBodyBytes+500)
	got := capBody(long)
	if len(got) > maxBodyBytes {
		t.Fatalf("capBody left %d bytes, want <= %d", len(got), maxBodyBytes)
	}
}
