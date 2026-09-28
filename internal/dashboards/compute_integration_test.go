package dashboards

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/connectors"
	"water/internal/connectors/google/gapi"
	"water/internal/connectors/google/gsheets"
	"water/internal/connectors/linear"
	"water/internal/connectors/tokenapi"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"

	_ "modernc.org/sqlite"
)

// This file is the end-to-end counterpart to compute_test.go's fakeInvoker
// unit tests: it drives the real gate over the real gsheets.Sheets,
// gsheets.Customers and linear.Linear connectors, each pointed at an
// httptest fixture server — exactly gsheets_test.go's and linear_test.go's
// own test convention — so "computed from connectors" is proven through
// the real connector code, not only a hand-rolled fake.

const integrationManifest = `
id: test
name: Test twin
usage: {window: 1h, model_calls: 10}
connectors:
  - name: company_finance
    functions:
      - {name: cash_position, level: R}
      - {name: spend_breakdown_all, level: R}
      - {name: monthly_costs, level: R}
      - {name: outstanding_invoices, level: R}
  - name: company_customers
    functions:
      - {name: accounts, level: R}
  - name: linear
    functions:
      - {name: list_issues, level: R}
`

func noSleep(context.Context, time.Duration) error { return nil }

// integrationRig wires one gate over real connectors, each against its own
// httptest fixture server (finance/customers share the Sheets fixture
// server since both are gsheets-family; linear gets its own GraphQL
// fixture server).
func newIntegrationRig(t *testing.T, sheetsBody, linearBody string) *Compute {
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

	sheetsAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sheetsBody))
	}))
	t.Cleanup(sheetsAPI.Close)
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"ya29.test","expires_in":3600,"token_type":"Bearer"}`))
	}))
	t.Cleanup(tokenSrv.Close)
	sheetsOpts := &gapi.Options{BaseURL: sheetsAPI.URL, TokenURL: tokenSrv.URL, Sleep: noSleep}
	finance := gsheets.NewWithOptions(sheetsOpts)
	customers := gsheets.NewCustomersWithOptions("test-customers-sheet-id", sheetsOpts)

	linearAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(linearBody))
	}))
	t.Cleanup(linearAPI.Close)
	lin := linear.NewWithOptions(&tokenapi.Options{BaseURL: linearAPI.URL, HTTPClient: linearAPI.Client(), Sleep: noSleep})

	reg, err := connectors.NewRegistry(finance, customers, lin)
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(integrationManifest))
	if err != nil {
		t.Fatal(err)
	}
	v := vault.NewMemory()
	googleCred, err := (gapi.Credential{ClientID: "cid.apps.googleusercontent.com", ClientSecret: "secret", RefreshToken: "refresh"}).Secret()
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Set(gapi.Service, gapi.DefaultAccount, googleCred); err != nil {
		t.Fatal(err)
	}
	linCred, err := (tokenapi.Credential{Token: "lin_api_testkey1234567890"}).Secret()
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Set(linear.Service, linear.Account, linCred); err != nil {
		t.Fatal(err)
	}
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: v, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, testNow)
	return &Compute{Gate: g, Cache: &Cache{TTL: DefaultCacheTTL}, Now: fixedClock(now)}
}

// TestIntegrationFinanceDashboardComputesExactNumbers drives the real
// company_finance connector (gsheets.Sheets) through the real gate against
// a single fixture Sheets API server — every tab/range request lands on
// the same handler, which always answers with cash_position's own fixed
// values regardless of which range was asked for. That is enough to prove
// the full pipeline (gate -> gsheets.Sheets.Invoke -> compute.go) produces
// the exact cash/burn/runway numbers the fixture carries.
func TestIntegrationFinanceCashMetricsComputeExactNumbers(t *testing.T) {
	co := newIntegrationRig(t, `{"values":[[250000],[40000],[6.25]]}`, `{"data":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}`)
	ctx := context.Background()

	if got := metricFuncs["cash_position"](ctx, co); got.State != TileOK || got.Value != 250000 {
		t.Fatalf("cash_position = %+v", got)
	}
	if got := metricFuncs["monthly_burn"](ctx, co); got.State != TileOK || got.Value != 40000 {
		t.Fatalf("monthly_burn = %+v", got)
	}
	if got := metricFuncs["runway_months"](ctx, co); got.State != TileOK || got.Value != 6.25 {
		t.Fatalf("runway_months = %+v", got)
	}
}

// TestIntegrationDeliveryDashboardComputesExactNumbers drives the real
// linear.Linear connector through the real gate against a fixture GraphQL
// server carrying one open issue and one it blocks.
func TestIntegrationDeliveryDashboardComputesExactNumbers(t *testing.T) {
	linearBody := `{"data":{"issues":{"nodes":[
		{"id":"i1","identifier":"CRA-3","title":"blocker","priority":1,"state":{"name":"In Progress","type":"started"},"team":{"key":"CRA"},
		 "relations":{"nodes":[{"type":"blocks","relatedIssue":{"identifier":"CRA-4","state":{"type":"unstarted"}}}]},
		 "inverseRelations":{"nodes":[]},"url":"https://linear.app/x/issue/CRA-3"},
		{"id":"i2","identifier":"CRA-4","title":"blocked","priority":3,"state":{"name":"Todo","type":"unstarted"},"team":{"key":"CRA"},
		 "relations":{"nodes":[]},
		 "inverseRelations":{"nodes":[{"type":"blocks","issue":{"identifier":"CRA-3","state":{"type":"started"}}}]},"url":"https://linear.app/x/issue/CRA-4"}
	],"pageInfo":{"hasNextPage":false}}}}`
	co := newIntegrationRig(t, `{"values":[]}`, linearBody)
	ctx := context.Background()

	if got := metricFuncs["open_issues"](ctx, co); got.State != TileOK || got.Value != 2 {
		t.Fatalf("open_issues = %+v", got)
	}
	if got := metricFuncs["blocked_issues"](ctx, co); got.State != TileOK || got.Value != 1 {
		t.Fatalf("blocked_issues = %+v", got)
	}
	if got := metricFuncs["urgent_issues"](ctx, co); got.State != TileOK || got.Value != 1 {
		t.Fatalf("urgent_issues = %+v", got)
	}
	if got := calloutFuncs["top_blocker_issue"](ctx, co); got.State != TileOK || got.Title != "CRA-3" {
		t.Fatalf("top_blocker_issue = %+v", got)
	}
}

// TestIntegrationClientsAccountsNotConnectedBeforeOwnerConfigures proves
// docs/slices/UI.md U3-A's pre-upload behavior: company_customers answers
// not_connected while its spreadsheet id is still a CONFIGURE_ME
// placeholder. The owner has since uploaded Renaissance_Customers.xlsx and
// customersSpreadsheetID is now real (2026-09-27), so this test now builds
// an explicitly-unconfigured instance via NewCustomersWithOptions rather
// than the production gsheets.NewCustomers() constructor — using the real
// constructor here would make a live network call to Google's OAuth
// endpoint with a fabricated refresh token instead of hitting the
// placeholder short-circuit, which is not what this test is for.
func TestIntegrationClientsAccountsNotConnectedBeforeOwnerConfigures(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log, err := audit.Open(filepath.Join(dir, "audit", "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	q := approvals.NewQueue(st, log)
	reg, err := connectors.NewRegistry(gsheets.NewCustomersWithOptions("CONFIGURE_ME_test_placeholder", nil))
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(`
id: test
name: Test twin
usage: {window: 1h, model_calls: 3}
connectors:
  - name: company_customers
    functions:
      - {name: accounts, level: R}
`))
	if err != nil {
		t.Fatal(err)
	}
	v := vault.NewMemory()
	googleCred, err := (gapi.Credential{ClientID: "cid.apps.googleusercontent.com", ClientSecret: "secret", RefreshToken: "refresh"}).Secret()
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Set(gapi.Service, gapi.DefaultAccount, googleCred); err != nil {
		t.Fatal(err)
	}
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: v, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, testNow)
	co := &Compute{Gate: g, Cache: &Cache{TTL: DefaultCacheTTL}, Now: fixedClock(now)}

	if got := metricFuncs["accounts_at_risk"](context.Background(), co); got.State != TileNotConnected {
		t.Fatalf("accounts_at_risk = %+v, want not_connected (spreadsheet id is an explicit CONFIGURE_ME placeholder)", got)
	}
}
