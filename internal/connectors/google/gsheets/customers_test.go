package gsheets_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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

const customersManifest = `
id: test
name: Test twin
usage: {window: 1h, model_calls: 3}
connectors:
  - name: company_customers
    functions:
      - {name: accounts, level: R}
`

type customersHarness struct {
	g *gate.Gate
}

// newCustomersHarness builds a gate over sh. withCredential controls
// whether the shared water.google/ceo credential is seeded in the vault —
// company_customers shares this credential with company_finance/gcal/
// gmail/gdrive, so an owner who has connected Google at all (for mail)
// already has it, regardless of whether the customers spreadsheet itself
// is configured.
func newCustomersHarness(t *testing.T, sh *gsheets.Customers, withCredential bool) *customersHarness {
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
	m, err := twins.Parse([]byte(customersManifest))
	if err != nil {
		t.Fatal(err)
	}
	v := vault.NewMemory()
	if withCredential {
		cred, err := testCred(t).Secret()
		if err != nil {
			t.Fatal(err)
		}
		if err := v.Set(gapi.Service, gapi.DefaultAccount, cred); err != nil {
			t.Fatal(err)
		}
	}
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: v, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	return &customersHarness{g: g}
}

func (h *customersHarness) invoke(t *testing.T) (gate.Result, error) {
	t.Helper()
	return h.g.Invoke(context.Background(), gate.Call{Function: "company_customers.accounts", Origin: gate.P0, Taint: gate.Clean})
}

// ---- not configured: the expected, current, correct state ----

// TestAccountsWithPlaceholderSpreadsheetIsNotConfigured is docs/slices/UI.md
// U3-A's own expected behavior *before* the owner uploads the real sheet:
// with a still-CONFIGURE_ME-prefixed spreadsheet id, every call fails
// clearly with ErrCustomersNotConfigured — and never makes an HTTP request
// at all (the server below fails the test if it ever receives one). The
// owner has since uploaded Renaissance_Customers.xlsx and this connector's
// production customersSpreadsheetID is now the real id (2026-09-27), so
// this test passes an explicit placeholder rather than relying on
// NewCustomersWithOptions("", ...)'s fallback to the production constant.
func TestAccountsWithPlaceholderSpreadsheetIsNotConfigured(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no Sheets API request should ever be made while the spreadsheet id is still the CONFIGURE_ME placeholder")
	}))
	defer api.Close()
	ts := newTokenServer(t)
	sh := gsheets.NewCustomersWithOptions("CONFIGURE_ME_test_placeholder", &gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep})
	h := newCustomersHarness(t, sh, true)

	_, err := h.invoke(t)
	if err == nil {
		t.Fatal("expected an error while the spreadsheet is unconfigured")
	}
	if !errors.Is(err, gsheets.ErrCustomersNotConfigured) {
		t.Fatalf("error = %v, want to match gsheets.ErrCustomersNotConfigured", err)
	}
}

// TestAccountsWithNoCredentialIsAlsoNotConnected is the other half of
// "not_connected (no credential/no sheet found)": with no shared Google
// credential at all, the gate itself refuses before Invoke ever runs, with
// its own "credential ... is unavailable" denial.
func TestAccountsWithNoCredentialIsAlsoNotConnected(t *testing.T) {
	sh := gsheets.NewCustomers()
	h := newCustomersHarness(t, sh, false)

	_, err := h.invoke(t)
	if err == nil {
		t.Fatal("expected an error with no credential configured")
	}
	if !strings.Contains(err.Error(), "credential for company_customers is unavailable") {
		t.Fatalf("error = %v, want the gate's credential-unavailable denial", err)
	}
}

// ---- configured: the real read path, exactly like gsheets_test.go's own
// cash-position/revenue-by-application tests ----

func TestAccountsReadsEveryRowOnceConfigured(t *testing.T) {
	api, gotRange := sheetsServer(t, `{"values":[
		["Meridian", "healthy", 1, 9, "2026-09-20"],
		["Northstar", "at_risk", 4, 5, "2026-08-01"],
		["Lexicon", "critical", 2, "", "2026-07-15"]
	]}`)
	ts := newTokenServer(t)
	sh := gsheets.NewCustomersWithOptions("test-customers-sheet-id", &gapi.Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: noSleep})
	h := newCustomersHarness(t, sh, true)

	res, err := h.invoke(t)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if !res.Untrusted {
		t.Fatal("company_customers.accounts must be marked untrusted (External)")
	}
	if *gotRange != "Accounts!A6:Q60" {
		t.Fatalf("requested range = %q, want %q", *gotRange, "Accounts!A6:Q60")
	}
	var out map[string]any
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	rows, ok := out["rows"].([]any)
	if !ok || len(rows) != 3 {
		t.Fatalf("rows = %+v, want 3", out["rows"])
	}
	if len(res.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(res.Records))
	}
	fig, ok := res.Records[0].(*store.FinanceFigure)
	if !ok || fig.Meta.Source != "company_customers" {
		t.Fatalf("record = %+v, want a company_customers FinanceFigure", res.Records[0])
	}
	assertNoSecrets(t, string(res.Output))
}

func TestAccountsFunctionIsReadOnly(t *testing.T) {
	sh := gsheets.NewCustomers()
	fns := sh.Functions()
	if len(fns) != 1 || fns[0].Name != "accounts" {
		t.Fatalf("Functions() = %+v, want exactly one \"accounts\" function", fns)
	}
	if fns[0].Level != twins.R {
		t.Fatalf("accounts level = %v, want R (read-only)", fns[0].Level)
	}
}

func TestCustomersName(t *testing.T) {
	if got := gsheets.NewCustomers().Name(); got != "company_customers" {
		t.Fatalf("Name() = %q, want company_customers", got)
	}
}

// noSleep/newTokenServer/testCred/sheetsServer/assertNoSecrets are shared
// with gsheets_test.go (same _test package).
