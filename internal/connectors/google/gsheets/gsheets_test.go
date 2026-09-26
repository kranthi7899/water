package gsheets_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/connectors"
	"water/internal/connectors/google/gapi"
	"water/internal/connectors/google/gsheets"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"

	_ "modernc.org/sqlite"
)

const (
	testSecret  = "GOCSPX-gsheets-test-secret"
	testRefresh = "1//gsheets-test-refresh-token"
)

func testCred(t *testing.T) gapi.Credential {
	return gapi.Credential{ClientID: "cid-" + t.Name() + ".apps.googleusercontent.com", ClientSecret: testSecret, RefreshToken: testRefresh}
}

func newTokenServer(t *testing.T) *httptest.Server {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"ya29.gsheetstok%d","expires_in":3600,"token_type":"Bearer"}`, n.Add(1))
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
  - name: company_finance
    functions:
      - {name: cash_position, level: R}
      - {name: budget_status, level: R}
      - {name: spend_breakdown, level: R}
      - {name: outstanding_invoices, level: R}
      - {name: revenue_by_application, level: R}
      - {name: funding_history, level: R}
`

type harness struct {
	g  *gate.Gate
	st *store.Store
}

func newHarness(t *testing.T, sh *gsheets.Sheets) *harness {
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
	reg, err := connectors.NewRegistry(sh)
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

func assertNoSecrets(t *testing.T, s string) {
	t.Helper()
	for _, secret := range []string{testSecret, testRefresh, "ya29."} {
		if strings.Contains(s, secret) {
			t.Fatalf("leaked %q in: %s", secret, s)
		}
	}
}

// sheetsServer returns an httptest server that answers any
// /v4/spreadsheets/{id}/values/{a1} request with body, recording the
// decoded A1 range it was asked for (tab!range, URL-decoded).
func sheetsServer(t *testing.T, body string) (*httptest.Server, *string) {
	t.Helper()
	var gotRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "/v4/spreadsheets/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		rest := strings.TrimPrefix(r.URL.Path, prefix)
		parts := strings.SplitN(rest, "/values/", 2)
		if len(parts) != 2 {
			t.Fatalf("unexpected path shape %s", r.URL.Path)
		}
		gotRange = parts[1]
		if got := r.URL.Query().Get("valueRenderOption"); got != "UNFORMATTED_VALUE" {
			t.Errorf("valueRenderOption = %q, want UNFORMATTED_VALUE", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &gotRange
}

func TestCashPositionReadsTheRightRangeAndLabelsInOrder(t *testing.T) {
	api, gotRange := sheetsServer(t, `{"range":"Cash & runway!B19:B21","values":[[250000],[40000],[6.25]]}`)
	ts := newTokenServer(t)
	sh := gsheets.NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep})
	h := newHarness(t, sh)

	res, err := h.invoke(t, "company_finance.cash_position", nil)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if !res.Untrusted {
		t.Fatal("cash_position must be marked untrusted (External)")
	}
	if *gotRange != "Cash & runway!B19:B21" {
		t.Fatalf("requested range = %q, want %q", *gotRange, "Cash & runway!B19:B21")
	}

	var out map[string]any
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	vals := out["values"].(map[string]any)
	if vals["cash_usd"] != 250000.0 || vals["burn_usd"] != 40000.0 || vals["runway_months"] != 6.25 {
		t.Fatalf("values = %+v", vals)
	}

	if len(res.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(res.Records))
	}
	fig, ok := res.Records[0].(*store.FinanceFigure)
	if !ok {
		t.Fatalf("record type = %T, want *store.FinanceFigure", res.Records[0])
	}
	if fig.Meta.Source != "company_finance" || fig.Meta.SourceID != "Cash & runway!B19:B21" {
		t.Fatalf("record meta = %+v", fig.Meta)
	}
	if fig.Tab != "Cash & runway" || fig.RangeA1 != "B19:B21" {
		t.Fatalf("record tab/range = %q/%q", fig.Tab, fig.RangeA1)
	}
	assertNoSecrets(t, string(res.Output))
}

// TestCashPositionIsLiveNotCached is the acceptance criterion from the
// request itself: two calls with different fixture data return different
// answers, proving nothing is cached or hardcoded.
func TestCashPositionIsLiveNotCached(t *testing.T) {
	var body atomic.Value
	body.Store(`{"values":[[100],[10],[10]]}`)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body.Load().(string))
	}))
	defer api.Close()
	ts := newTokenServer(t)
	sh := gsheets.NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep})
	h := newHarness(t, sh)

	res1, err := h.invoke(t, "company_finance.cash_position", nil)
	if err != nil {
		t.Fatal(err)
	}
	body.Store(`{"values":[[999],[20],[20]]}`)
	res2, err := h.invoke(t, "company_finance.cash_position", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(res1.Output) == string(res2.Output) {
		t.Fatal("two calls with different fixture data returned identical output — looks cached/hardcoded, not live")
	}
	var out2 map[string]any
	json.Unmarshal(res2.Output, &out2)
	if out2["values"].(map[string]any)["cash_usd"] != 999.0 {
		t.Fatalf("second call did not reflect the updated fixture: %+v", out2)
	}
}

func TestBudgetStatusMatchesRowByApplicationRegardlessOfColumn(t *testing.T) {
	api, _ := sheetsServer(t, `{"values":[
		["crawler", 50000, 48000, -2000, 12000],
		["econ-rag", 30000, 31000, 1000, 8000]
	]}`)
	ts := newTokenServer(t)
	sh := gsheets.NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep})
	h := newHarness(t, sh)

	res, err := h.invoke(t, "company_finance.budget_status", map[string]any{"application": "econ-rag"})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.Unmarshal(res.Output, &out)
	if out["matched"] != true {
		t.Fatalf("matched = %v, want true", out["matched"])
	}
	row := out["row"].([]any)
	if row[0] != "econ-rag" {
		t.Fatalf("row = %v, want the econ-rag row", row)
	}

	if len(res.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(res.Records))
	}
	fig := res.Records[0].(*store.FinanceFigure)
	if fig.Meta.SourceID != "Budget!A5:I11#econ-rag" {
		t.Fatalf("source id = %q, want a per-application id so two applications don't overwrite each other", fig.Meta.SourceID)
	}
}

func TestBudgetStatusNoMatchIsCleanNotError(t *testing.T) {
	api, _ := sheetsServer(t, `{"values":[["crawler", 50000, 48000, -2000, 12000]]}`)
	ts := newTokenServer(t)
	sh := gsheets.NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep})
	h := newHarness(t, sh)

	res, err := h.invoke(t, "company_finance.budget_status", map[string]any{"application": "no-such-application"})
	if err != nil {
		t.Fatalf("a no-match should not be a call error: %v", err)
	}
	var out map[string]any
	json.Unmarshal(res.Output, &out)
	if out["matched"] != false {
		t.Fatalf("matched = %v, want false", out["matched"])
	}
}

func TestTwoDifferentApplicationsDoNotOverwriteEachOtherInTheStore(t *testing.T) {
	api, _ := sheetsServer(t, `{"values":[
		["crawler", 50000, 48000, -2000, 12000],
		["econ-rag", 30000, 31000, 1000, 8000]
	]}`)
	ts := newTokenServer(t)
	sh := gsheets.NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep})
	h := newHarness(t, sh)

	if _, err := h.invoke(t, "company_finance.budget_status", map[string]any{"application": "crawler"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.invoke(t, "company_finance.budget_status", map[string]any{"application": "econ-rag"}); err != nil {
		t.Fatal(err)
	}
	crawler, err := store.Get[store.FinanceFigure](context.Background(), h.st, "company_finance", "Budget!A5:I11#crawler")
	if err != nil {
		t.Fatalf("crawler record missing after econ-rag was also queried: %v", err)
	}
	econ, err := store.Get[store.FinanceFigure](context.Background(), h.st, "company_finance", "Budget!A5:I11#econ-rag")
	if err != nil {
		t.Fatalf("econ-rag record missing: %v", err)
	}
	if crawler.Values == econ.Values {
		t.Fatal("both stored records have identical values — one overwrote the other")
	}
}

func TestOutstandingInvoicesFiltersToOpenOnly(t *testing.T) {
	api, _ := sheetsServer(t, `{"values":[
		["inv-1", "Acme", 5000, "2026-09-01", "Open"],
		["inv-2", "Beta", 3000, "2026-08-01", "Paid"],
		["inv-3", "Gamma", 7000, "2026-09-10", "open"]
	]}`)
	ts := newTokenServer(t)
	sh := gsheets.NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep})
	h := newHarness(t, sh)

	res, err := h.invoke(t, "company_finance.outstanding_invoices", nil)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.Unmarshal(res.Output, &out)
	rows := out["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (case-insensitive Open match, Paid excluded): %+v", len(rows), rows)
	}
}

func TestRevenueByApplicationAndFundingHistoryReturnRawGrid(t *testing.T) {
	api, _ := sheetsServer(t, `{"values":[["a","b"],["c","d"]]}`)
	ts := newTokenServer(t)
	sh := gsheets.NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep})
	h := newHarness(t, sh)

	for _, fn := range []string{"company_finance.revenue_by_application", "company_finance.funding_history"} {
		res, err := h.invoke(t, fn, nil)
		if err != nil {
			t.Fatalf("%s: %v", fn, err)
		}
		var out map[string]any
		json.Unmarshal(res.Output, &out)
		rows := out["rows"].([]any)
		if len(rows) != 2 {
			t.Fatalf("%s: rows = %d, want 2", fn, len(rows))
		}
	}
}

func TestFunctionsAreAllReadOnly(t *testing.T) {
	sh := gsheets.New()
	fns := sh.Functions()
	want := map[string]bool{
		"cash_position": true, "budget_status": true, "spend_breakdown": true,
		"outstanding_invoices": true, "revenue_by_application": true, "funding_history": true,
	}
	if len(fns) != len(want) {
		t.Fatalf("got %d functions, want exactly %d: %+v", len(fns), len(want), fns)
	}
	for _, f := range fns {
		if !want[f.Name] {
			t.Fatalf("unexpected function %q — no write function should ever be declared for this connector", f.Name)
		}
		if f.Level != twins.R {
			t.Fatalf("function %q level = %v, want R (read-only)", f.Name, f.Level)
		}
	}
}
