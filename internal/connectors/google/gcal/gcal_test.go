package gcal

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
  - name: gcal
    functions:
      - {name: list_events, level: R}
`

// harness wires one gcal connector through a real gate, the only way to mint
// the permit Invoke needs.
type harness struct {
	g   *gate.Gate
	st  *store.Store
	log *audit.Log
}

func newHarness(t *testing.T, conn *Calendar) *harness {
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

func listEvents(t *testing.T, h *harness, args map[string]any) (gate.Result, error) {
	t.Helper()
	return h.g.Invoke(context.Background(), gate.Call{Function: "gcal.list_events", Args: args, Origin: gate.P0, Taint: gate.Clean})
}

var basicRange = map[string]any{"time_min": "2026-09-24T00:00:00Z", "time_max": "2026-09-30T00:00:00Z"}

func TestFunctionDeclaration(t *testing.T) {
	fn := New().Functions()[0]
	if fn.Name != "list_events" || fn.Level != twins.R || fn.Risk != connectors.RiskLow || !fn.External {
		t.Fatalf("declaration: %+v", fn)
	}
	if svc, acct := New().Credential(); svc != gapi.Service || acct != gapi.DefaultAccount {
		t.Fatalf("credential: %s/%s", svc, acct)
	}
	if New().Name() != "gcal" {
		t.Fatal("connector name")
	}
}

func TestSchemaValidation(t *testing.T) {
	schema := New().Functions()[0].Schema
	cases := []struct {
		name string
		args map[string]any
		ok   bool
	}{
		{"missing time_min", map[string]any{"time_max": "b"}, false},
		{"missing time_max", map[string]any{"time_min": "a"}, false},
		{"unexpected argument", map[string]any{"time_min": "a", "time_max": "b", "extra": true}, false},
		{"max wrong type", map[string]any{"time_min": "a", "time_max": "b", "max": "lots"}, false},
		{"calendar_id wrong type", map[string]any{"time_min": "a", "time_max": "b", "calendar_id": 5}, false},
		{"minimal valid", map[string]any{"time_min": "a", "time_max": "b"}, true},
		{"full valid", map[string]any{"time_min": "a", "time_max": "b", "calendar_id": "team@acme.com", "max": json.Number("10")}, true},
	}
	for _, tc := range cases {
		err := schema.Validate(tc.args)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err=%v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestInvalidTimeFormatNeverReachesTheAPI(t *testing.T) {
	ts := newTokenServer(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("API reached with an invalid time argument")
	}))
	defer api.Close()
	h := newHarness(t, NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep}))
	_, err := listEvents(t, h, map[string]any{"time_min": "not-a-time", "time_max": "2026-09-30T00:00:00Z"})
	if err == nil || !strings.Contains(err.Error(), "RFC 3339") {
		t.Fatalf("got %v", err)
	}
}

func TestNormalizesPaginatesAndMarksExternal(t *testing.T) {
	ts := newTokenServer(t)
	page1, page2 := fixture(t, "events_page1.json"), fixture(t, "events_page2.json")
	var reqs int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		if r.URL.Path != "/calendar/v3/calendars/primary/events" {
			t.Errorf("path %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("singleEvents") != "true" || q.Get("orderBy") != "startTime" ||
			q.Get("timeMin") != basicRange["time_min"] || q.Get("timeMax") != basicRange["time_max"] {
			t.Errorf("query %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		if q.Get("pageToken") == "p2" {
			w.Write(page2)
			return
		}
		if q.Get("pageToken") != "" {
			t.Errorf("unexpected pageToken %q on first page", q.Get("pageToken"))
		}
		w.Write(page1)
	}))
	defer api.Close()

	h := newHarness(t, NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep}))
	res, err := listEvents(t, h, basicRange)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Untrusted {
		t.Fatal("expected Untrusted output for a function that reads other people's events")
	}
	if reqs != 2 {
		t.Fatalf("requests %d, want 2 (nextPageToken followed)", reqs)
	}

	var events []Event
	if err := json.Unmarshal(res.Output, &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events %d, want 3", len(events))
	}
	if events[0].ID != "e1" || events[0].CalendarID != "primary" {
		t.Fatalf("event0: %+v", events[0])
	}
	if n := len([]rune(events[0].Description)); n > descriptionCap+1 {
		t.Fatalf("description not capped: %d runes", n)
	}
	if events[2].ID != "e3" {
		t.Fatalf("event2: %+v", events[2])
	}

	if len(res.Records) != 3 {
		t.Fatalf("records %d, want 3", len(res.Records))
	}
	for i, r := range res.Records {
		ev, ok := r.(*store.Event)
		if !ok {
			t.Fatalf("record %d: wrong type %T", i, r)
		}
		if !ev.External {
			t.Fatalf("record %d: not External", i)
		}
		if ev.Source != "gcal" {
			t.Fatalf("record %d: source %q", i, ev.Source)
		}
	}
	e1 := res.Records[0].(*store.Event)
	if e1.SourceID != "primary:e1" {
		t.Fatalf("e1 source id %q", e1.SourceID)
	}
	if want := time.Date(2026, 9, 24, 13, 0, 0, 0, time.UTC); !e1.StartAt.Equal(want) {
		t.Fatalf("e1 start = %v, want %v (09:00-04:00)", e1.StartAt, want)
	}
	e2 := res.Records[1].(*store.Event)
	if e2.SourceID != "primary:e2" {
		t.Fatalf("e2 source id %q", e2.SourceID)
	}
	if want := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC); !e2.StartAt.Equal(want) {
		t.Fatalf("all-day e2 start = %v, want %v", e2.StartAt, want)
	}

	stored, err := store.List[store.Event](context.Background(), h.st, store.Query{Source: "gcal"})
	if err != nil || len(stored) != 3 {
		t.Fatalf("store list: %v %d", err, len(stored))
	}
}

func TestMaxCapsResultsAndStopsPaginating(t *testing.T) {
	ts := newTokenServer(t)
	capped := fixture(t, "events_capped.json")
	var reqs atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write(capped)
	}))
	defer api.Close()
	h := newHarness(t, NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep}))
	args := map[string]any{"time_min": basicRange["time_min"], "time_max": basicRange["time_max"], "max": json.Number("1")}
	res, err := listEvents(t, h, args)
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	if err := json.Unmarshal(res.Output, &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events %d, want 1 (max cap)", len(events))
	}
	if reqs.Load() != 1 {
		t.Fatalf("requests %d, want 1: max was reached so nextPageToken must not be followed", reqs.Load())
	}
}

func TestCalendarIDIsPathEscaped(t *testing.T) {
	ts := newTokenServer(t)
	single := fixture(t, "events_page2.json")
	var gotPath string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write(single)
	}))
	defer api.Close()
	h := newHarness(t, NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep}))
	args := map[string]any{"time_min": basicRange["time_min"], "time_max": basicRange["time_max"], "calendar_id": "team@acme.com"}
	if _, err := listEvents(t, h, args); err != nil {
		t.Fatal(err)
	}
	if want := "/calendar/v3/calendars/team@acme.com/events"; gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}
}

func TestUnauthorizedRefreshesOnceThenRetries(t *testing.T) {
	ts := newTokenServer(t)
	single := fixture(t, "events_page2.json")
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"code":401,"message":"Invalid Credentials"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(single)
	}))
	defer api.Close()
	h := newHarness(t, NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep}))
	res, err := listEvents(t, h, basicRange)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("api calls %d, want 2 (401 then retry)", calls.Load())
	}
	if ts.n.Load() != 2 {
		t.Fatalf("token refreshes %d, want 2", ts.n.Load())
	}
	var events []Event
	if err := json.Unmarshal(res.Output, &events); err != nil || len(events) != 1 || events[0].ID != "e3" {
		t.Fatalf("events %+v %v", events, err)
	}
}

func TestBackoffOn429ThenSucceeds(t *testing.T) {
	ts := newTokenServer(t)
	single := fixture(t, "events_page2.json")
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"code":429,"message":"slow down"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(single)
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
	res, err := listEvents(t, h, basicRange)
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
	var events []Event
	if err := json.Unmarshal(res.Output, &events); err != nil || len(events) != 1 {
		t.Fatalf("events %+v %v", events, err)
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
	_, err := listEvents(t, h, basicRange)
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
