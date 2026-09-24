package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/connectors"
	"water/internal/connectors/google/gmail"
	"water/internal/decisions"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

const emailTestManifest = `
id: t
name: Test twin
usage: {window: 1h, model_calls: 50, auto_model_calls: 10}
connectors:
  - name: gmail
    functions:
      - {name: list_messages, level: R}
      - {name: send_message, level: A}
`

// newEmailTestDaemon builds a daemon wired exactly like
// TestGetDecisionsProducesAGenericCardThroughTheRealPath's, plus a real
// gmail connector (so gmail.send_message is actually grantable) and no
// fixture message seeded yet -- callers add one per test.
func newEmailTestDaemon(t *testing.T) (*Daemon, string, *approvals.Queue, *store.Store) {
	t.Helper()
	return newEmailTestDaemonWith(t, emailTestManifest)
}

// newEmailTestDaemonWith is newEmailTestDaemon over a caller's manifest.
func newEmailTestDaemonWith(t *testing.T, manifest string) (*Daemon, string, *approvals.Queue, *store.Store) {
	t.Helper()
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

	m, err := twins.Parse([]byte(manifest))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := connectors.NewRegistry(gmail.New("agent@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	v := vault.NewMemory()
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: v, Store: st})
	if err != nil {
		t.Fatal(err)
	}

	decisionsReg, err := decisions.LoadRegistry(fstest.MapFS{}, m)
	if err != nil {
		t.Fatal(err)
	}
	fb := backend.NewFake("fake")
	fb.Reply = func(backend.Request) string {
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
	return d, tok, q, st
}

func TestEmailDecisionReportStagesASendMessageApprovalWithTheReportAttached(t *testing.T) {
	d, tok, q, st := newEmailTestDaemon(t)
	srv := httptest.NewServer(d.Mux())
	t.Cleanup(srv.Close)

	msg := &store.Message{
		Meta:    store.Meta{Source: "gmail", SourceID: "msg-1", External: true, CreatedAt: time.Now()},
		From:    "dana@example.com",
		Subject: "Speaking invite",
		Body:    "Could you speak at our conference? Please respond by Friday.",
	}
	if err := st.Upsert(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	cards := getDecisions(t, srv, tok)
	if len(cards) != 1 {
		t.Fatalf("got %d cards, want 1", len(cards))
	}
	id := cards[0].ID

	reqBody, _ := json.Marshal(map[string]any{"to": []string{"ceo@real.example.com"}})
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/decisions/"+id+"/email", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "queued" {
		t.Fatalf("status = %v, want queued: %+v", out["status"], out)
	}
	approvalID, _ := out["approval_id"].(string)
	if approvalID == "" {
		t.Fatal("no approval_id in response")
	}

	pending, err := q.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending approvals = %d, want 1", len(pending))
	}
	env := pending[0]
	if env.ID != approvalID || env.Action != "gmail.send_message" {
		t.Fatalf("envelope = %+v, want id %q action gmail.send_message", env, approvalID)
	}
	to, _ := env.Payload["to"].([]any)
	if len(to) != 1 || to[0] != "ceo@real.example.com" {
		t.Fatalf("to = %+v", env.Payload["to"])
	}
	html, _ := env.Payload["html_attachment"].(string)
	if !strings.Contains(html, "<!doctype html>") || !strings.Contains(html, "Speaking invite") {
		t.Fatalf("html_attachment does not look like a rendered report carrying the card's own content: %q", html)
	}
}

func TestEmailDecisionReportRequiresTo(t *testing.T) {
	d, tok, _, _ := newEmailTestDaemon(t)
	srv := httptest.NewServer(d.Mux())
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/decisions/any-id/email", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestEmailDecisionReportUnknownCardIs404(t *testing.T) {
	d, tok, _, _ := newEmailTestDaemon(t)
	srv := httptest.NewServer(d.Mux())
	t.Cleanup(srv.Close)

	reqBody, _ := json.Marshal(map[string]any{"to": []string{"a@b.com"}})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/decisions/nope/email", bytes.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// getDecisions is a small helper mirroring what `water decisions list` does
// against the real GET /v1/decisions handler.
func getDecisions(t *testing.T, srv *httptest.Server, tok string) []*decisions.Card {
	t.Helper()
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
		t.Fatalf("GET /v1/decisions status = %d", resp.StatusCode)
	}
	var cards []*decisions.Card
	if err := json.NewDecoder(resp.Body).Decode(&cards); err != nil {
		t.Fatal(err)
	}
	return cards
}
