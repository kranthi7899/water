package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/connectors"
	"water/internal/decisions"
	"water/internal/gate"
	"water/internal/needsyou"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

// fakeDecisionSource is a canned needsyou.DecisionSource, mirroring
// internal/needsyou's own unexported test fakeSource (not reusable across
// the package boundary).
type fakeDecisionSource struct {
	cards []*decisions.Card
}

func (f *fakeDecisionSource) Run(ctx context.Context, now time.Time) ([]*decisions.Card, error) {
	return f.cards, nil
}

// TestGetTodayReturnsNeedsYouAndSchedule seeds a fake DecisionSource with one
// crossing decision and the store with one event starting today, ticks a
// real needsyou.Service once, and confirms GET /v1/today surfaces both in
// the documented shape, and that the route requires the client token.
func TestGetTodayReturnsNeedsYouAndSchedule(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	q := approvals.NewQueue(st, log)

	// A minimal manifest with no connector functions at all (mirroring
	// decisions_test.go's TestGetDecisionsProducesAGenericCardThroughTheRealPath):
	// this test's decision card comes from the fake DecisionSource below,
	// not from a real classification/gate path, so nothing here needs a
	// registered connector.
	m, err := twins.Parse([]byte("id: t\nname: Test twin\nusage: {window: 1h, model_calls: 50, auto_model_calls: 10}\n"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := connectors.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	v := vault.NewMemory()
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: v, Store: st})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	// A crossing decision card: severity 3 >= the min_severity (2) threshold
	// below.
	src := &fakeDecisionSource{cards: []*decisions.Card{
		{ID: "card-1", Severity: 3, Lead: "A decision that needs you"},
	}}
	svc := needsyou.NewService(src, q, st, 2, 0)
	if err := svc.Tick(context.Background(), now); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// An event starting later today, so it falls inside todayBounds(now).
	if err := st.Upsert(context.Background(), &store.Event{
		Meta:     store.Meta{Source: "fake", SourceID: "ev1", CreatedAt: now, UpdatedAt: now},
		Title:    "Standup",
		StartAt:  now.Add(time.Hour),
		EndAt:    now.Add(90 * time.Minute),
		Location: "Zoom",
	}); err != nil {
		t.Fatal(err)
	}

	clients, err := LoadClients(filepath.Join(dir, "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := clients.EnsureCLI()
	if err != nil {
		t.Fatal(err)
	}
	d := New(Config{
		Manifest: m, Store: st, Audit: log, Approvals: q, Gate: g, Registry: reg, Backend: backend.NewFake("fake"),
		Clients: clients, SocketPath: "unused-in-http-tests.sock", Nervous: testNervous(t, m, st),
		NeedsYou: svc,
	})
	srv := httptest.NewServer(d.Mux())
	t.Cleanup(srv.Close)

	// No token: 401.
	if resp := doTodayGet(t, srv.URL, ""); resp != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want %d", resp, http.StatusUnauthorized)
	}
	// Bad token: 401.
	if resp := doTodayGet(t, srv.URL, "bogus"); resp != http.StatusUnauthorized {
		t.Fatalf("bad token: status = %d, want %d", resp, http.StatusUnauthorized)
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/today", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out todayResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(out.NeedsYou) != 1 || out.NeedsYou[0].ID != "card-1" {
		t.Fatalf("needs_you = %+v, want exactly one item with id card-1", out.NeedsYou)
	}
	if out.NeedsYou[0].Kind != needsyou.KindDecision {
		t.Fatalf("needs_you[0].Kind = %q, want %q", out.NeedsYou[0].Kind, needsyou.KindDecision)
	}
	if len(out.Schedule) != 1 || out.Schedule[0].Title != "Standup" {
		t.Fatalf("schedule = %+v, want exactly one Standup entry", out.Schedule)
	}
	if out.Schedule[0].Location != "Zoom" {
		t.Fatalf("schedule[0].Location = %q, want %q", out.Schedule[0].Location, "Zoom")
	}
	if out.GeneratedAt.IsZero() {
		t.Fatal("generated_at is zero")
	}
}

// TestGetTodayCarriesItemOrigin confirms needsyou.Item.Origin (Phase 3a)
// actually reaches the wire: an untrusted decision card's Item comes back
// with the "Built from an outside email" note, under the same "Origin" key
// existing needs-you clients already read (get(it, 'Origin', 'origin') in
// view_today.js).
func TestGetTodayCarriesItemOrigin(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	q := approvals.NewQueue(st, log)
	m, err := twins.Parse([]byte("id: t\nname: Test twin\nusage: {window: 1h, model_calls: 50, auto_model_calls: 10}\n"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := connectors.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: vault.NewMemory(), Store: st})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	src := &fakeDecisionSource{cards: []*decisions.Card{
		{ID: "card-untrusted", Severity: 3, Lead: "An external ask", Untrusted: true},
	}}
	svc := needsyou.NewService(src, q, st, 2, 0)
	if err := svc.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	clients, err := LoadClients(filepath.Join(dir, "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := clients.EnsureCLI()
	if err != nil {
		t.Fatal(err)
	}
	d := New(Config{
		Manifest: m, Store: st, Audit: log, Approvals: q, Gate: g, Registry: reg, Backend: backend.NewFake("fake"),
		Clients: clients, SocketPath: "unused-in-http-tests.sock", Nervous: testNervous(t, m, st), NeedsYou: svc,
	})
	srv := httptest.NewServer(d.Mux())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/today", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw struct {
		NeedsYou []map[string]any `json:"needs_you"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.NeedsYou) != 1 {
		t.Fatalf("needs_you = %+v, want exactly one item", raw.NeedsYou)
	}
	if got := raw.NeedsYou[0]["Origin"]; got != "Built from an outside email" {
		t.Fatalf("Origin = %v, want %q", got, "Built from an outside email")
	}
}

// doTodayGet issues GET /v1/today with the given bearer token (empty means
// no Authorization header at all) and returns the response status code.
func doTodayGet(t *testing.T, baseURL, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/today", nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestGetTodayWithNoNeedsYouServiceReportsEmpty confirms a daemon with no
// needsyou.Service wired (Config.NeedsYou nil, the same "optional
// cross-cutting dependency" posture Config.Decisions has) answers with an
// empty needs_you list rather than an error, using the package's own
// default harness (newHarness never sets NeedsYou).
func TestGetTodayWithNoNeedsYouServiceReportsEmpty(t *testing.T) {
	h := newHarness(t)
	req, err := http.NewRequest(http.MethodGet, h.srv.URL+"/v1/today", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out todayResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.NeedsYou) != 0 {
		t.Fatalf("needs_you = %+v, want empty with no service configured", out.NeedsYou)
	}
}
