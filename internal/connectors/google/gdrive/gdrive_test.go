package gdrive_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/connectors"
	"water/internal/connectors/google/gapi"
	"water/internal/connectors/google/gdrive"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"

	_ "modernc.org/sqlite"
)

const (
	testSecret  = "GOCSPX-gdrive-test-secret"
	testRefresh = "1//gdrive-test-refresh-token"
)

func testCred(t *testing.T) gapi.Credential {
	return gapi.Credential{ClientID: "cid-" + t.Name() + ".apps.googleusercontent.com", ClientSecret: testSecret, RefreshToken: testRefresh}
}

// newTokenServer issues a fresh access token for every request. Each test
// gets its own server so the package-level token cache (keyed on TokenURL)
// starts cold.
func newTokenServer(t *testing.T) *httptest.Server {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"ya29.gdrivetok%d","expires_in":3600,"token_type":"Bearer"}`, n.Add(1))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func noSleep(context.Context, time.Duration) error { return nil }

const manifest = `
id: test
name: Test twin
usage: {window: 1h, model_calls: 3}
connectors:
  - name: gdrive
    functions:
      - {name: search_files, level: R}
      - {name: read_file, level: R}
`

type harness struct {
	g  *gate.Gate
	st *store.Store
}

func newHarness(t *testing.T, drv *gdrive.Drive) *harness {
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
	reg, err := connectors.NewRegistry(drv)
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(manifest))
	if err != nil {
		t.Fatal(err)
	}
	v := vault.NewMemory()
	cred, err := testCred(t).Secret()
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Set(gapi.Service, gapi.DefaultAccount, cred); err != nil {
		t.Fatal(err)
	}
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: v, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{g: g, st: st}
}

func (h *harness) invoke(t *testing.T, fn string, args map[string]any) (gate.Result, error) {
	t.Helper()
	return h.g.Invoke(context.Background(), gate.Call{Function: fn, Args: args, Origin: gate.P0, Taint: gate.Clean})
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func assertNoSecrets(t *testing.T, s string) {
	t.Helper()
	for _, secret := range []string{testSecret, testRefresh, "ya29."} {
		if strings.Contains(s, secret) {
			t.Fatalf("leaked %q in: %s", secret, s)
		}
	}
}

// TestSearchFilesNormalizeAndPagination checks that search_files follows
// nextPageToken until it has at least "max" results, stops there, and that
// Normalize turns the raw output into store.Document records.
func TestSearchFilesNormalizeAndPagination(t *testing.T) {
	pages := [][]byte{readFixture(t, "search_page1.json"), readFixture(t, "search_page2.json"), readFixture(t, "search_page3.json")}
	var calls atomic.Int32
	var gotQ, gotPageSize []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/drive/v3/files" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		gotQ = append(gotQ, r.URL.Query().Get("q"))
		gotPageSize = append(gotPageSize, r.URL.Query().Get("pageSize"))
		n := calls.Add(1)
		if n > int32(len(pages)) {
			t.Fatalf("fetched more pages than expected: %d", n)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(pages[n-1])
	}))
	defer api.Close()

	ts := newTokenServer(t)
	drv := gdrive.NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep})
	h := newHarness(t, drv)

	res, err := h.invoke(t, "gdrive.search_files", map[string]any{"query": "budget", "max": 3})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if !res.Untrusted {
		t.Fatal("search_files must be marked untrusted (External)")
	}
	// Only enough pages to reach 3 results (2 + 2 >= 3): page3 is never fetched.
	if calls.Load() != 2 {
		t.Fatalf("fetched %d pages, want 2", calls.Load())
	}
	wantQ := "trashed = false and (fullText contains 'budget' or name contains 'budget')"
	for _, q := range gotQ {
		if q != wantQ {
			t.Fatalf("q = %q, want %q", q, wantQ)
		}
	}
	if gotPageSize[0] != "3" || gotPageSize[1] != "1" {
		t.Fatalf("pageSize sequence = %v", gotPageSize)
	}

	if len(res.Records) != 3 {
		t.Fatalf("records = %d, want 3", len(res.Records))
	}
	want := []struct {
		id, title, mime, owner, url string
	}{
		{"f1", "Q3 Budget", "application/vnd.google-apps.spreadsheet", "Dana", "https://drive.google.com/file/f1"},
		{"f2", "Roadmap", "application/vnd.google-apps.document", "Sam", "https://drive.google.com/file/f2"},
		{"f3", "Notes", "text/plain", "Ceo", "https://drive.google.com/file/f3"},
	}
	for i, w := range want {
		doc, ok := res.Records[i].(*store.Document)
		if !ok {
			t.Fatalf("record %d is not a Document: %T", i, res.Records[i])
		}
		if doc.Source != "gdrive" || doc.SourceID != w.id || !doc.External {
			t.Fatalf("record %d meta: %+v", i, doc.Meta)
		}
		if doc.Title != w.title || doc.MimeType != w.mime || doc.Owner != w.owner || doc.URL != w.url {
			t.Fatalf("record %d: %+v, want %+v", i, doc, w)
		}
		if doc.Excerpt != "" {
			t.Fatalf("record %d: search_files must not set an excerpt, got %q", i, doc.Excerpt)
		}
		if doc.ModifiedAt.IsZero() {
			t.Fatalf("record %d: ModifiedAt not parsed", i)
		}
	}

	// The gate's own store upsert also lands these rows.
	stored, err := store.List[store.Document](context.Background(), h.st, store.Query{Source: "gdrive"})
	if err != nil || len(stored) != 3 {
		t.Fatalf("stored documents: %v %v", stored, err)
	}
}

// TestReadFileVariants covers Docs/Sheets/Slides export, plain text via
// alt=media, and metadata-only fallback for an unsupported type.
func TestReadFileVariants(t *testing.T) {
	longBody := strings.Repeat("The quarterly plan is ambitious and detailed. ", 20) // > 280 runes

	cases := []struct {
		name        string
		meta        string
		wantExport  string // expected mimeType query param on /export, "" if alt=media path used instead
		body        string
		wantContent string
		wantExcerpt string
		wantNote    bool
	}{
		{"doc", "doc_meta.json", "text/plain", longBody, longBody, string([]rune(longBody)[:280]), false},
		{"sheet", "sheet_meta.json", "text/csv", "a,b\n1,2\n", "a,b\n1,2\n", "a,b\n1,2\n", false},
		{"slide", "slide_meta.json", "text/plain", "Slide one\nSlide two\n", "Slide one\nSlide two\n", "Slide one\nSlide two\n", false},
		{"text", "text_meta.json", "", "# README\nHello.", "# README\nHello.", "# README\nHello.", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			meta := readFixture(t, c.meta)
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/export"):
					if r.URL.Query().Get("mimeType") != c.wantExport {
						t.Errorf("export mimeType = %q, want %q", r.URL.Query().Get("mimeType"), c.wantExport)
					}
					w.Write([]byte(c.body))
				case r.URL.Query().Get("alt") == "media":
					w.Write([]byte(c.body))
				default:
					w.Write(meta)
				}
			}))
			defer api.Close()
			ts := newTokenServer(t)
			drv := gdrive.NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep})
			h := newHarness(t, drv)

			res, err := h.invoke(t, "gdrive.read_file", map[string]any{"id": "x"})
			if err != nil {
				t.Fatalf("invoke: %v", err)
			}
			if !res.Untrusted {
				t.Fatal("read_file must be External")
			}
			if len(res.Records) != 1 {
				t.Fatalf("records = %d", len(res.Records))
			}
			doc := res.Records[0].(*store.Document)
			if !doc.External || doc.Source != "gdrive" {
				t.Fatalf("meta: %+v", doc.Meta)
			}
			if doc.Excerpt != c.wantExcerpt {
				t.Fatalf("excerpt = %q, want %q", doc.Excerpt, c.wantExcerpt)
			}
			assertNoSecrets(t, string(res.Output))
		})
	}

	t.Run("unsupported type is metadata only", func(t *testing.T) {
		meta := readFixture(t, "binary_meta.json")
		var exportHit, mediaHit atomic.Bool
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/export") {
				exportHit.Store(true)
			}
			if r.URL.Query().Get("alt") == "media" {
				mediaHit.Store(true)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(meta)
		}))
		defer api.Close()
		ts := newTokenServer(t)
		drv := gdrive.NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep})
		h := newHarness(t, drv)
		res, err := h.invoke(t, "gdrive.read_file", map[string]any{"id": "b1"})
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		if exportHit.Load() || mediaHit.Load() {
			t.Fatal("fetched content for an unsupported mime type")
		}
		doc := res.Records[0].(*store.Document)
		if doc.Excerpt != "" || doc.MimeType != "image/png" {
			t.Fatalf("doc: %+v", doc)
		}
		if !strings.Contains(string(res.Output), "metadata only") {
			t.Fatalf("output missing note: %s", res.Output)
		}
	})

	t.Run("content is capped", func(t *testing.T) {
		huge := strings.Repeat("x", 60<<10)
		meta := readFixture(t, "doc_meta.json")
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/export") {
				w.Write([]byte(huge))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(meta)
		}))
		defer api.Close()
		ts := newTokenServer(t)
		drv := gdrive.NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep})
		h := newHarness(t, drv)
		res, err := h.invoke(t, "gdrive.read_file", map[string]any{"id": "d1"})
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		if len(res.Output) > 60<<10 {
			t.Fatalf("output not capped: %d bytes", len(res.Output))
		}
		doc := res.Records[0].(*store.Document)
		if len([]rune(doc.Excerpt)) > 280 {
			t.Fatalf("excerpt too long: %d runes", len([]rune(doc.Excerpt)))
		}
	})
}

// TestUnauthorizedRefreshesAndRetries: a 401 on the first request triggers
// exactly one token refresh and one retry.
func TestUnauthorizedRefreshesAndRetries(t *testing.T) {
	var calls atomic.Int32
	page1 := readFixture(t, "search_page3.json")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") == "Bearer ya29.gdrivetok1" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"code":401,"message":"Invalid Credentials"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(page1)
	}))
	defer api.Close()
	ts := newTokenServer(t)
	drv := gdrive.NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep})
	h := newHarness(t, drv)

	res, err := h.invoke(t, "gdrive.search_files", map[string]any{"query": "archive"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 (one 401, one retry)", calls.Load())
	}
	if len(res.Records) != 1 {
		t.Fatalf("records = %d", len(res.Records))
	}
}

// TestBackoffOn429 checks a 429 is retried (with an injected no-op sleep)
// rather than failing the call.
func TestBackoffOn429(t *testing.T) {
	var calls atomic.Int32
	page1 := readFixture(t, "search_page3.json")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"code":429,"message":"slow down"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(page1)
	}))
	defer api.Close()
	ts := newTokenServer(t)
	drv := gdrive.NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep})
	h := newHarness(t, drv)

	if _, err := h.invoke(t, "gdrive.search_files", nil); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

// TestSecretsNeverLeak forces an API error that echoes the bearer token back
// and checks it never reaches the caller.
func TestSecretsNeverLeak(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":{"code":404,"message":"File not found: %s"}}`, tok)
	}))
	defer api.Close()
	ts := newTokenServer(t)
	drv := gdrive.NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep})
	h := newHarness(t, drv)

	_, err := h.invoke(t, "gdrive.read_file", map[string]any{"id": "missing"})
	if err == nil {
		t.Fatal("expected an error")
	}
	assertNoSecrets(t, err.Error())
}

// TestSchemaValidation checks bad arguments are refused before the connector
// ever runs.
func TestSchemaValidation(t *testing.T) {
	var called atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		w.Write([]byte(`{}`))
	}))
	defer api.Close()
	ts := newTokenServer(t)
	drv := gdrive.NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep})
	h := newHarness(t, drv)

	if _, err := h.invoke(t, "gdrive.read_file", map[string]any{}); err == nil || !strings.Contains(err.Error(), "missing argument") {
		t.Fatalf("missing id: %v", err)
	}
	if _, err := h.invoke(t, "gdrive.search_files", map[string]any{"bogus": "x"}); err == nil || !strings.Contains(err.Error(), "unexpected argument") {
		t.Fatalf("unexpected argument: %v", err)
	}
	if called.Load() {
		t.Fatal("connector ran despite invalid arguments")
	}
}

// TestExternalFlags checks every function is marked External, since all
// content it returns was written by someone else.
func TestExternalFlags(t *testing.T) {
	for _, f := range gdrive.New().Functions() {
		if !f.External {
			t.Fatalf("%s: expected External", f.Name)
		}
		if f.Level != twins.R {
			t.Fatalf("%s: expected level R, got %s", f.Name, f.Level)
		}
	}
}

func TestCredentialUsesGapiService(t *testing.T) {
	svc, acct := gdrive.New().Credential()
	if svc != gapi.Service || acct != gapi.DefaultAccount {
		t.Fatalf("credential = %s/%s", svc, acct)
	}
}
