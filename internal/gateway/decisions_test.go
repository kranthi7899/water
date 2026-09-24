package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/connectors"
	"water/internal/decisions"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

// TestGetDecisionsProducesAGenericCardThroughTheRealPath is the end-to-end
// test the task asks for: a fixture item that matches no registered decision
// type still produces a valid "generic" card, exercised through the actual
// daemon wiring (a real *decisions.Registry, *decisions.Trigger and the
// GET /v1/decisions HTTP handler) rather than only decisions' own unit
// tests. A minimal manifest (no connector functions at all) keeps generic's
// own needs empty, so the card resolves to Ready with no gate calls needed.
func TestGetDecisionsProducesAGenericCardThroughTheRealPath(t *testing.T) {
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
	reg, err := connectors.NewRegistry() // no connectors: nothing to grant
	if err != nil {
		t.Fatal(err)
	}
	v := vault.NewMemory()
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: v, Store: st})
	if err != nil {
		t.Fatal(err)
	}

	// An empty in-memory filesystem: no twins/t/decisions directory, so
	// LoadRegistry returns an empty (generic-only) registry, exactly as it
	// does for a real twin that has not shipped any decision types yet.
	decisionsReg, err := decisions.LoadRegistry(fstest.MapFS{}, m)
	if err != nil {
		t.Fatal(err)
	}

	// A fixture message the "needs attention" candidate heuristic flags (a
	// question, a respond-by deadline), from a sender no registered type
	// would match — there are no registered types at all here, so Classify
	// has nothing to pick but generic.
	msg := &store.Message{
		Meta:    store.Meta{Source: "gmail", SourceID: "msg-1", External: true, CreatedAt: time.Now()},
		From:    "dana@example.com",
		Subject: "Speaking invite",
		Body:    "Could you speak at our conference? Please respond by Friday.",
	}
	if err := st.Upsert(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	fb := backend.NewFake("fake")
	fb.Reply = func(backend.Request) string {
		// The classifier's system prompt asks for exactly this shape; with
		// no registered types, there is nothing to name but generic.
		return `{"needs_decision": true, "type_id": "generic", "confidence": 0.9}`
	}
	classifier := &decisions.ModelClassifier{Registry: decisionsReg, Backend: fb, Model: "fake"}
	cached := &decisions.StoreCache{Store: st, Inner: classifier}
	triager, err := decisions.NewTriager(cached, decisions.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	builder := &decisions.Builder{Registry: decisionsReg, Gate: g, Origin: gate.P1}
	trigger := &decisions.Trigger{Store: st, Triager: triager, Builder: builder}

	clients, err := LoadClients(filepath.Join(dir, "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := clients.EnsureCLI()
	if err != nil {
		t.Fatal(err)
	}
	d := New(Config{
		Manifest: m, Store: st, Audit: log, Approvals: q, Gate: g, Registry: reg, Backend: fb,
		Decisions: trigger, Clients: clients, SocketPath: "unused-in-http-tests.sock",
	})
	srv := httptest.NewServer(d.Mux())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/decisions", nil)
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
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var cards []*decisions.Card
	if err := json.NewDecoder(resp.Body).Decode(&cards); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("got %d cards, want exactly 1: %+v", len(cards), cards)
	}
	c := cards[0]
	if c.TypeID != decisions.GenericID {
		t.Fatalf("TypeID = %q, want %q", c.TypeID, decisions.GenericID)
	}
	if c.Readiness == "" {
		t.Fatal("Readiness is empty")
	}
	if !c.Untrusted {
		t.Fatal("Untrusted should be true: the source item is External")
	}
	if len(c.Evidence) == 0 {
		t.Fatal("expected at least the item itself as evidence")
	}
}

// TestGetDecisionsWithNoRegistryReportsEmpty confirms a daemon with no
// decision registry wired (Config.Decisions nil, as every pre-Slice-C test
// in this package already leaves it) answers with an empty list rather than
// an error.
func TestGetDecisionsWithNoRegistryReportsEmpty(t *testing.T) {
	h := newHarness(t)
	req, err := http.NewRequest(http.MethodGet, h.srv.URL+"/v1/decisions", nil)
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
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var cards []*decisions.Card
	if err := json.NewDecoder(resp.Body).Decode(&cards); err != nil {
		t.Fatal(err)
	}
	if len(cards) != 0 {
		t.Fatalf("got %d cards with no registry configured, want 0", len(cards))
	}
}
