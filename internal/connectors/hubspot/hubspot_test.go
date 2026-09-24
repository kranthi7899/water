package hubspot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/connectors"
	"water/internal/connectors/tokenapi"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

const testManifest = `
id: test
name: Test twin
usage: {window: 1h, model_calls: 10}
connectors:
  - name: hubspot
    functions:
      - {name: list_deals, level: R}
      - {name: list_contacts, level: R}
`

func noSleep(context.Context, time.Duration) error { return nil }

func testCredential(t *testing.T) vault.Secret {
	t.Helper()
	cred := tokenapi.Credential{Token: "pat-na1-testtoken1234567890"}
	sec, err := cred.Secret()
	if err != nil {
		t.Fatal(err)
	}
	return sec
}

type harness struct {
	g *gate.Gate
}

func newHarness(t *testing.T, srv *httptest.Server, tok vault.Secret) *harness {
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
	opts := &tokenapi.Options{BaseURL: srv.URL, HTTPClient: srv.Client(), Sleep: noSleep}
	conn := NewWithOptions(opts)
	reg, err := connectors.NewRegistry(conn)
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(testManifest))
	if err != nil {
		t.Fatal(err)
	}
	v := vault.NewMemory()
	if !tok.IsZero() {
		if err := v.Set(Service, Account, tok); err != nil {
			t.Fatal(err)
		}
	}
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: v, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{g: g}
}

func (h *harness) invoke(t *testing.T, fn string, args map[string]any) (gate.Result, error) {
	t.Helper()
	return h.g.Invoke(context.Background(), gate.Call{Function: "hubspot." + fn, Args: args, Origin: gate.P0, Taint: gate.Clean})
}

// ---- schema ----

func TestFunctionsSchema(t *testing.T) {
	hs := New()
	for _, f := range hs.Functions() {
		if f.Level != twins.R {
			t.Fatalf("%s: level %s, want R", f.Name, f.Level)
		}
		if !f.External {
			t.Fatalf("%s: must be marked External", f.Name)
		}
		b, err := json.Marshal(f.Schema)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"additionalProperties":false`) {
			t.Fatalf("%s schema: %s", f.Name, b)
		}
		if err := f.Schema.Validate(map[string]any{"bogus": "x"}); err == nil {
			t.Fatalf("%s: unknown argument accepted", f.Name)
		}
	}
}

// ---- deals: pagination, company-association resolution, amount_usd ----

func TestListDealsResolvesCompaniesAndPaginates(t *testing.T) {
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer pat-na1-testtoken1234567890" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/crm/v3/objects/deals"):
			calls["deals"]++
			after := r.URL.Query().Get("after")
			if after == "" {
				fmt.Fprint(w, `{"results":[{"id":"d1","properties":{"dealname":"Acme renewal","dealstage":"Negotiation","amount":"42000.00","closedate":"2026-10-08T00:00:00.000Z"},"associations":{"companies":{"results":[{"id":"c1"}]}}}],"paging":{"next":{"after":"page2"}}}`)
			} else if after == "page2" {
				fmt.Fprint(w, `{"results":[{"id":"d2","properties":{"dealname":"Northwind pilot","dealstage":"Proposal Sent","amount":"18500.50","closedate":"2026-10-04T00:00:00.000Z"},"associations":{"companies":{"results":[{"id":"c2"}]}}}]}`)
			}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/crm/v3/objects/companies/batch/read"):
			calls["companies_batch"]++
			var body struct {
				Inputs []struct {
					ID string `json:"id"`
				} `json:"inputs"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			names := map[string]string{"c1": "Acme Robotics", "c2": "Northwind Logistics"}
			var results []map[string]any
			for _, in := range body.Inputs {
				if n, ok := names[in.ID]; ok {
					results = append(results, map[string]any{"id": in.ID, "properties": map[string]string{"name": n}})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	h := newHarness(t, srv, testCredential(t))
	res, err := h.invoke(t, "list_deals", nil)
	if err != nil {
		t.Fatal(err)
	}
	var deals []dealOutput
	if err := json.Unmarshal(res.Output, &deals); err != nil {
		t.Fatal(err)
	}
	if len(deals) != 2 {
		t.Fatalf("got %d deals, want 2", len(deals))
	}
	if deals[0].Company != "Acme Robotics" || deals[1].Company != "Northwind Logistics" {
		t.Fatalf("company resolution: %+v", deals)
	}
	if deals[0].AmountUSD != 42000 {
		t.Fatalf("AmountUSD for deal 0 = %v, want 42000 (4200000 cents)", deals[0].AmountUSD)
	}
	if deals[1].AmountCents != 1850050 {
		t.Fatalf("AmountCents for deal 1 = %d, want 1850050", deals[1].AmountCents)
	}
	if calls["deals"] != 2 {
		t.Fatalf("deals pages fetched = %d, want 2", calls["deals"])
	}
	if calls["companies_batch"] != 1 {
		t.Fatalf("companies batch/read calls = %d, want 1 (both ids in one batch)", calls["companies_batch"])
	}
	if !res.Untrusted {
		t.Fatal("hubspot.list_deals result must be marked untrusted (External)")
	}
}

// ---- contacts: query filter ----

func TestListContactsFilters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[
			{"id":"1","properties":{"firstname":"Elena","lastname":"Cross","email":"elena@meridian.example","company":"Meridian Ventures","jobtitle":"Partner"}},
			{"id":"2","properties":{"firstname":"Marcus","lastname":"Webb","email":"marcus@acme.example","company":"Acme Robotics","jobtitle":"VP Engineering"}}
		]}`)
	}))
	defer srv.Close()

	h := newHarness(t, srv, testCredential(t))
	res, err := h.invoke(t, "list_contacts", map[string]any{"query": "meridian"})
	if err != nil {
		t.Fatal(err)
	}
	var contacts []Contact
	if err := json.Unmarshal(res.Output, &contacts); err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 1 || contacts[0].Name != "Elena Cross" {
		t.Fatalf("filtered contacts: %+v", contacts)
	}
}

// ---- 401 ----

func TestInvokeReturns401AsAClearAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"This oauth token does not exist"}`))
	}))
	defer srv.Close()

	h := newHarness(t, srv, testCredential(t))
	_, err := h.invoke(t, "list_deals", nil)
	if err == nil {
		t.Fatal("expected an error on 401")
	}
}

// ---- not connected ----

func TestInvokeWithNoTokenFailsClearly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no request should ever be made without a token")
	}))
	defer srv.Close()

	h := newHarness(t, srv, vault.Secret{})
	_, err := h.invoke(t, "list_contacts", nil)
	if err == nil {
		t.Fatal("expected an error with no token configured")
	}
}

// ---- Normalize ----

func TestNormalizeDealsAndContacts(t *testing.T) {
	hs := New()

	deals := []Deal{{ID: "d1", Name: "Acme renewal", Stage: "Negotiation", AmountCents: 4200000, CloseDate: "2026-10-08T00:00:00.000Z", Company: "Acme Robotics"}}
	dealRaw, err := json.Marshal(deals)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := hs.Normalize("list_deals", dealRaw)
	if err != nil {
		t.Fatal(err)
	}
	tx, ok := recs[0].(*store.Transaction)
	if !ok {
		t.Fatalf("deal did not normalize to *store.Transaction: %T", recs[0])
	}
	if !tx.External || tx.Account != "Negotiation" || tx.Counterparty != "Acme Robotics" || tx.AmountMinor != 4200000 || tx.PostedAt.IsZero() {
		t.Fatalf("transaction from deal: %+v", tx)
	}

	contacts := []Contact{{ID: "c1", Name: "Elena Cross", Email: "elena@meridian.example", Company: "Meridian Ventures", Title: "Partner"}}
	contactRaw, err := json.Marshal(contacts)
	if err != nil {
		t.Fatal(err)
	}
	recs, err = hs.Normalize("list_contacts", contactRaw)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := recs[0].(*store.Contact)
	if !ok {
		t.Fatalf("contact did not normalize to *store.Contact: %T", recs[0])
	}
	if !c.External || c.Org != "Meridian Ventures" || c.Title != "Partner" {
		t.Fatalf("contact: %+v", c)
	}
}

// ---- secrets never leak ----

func TestTokenNeverAppearsInError(t *testing.T) {
	const token = "pat-na1-supersecrettoken00000000"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"message":"bad request, saw %s"}`, r.Header.Get("Authorization"))))
	}))
	defer srv.Close()

	cred := tokenapi.Credential{Token: token}
	sec, err := cred.Secret()
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, srv, sec)
	_, err = h.invoke(t, "list_deals", nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("token leaked into error: %v", err)
	}
}

// ---- CheckStatus ----

func TestCheckStatusOKAndError(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer ok.Close()
	cred := tokenapi.Credential{Token: "pat-na1-testtoken1234567890"}
	sec, err := cred.Secret()
	if err != nil {
		t.Fatal(err)
	}
	optsOK := &tokenapi.Options{BaseURL: ok.URL, HTTPClient: ok.Client(), Sleep: noSleep}
	if err := checkStatusWithOptions(context.Background(), sec, optsOK); err != nil {
		t.Fatalf("CheckStatus: %v", err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer bad.Close()
	optsBad := &tokenapi.Options{BaseURL: bad.URL, HTTPClient: bad.Client(), Sleep: noSleep}
	if err := checkStatusWithOptions(context.Background(), sec, optsBad); err == nil {
		t.Fatal("expected an error from a 401")
	}
}
