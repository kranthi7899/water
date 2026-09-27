package gateway

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"water"
	"water/internal/audit"
	"water/internal/dashboards"
	"water/internal/store"
	"water/internal/workspaces"
)

// newRegistryTestDaemon builds the smallest real daemon GET /v1/workspaces
// and GET /v1/dashboards need: a client token for auth, and the real CEO
// twin's workspaces/dashboards registries loaded from the embedded FS
// exactly as internal/cli's buildTwinDepsFS does at daemon startup (never
// from ~/.water, and never re-validating or hand-rolling the YAML — this
// task's own brief says to read the already-loaded registry, not reload
// it). Neither handler needs a gate, a backend or Nervous, so this harness
// leaves them nil, unlike newHarness.
func newRegistryTestDaemon(t *testing.T) (srv *httptest.Server, token string) {
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

	wsReg, err := workspaces.LoadRegistry(water.TwinsFS(), "ceo")
	if err != nil {
		t.Fatalf("workspaces.LoadRegistry: %v", err)
	}
	dashReg, err := dashboards.LoadRegistry(water.TwinsFS(), "ceo")
	if err != nil {
		t.Fatalf("dashboards.LoadRegistry: %v", err)
	}
	clients, err := LoadClients(filepath.Join(dir, "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := clients.EnsureCLI()
	if err != nil {
		t.Fatal(err)
	}
	d := New(Config{Store: st, Audit: log, Clients: clients, SocketPath: "unused-in-http-tests.sock", Workspaces: wsReg, Dashboards: dashReg})
	srv = httptest.NewServer(d.Mux())
	t.Cleanup(srv.Close)
	return srv, tok
}

// newRegistryTestDaemonWithCompute is newRegistryTestDaemon plus a
// dashboards.Compute wired to a stub Invoker that answers every gate call
// with body — for GET /v1/dashboards/{id} tests that need real computed
// tiles, not just the list route's identity/name/source.
func newRegistryTestDaemonWithCompute(t *testing.T, body string) (srv *httptest.Server, token string) {
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

	dashReg, err := dashboards.LoadRegistry(water.TwinsFS(), "ceo")
	if err != nil {
		t.Fatalf("dashboards.LoadRegistry: %v", err)
	}
	clients, err := LoadClients(filepath.Join(dir, "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := clients.EnsureCLI()
	if err != nil {
		t.Fatal(err)
	}
	compute := &dashboards.Compute{Gate: stubInvoker{body: body}, Cache: &dashboards.Cache{TTL: dashboards.DefaultCacheTTL}}
	d := New(Config{Store: st, Audit: log, Clients: clients, SocketPath: "unused-in-http-tests.sock", Dashboards: dashReg, Compute: compute})
	srv = httptest.NewServer(d.Mux())
	t.Cleanup(srv.Close)
	return srv, tok
}

// TestGetWorkspacesListsTheLoadedRegistryInOrder: the real CEO twin's
// twins/ceo/workspaces/*.yaml specs come back id, name, template, source, in
// the registry's own (id) order — clients, crawler, econ-rag, finance,
// ideas, kevin, marketing, people, research, voice-text, water,
// yt-recamendo — and the route requires the client token.
func TestGetWorkspacesListsTheLoadedRegistryInOrder(t *testing.T) {
	srv, tok := newRegistryTestDaemon(t)

	if resp := do(t, srv.URL, "GET", "/v1/workspaces", "", ""); statusOf(resp) != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", statusOf(resp))
	}

	var out []workspaceListItem
	decodeInto(t, do(t, srv.URL, "GET", "/v1/workspaces", "", tok), http.StatusOK, &out)

	wantIDs := []string{"clients", "crawler", "econ-rag", "finance", "ideas", "kevin", "marketing", "people", "research", "voice-text", "water", "yt-recamendo"}
	if len(out) != len(wantIDs) {
		t.Fatalf("workspaces = %+v, want %d entries", out, len(wantIDs))
	}
	for i, id := range wantIDs {
		if out[i].ID != id {
			t.Errorf("workspaces[%d].ID = %q, want %q", i, out[i].ID, id)
		}
	}
	// One project and one domain workspace's exact fields, straight from
	// their YAML (internal/workspaces.Spec), never recomputed here.
	byID := map[string]workspaceListItem{}
	for _, w := range out {
		byID[w.ID] = w
	}
	if got := byID["water"]; got.Name != "Water" || got.Template != "project" || got.Source != "linear_team:WAT" {
		t.Errorf("water workspace = %+v", got)
	}
	if got := byID["clients"]; got.Name != "Clients" || got.Template != "clients" || got.Source != "company_customers" {
		t.Errorf("clients workspace = %+v", got)
	}
}

// TestGetWorkspacesWithNoRegistryReportsEmpty mirrors GET /v1/today's own
// "optional cross-cutting dependency" posture: a daemon with no Workspaces
// registry configured answers an empty list, not an error.
func TestGetWorkspacesWithNoRegistryReportsEmpty(t *testing.T) {
	h := newHarness(t)
	var out []workspaceListItem
	decodeInto(t, do(t, h.srv.URL, "GET", "/v1/workspaces", "", h.token), http.StatusOK, &out)
	if len(out) != 0 {
		t.Fatalf("workspaces = %+v, want empty with no registry configured", out)
	}
}
