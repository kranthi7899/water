package gmail

import (
	"context"
	"encoding/json"
	"errors"
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
		{"list: since_history_id alone", listSchema, map[string]any{"since_history_id": "12345"}, true},
		{"list: since_history_id wrong type", listSchema, map[string]any{"since_history_id": 12345}, false},
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

	var out listMessagesOutput
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	msgs := out.Messages
	if len(msgs) != 3 || msgs[0].ID != "m1" || msgs[2].ID != "m3" {
		t.Fatalf("messages %+v", msgs)
	}
	if out.HistoryID != "" {
		t.Fatalf("history_id = %q, want empty: fixtures carry no historyId", out.HistoryID)
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
	var out listMessagesOutput
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	msgs := out.Messages
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

// TestListMessagesSinceHistoryID_FetchesAddedAndDedupesAcrossPages exercises
// the incremental path end to end through the gate: history.list pagination
// via nextPageToken, deduping a message ID (m10) that history.list repeats
// across pages, and reusing the same per-message metadata fetch as the
// query-based path to build identical records.
func TestListMessagesSinceHistoryID_FetchesAddedAndDedupesAcrossPages(t *testing.T) {
	ts := newTokenServer(t)
	api := gmailAPI(t, map[string][]byte{
		"/gmail/v1/users/me/history":                      fixture(t, "history_page1.json"),
		"/gmail/v1/users/me/history&pageToken=hp2":        fixture(t, "history_page2.json"),
		"/gmail/v1/users/me/messages/m10?format=metadata": fixture(t, "meta_m10.json"),
		"/gmail/v1/users/me/messages/m11?format=metadata": fixture(t, "meta_m11.json"),
	})
	defer api.Close()

	h := newHarness(t, NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep}))
	res, err := listMessages(t, h, map[string]any{"since_history_id": "100"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Untrusted {
		t.Fatal("expected Untrusted output: mail is written by other people")
	}

	var out listMessagesOutput
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("messages %+v, want 2 (m10 deduped once, m11)", out.Messages)
	}
	got := map[string]bool{}
	for _, m := range out.Messages {
		got[m.ID] = true
	}
	if !got["m10"] || !got["m11"] {
		t.Fatalf("messages %+v, want m10 and m11", out.Messages)
	}
	if out.HistoryID != "205" {
		t.Fatalf("history_id = %q, want %q (history.list's own last-page value)", out.HistoryID, "205")
	}

	if len(res.Records) != 2 {
		t.Fatalf("records %d, want 2", len(res.Records))
	}
	for _, r := range res.Records {
		m, ok := r.(*store.Message)
		if !ok || !m.External || m.Source != "gmail" {
			t.Fatalf("record: %+v", r)
		}
	}
}

// TestListMessagesSinceHistoryID_SkipsDeletedMessage covers the case the
// task calls out explicitly: a message history.list still reports as added
// but that 404s on its own metadata fetch (deleted in between) must be
// skipped, not mistaken for ErrHistoryTooOld.
func TestListMessagesSinceHistoryID_SkipsDeletedMessage(t *testing.T) {
	ts := newTokenServer(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gmail/v1/users/me/history":
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixture(t, "history_single.json"))
		case "/gmail/v1/users/me/messages/m20":
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixture(t, "meta_m20.json"))
		case "/gmail/v1/users/me/messages/m21":
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"code":404,"message":"Requested entity was not found."}}`)
		default:
			t.Fatalf("unexpected request %s", r.URL)
		}
	}))
	defer api.Close()

	h := newHarness(t, NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep}))
	res, err := listMessages(t, h, map[string]any{"since_history_id": "1"})
	if err != nil {
		t.Fatalf("expected the deleted message to be skipped, not to fail the call: %v", err)
	}
	var out listMessagesOutput
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 1 || out.Messages[0].ID != "m20" {
		t.Fatalf("messages %+v, want just m20 (m21 skipped)", out.Messages)
	}
	if out.HistoryID != "310" {
		t.Fatalf("history_id = %q, want %q", out.HistoryID, "310")
	}
}

// TestListMessagesSinceHistoryID_TooOld verifies the 404-from-history.list
// sentinel directly against the connector's own incremental path. It calls
// listMessagesSinceHistory (unexported, same package) with a *gapi.Client
// built straight from gapi.New rather than going through the gate: the gate
// redacts a failed call's error into a plain new error (internal/gate/
// gate.go's redact step, which strips credentials but, as a side effect,
// discards any wrapped sentinel's identity), so errors.Is only survives to
// check here, at the boundary this package actually controls. Downstream
// code that wants errors.Is(err, ErrHistoryTooOld) through gate.Gate.Invoke
// depends on that gate behavior changing.
func TestListMessagesSinceHistoryID_TooOld(t *testing.T) {
	ts := newTokenServer(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gmail/v1/users/me/history" {
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"code":404,"message":"Requested entity was not found."}}`)
	}))
	defer api.Close()

	cred := gapi.Credential{ClientID: "cid.apps.googleusercontent.com", ClientSecret: testSecret, RefreshToken: testRefresh}
	cl, err := gapi.New(cred, &gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	_, err = New().listMessagesSinceHistory(context.Background(), cl, "999999", 20)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrHistoryTooOld) {
		t.Fatalf("err = %v, want ErrHistoryTooOld in its chain", err)
	}
	for _, s := range []string{testSecret, testRefresh, "ya29."} {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error leaks %q: %v", s, err)
		}
	}
}

// TestListMessagesSinceHistoryID_NoSecretLeak mirrors
// TestSecretsNeverLeakInErrorsOrOutput for the incremental path: a failing
// history.list call must not surface the access token, refresh token or
// client secret through the gate either.
func TestListMessagesSinceHistoryID_NoSecretLeak(t *testing.T) {
	ts := newTokenServer(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":{"code":400,"message":"bad request for %s / %s / %s"}}`, tok, testRefresh, testSecret)
	}))
	defer api.Close()
	h := newHarness(t, NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep}))
	_, err := listMessages(t, h, map[string]any{"since_history_id": "1"})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, s := range []string{testSecret, testRefresh, "ya29."} {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error leaks %q: %v", s, err)
		}
	}
}

// fakeHistory is a stateful history.list + messages.get fake: records are
// numbered from 1, record i carries message "m<i>" with history id
// base+i, and history.list honours startHistoryId (exclusive, as Gmail
// documents it) and paginates perPage records at a time.
type fakeHistory struct {
	t        *testing.T
	base     int
	n        int
	perPage  int
	latest   string
	labels   map[int][]string // record index -> message labelIds
	fetched  sync.Map         // message id -> true
	noFetch  map[string]bool  // message ids that must never be fetched
	historyN atomic.Int32
}

func (f *fakeHistory) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/gmail/v1/users/me/history":
			f.historyN.Add(1)
			start := 0
			fmt.Sscanf(q.Get("startHistoryId"), "%d", &start)
			first := 1
			if pt := q.Get("pageToken"); pt != "" {
				fmt.Sscanf(pt, "p%d", &first)
			}
			type msg struct {
				ID       string   `json:"id"`
				ThreadID string   `json:"threadId"`
				LabelIDs []string `json:"labelIds,omitempty"`
			}
			type added struct {
				Message msg `json:"message"`
			}
			type rec struct {
				ID            string  `json:"id"`
				MessagesAdded []added `json:"messagesAdded"`
			}
			var recs []rec
			i := first
			for ; i <= f.n && len(recs) < f.perPage; i++ {
				if f.base+i <= start {
					continue
				}
				labels := f.labels[i]
				if labels == nil {
					labels = []string{"INBOX", "UNREAD"}
				}
				recs = append(recs, rec{
					ID:            fmt.Sprint(f.base + i),
					MessagesAdded: []added{{Message: msg{ID: fmt.Sprintf("m%d", i), ThreadID: fmt.Sprintf("t%d", i), LabelIDs: labels}}},
				})
			}
			resp := map[string]any{"history": recs, "historyId": f.latest}
			if i <= f.n {
				resp["nextPageToken"] = fmt.Sprintf("p%d", i)
			}
			json.NewEncoder(w).Encode(resp)
		case strings.HasPrefix(r.URL.Path, "/gmail/v1/users/me/messages/"):
			id := strings.TrimPrefix(r.URL.Path, "/gmail/v1/users/me/messages/")
			if f.noFetch[id] {
				f.t.Errorf("fetched %s, which history.list labelled as spam/trash/draft", id)
			}
			f.fetched.Store(id, true)
			var idx int
			fmt.Sscanf(id, "m%d", &idx)
			fmt.Fprintf(w, `{"id":%q,"threadId":"t%d","snippet":"hi","internalDate":"1758700000000","historyId":%q,"payload":{"headers":[{"name":"From","value":"x@acme.com"}]}}`,
				id, idx, fmt.Sprint(f.base+idx))
		default:
			f.t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// drainHistory repeatedly calls list_messages with since_history_id,
// threading each call's history_id into the next exactly as sync.tick does,
// and returns every message id seen in order plus the final cursor.
func drainHistory(t *testing.T, h *harness, cursor string, extra map[string]any, calls int) ([]string, string) {
	t.Helper()
	var ids []string
	for i := 0; i < calls; i++ {
		args := map[string]any{"since_history_id": cursor}
		for k, v := range extra {
			args[k] = v
		}
		res, err := listMessages(t, h, args)
		if err != nil {
			t.Fatal(err)
		}
		var out listMessagesOutput
		if err := json.Unmarshal(res.Output, &out); err != nil {
			t.Fatal(err)
		}
		for _, m := range out.Messages {
			ids = append(ids, m.ID)
		}
		if out.HistoryID != "" {
			cursor = out.HistoryID
		}
	}
	return ids, cursor
}

// TestListMessagesSinceHistoryID_MoreThanMaxResumesWhereItStopped proves a
// backlog bigger than max is drained over successive calls instead of the
// cursor jumping to the mailbox's latest historyId and silently skipping
// everything past the cut.
func TestListMessagesSinceHistoryID_MoreThanMaxResumesWhereItStopped(t *testing.T) {
	ts := newTokenServer(t)
	f := &fakeHistory{t: t, base: 100, n: 45, perPage: 100, latest: "9999"}
	api := f.server()
	defer api.Close()
	h := newHarness(t, NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep}))

	first, cursor := drainHistory(t, h, "100", nil, 1)
	if len(first) != 20 || first[0] != "m1" || first[19] != "m20" {
		t.Fatalf("first call returned %v, want m1..m20 (default max 20, oldest first)", first)
	}
	if cursor != "120" {
		t.Fatalf("cursor after a truncated call = %q, want 120 (the last history record actually processed), not the mailbox's latest", cursor)
	}
	rest, cursor := drainHistory(t, h, cursor, nil, 2)
	all := append(first, rest...)
	if len(all) != 45 {
		t.Fatalf("drained %d messages over 3 calls, want all 45: %v", len(all), all)
	}
	for i, id := range all {
		if want := fmt.Sprintf("m%d", i+1); id != want {
			t.Fatalf("message %d = %s, want %s", i, id, want)
		}
	}
	if cursor != "9999" {
		t.Fatalf("cursor after a complete walk = %q, want the mailbox's latest 9999", cursor)
	}
}

// TestListMessagesSinceHistoryID_MaxPagesResumesWhereItStopped covers the
// other truncation: the page loop giving up at maxPages with a
// nextPageToken still pending must not advance the cursor past what it
// actually walked.
func TestListMessagesSinceHistoryID_MaxPagesResumesWhereItStopped(t *testing.T) {
	ts := newTokenServer(t)
	f := &fakeHistory{t: t, base: 100, n: maxPages + 5, perPage: 1, latest: "9999"}
	api := f.server()
	defer api.Close()
	h := newHarness(t, NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep}))

	extra := map[string]any{"max": json.Number("100")}
	first, cursor := drainHistory(t, h, "100", extra, 1)
	if len(first) != maxPages {
		t.Fatalf("first call returned %d messages, want %d (one per page, maxPages pages)", len(first), maxPages)
	}
	if want := fmt.Sprint(100 + maxPages); cursor != want {
		t.Fatalf("cursor after hitting maxPages = %q, want %s", cursor, want)
	}
	rest, cursor := drainHistory(t, h, cursor, extra, 1)
	if len(first)+len(rest) != maxPages+5 {
		t.Fatalf("second call returned %v, want the remaining 5", rest)
	}
	if cursor != "9999" {
		t.Fatalf("final cursor %q, want 9999", cursor)
	}
}

// TestListMessagesSinceHistoryID_SkipsSpamTrashAndDrafts keeps the
// incremental path consistent with the query path (messages.list excludes
// SPAM/TRASH by default): such messages are never fetched, never returned,
// and do not take one of the max slots.
func TestListMessagesSinceHistoryID_SkipsSpamTrashAndDrafts(t *testing.T) {
	ts := newTokenServer(t)
	f := &fakeHistory{t: t, base: 100, n: 5, perPage: 100, latest: "500",
		labels:  map[int][]string{1: {"SPAM"}, 2: {"TRASH"}, 3: {"DRAFT"}, 5: {"SENT"}},
		noFetch: map[string]bool{"m1": true, "m2": true, "m3": true}}
	api := f.server()
	defer api.Close()
	h := newHarness(t, NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep}))

	ids, cursor := drainHistory(t, h, "100", map[string]any{"max": json.Number("2")}, 1)
	if len(ids) != 2 || ids[0] != "m4" || ids[1] != "m5" {
		t.Fatalf("messages %v, want [m4 m5]: spam/trash/draft skipped and not counted against max, SENT kept", ids)
	}
	if cursor != "500" {
		t.Fatalf("cursor %q, want 500 (walk completed)", cursor)
	}
}

// TestListDoesNotOverwriteFullBody guards against a list_messages call
// (which only has the snippet) downgrading a body get_message already
// stored in full under the same (gmail, id) identity.
func TestListDoesNotOverwriteFullBody(t *testing.T) {
	ts := newTokenServer(t)
	api := gmailAPI(t, map[string][]byte{
		"/gmail/v1/users/me/messages/m-flat?format=full":     fixture(t, "full_flat_plain.json"),
		"/gmail/v1/users/me/messages":                        []byte(`{"messages":[{"id":"m-flat","threadId":"t-flat"}]}`),
		"/gmail/v1/users/me/messages/m-flat?format=metadata": []byte(`{"id":"m-flat","threadId":"t-flat","snippet":"Confirmed","internalDate":"1758700000000","payload":{"headers":[{"name":"Subject","value":"Q3, flat (edited)"}]}}`),
	})
	defer api.Close()
	h := newHarness(t, NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep}))

	if _, err := getMessage(t, h, map[string]any{"id": "m-flat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := listMessages(t, h, map[string]any{"query": "from:dana"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get[store.Message](context.Background(), h.st, "gmail", "m-flat")
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "Confirmed for Q3." {
		t.Fatalf("body = %q, want the full body get_message stored, not the list snippet", got.Body)
	}
	if got.Subject != "Q3, flat (edited)" {
		t.Fatalf("subject = %q: other fields should still take the newer list values", got.Subject)
	}
	if !got.BodyFull {
		t.Fatal("BodyFull should stay set once a full body has been stored")
	}
}
