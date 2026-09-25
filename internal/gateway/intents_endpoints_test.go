package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/connectors"
	"water/internal/connectors/fake"
	"water/internal/gate"
	"water/internal/nervous"
	"water/internal/nervous/intents"
	"water/internal/nervous/promote"
	"water/internal/nervous/reflex"
	"water/internal/nervous/render"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

const intentsTestSharedYAML = "skip_words: [please]\n"

// newIntentsHarness is newRoutingHarness's sibling, wired with
// Home/PromotionEnabled/MaxLearned/ReloadIntents (R-23) so POST
// /v1/intents/draft|reload are actually reachable. Its registry starts
// with no embedded intents beyond the required _shared.yaml (this task's
// endpoints only need a real *store.Store and a real *nervous.Nervous to
// reload into, not any particular embedded intent).
func newIntentsHarness(t *testing.T, promotionEnabled bool) (*harness, *nervous.Nervous) {
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

	mail := fake.NewMail()
	nt := &notes{}
	sa := newSlowAct()
	connReg, err := connectors.NewRegistry(mail, nt, sa)
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(testManifest))
	if err != nil {
		t.Fatal(err)
	}
	v := vault.NewMemory()
	g, err := gate.New(gate.Config{Manifest: m, Registry: connReg, Approvals: q, Audit: log, Vault: v, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	fb := backend.NewFake("fake")
	clients, err := LoadClients(filepath.Join(dir, "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := clients.EnsureCLI()
	if err != nil {
		t.Fatal(err)
	}

	fsys := fstest.MapFS{"twins/test/intents/_shared.yaml": &fstest.MapFile{Data: []byte(intentsTestSharedYAML)}}
	fns := intents.Functions{Read: reflex.Specs()}
	intentsReg, err := intents.LoadRegistry(fsys, m, fns, intents.LoadOptions{})
	if err != nil {
		t.Fatalf("intents.LoadRegistry: %v", err)
	}
	nvCfg := nervous.DefaultConfig()
	nvCfg.Registry = func() *intents.Registry { return intentsReg }
	nvCfg.Style = render.DefaultStyle()
	nvCfg.Store = st
	nv, err := nervous.New(nvCfg)
	if err != nil {
		t.Fatalf("nervous.New: %v", err)
	}

	// reload mirrors internal/cli/cmd_daemon.go's daemonIntentsReloader,
	// scoped to this test's fixed fsys/manifest.
	reload := func(ctx context.Context) error {
		fresh, err := intents.LoadRegistry(fsys, m, fns, intents.LoadOptions{})
		if err != nil {
			return err
		}
		nv.Reload(fresh)
		return nil
	}

	d := New(Config{
		Manifest: m, Store: st, Audit: log, Approvals: q, Gate: g, Registry: connReg, Backend: fb,
		Clients: clients, SocketPath: "unused-in-http-tests.sock", Nervous: nv,
		Home: dir, PromotionEnabled: promotionEnabled, MaxLearned: promote.DefaultMaxLearned,
		ReloadIntents: reload,
	})
	srv := httptest.NewServer(d.Mux())
	t.Cleanup(srv.Close)
	return &harness{d: d, srv: srv, token: tok, st: st, log: log, q: q, fake: fb, mail: mail, dir: dir}, nv
}

const draftReplyYAML = "id: whatever-the-model-called-it\n" +
	"description: what's up next\n" +
	"kind: read\n" +
	"function: store.next_event\n" +
	"reflex_eligible: true\n" +
	"escalate_if: [slot_unresolved, ambiguous_match]\n" +
	"templates:\n" +
	"  - \"what's up next for me\"\n" +
	"tests:\n" +
	"  - {utterance: \"what's up next for me\", intent: learned.placeholder}\n" +
	"  - {utterance: \"some unrelated phrase\", intent: \"none\"}\n"

func firstCandidateID(t *testing.T, h *harness) string {
	t.Helper()
	resp := h.get(t, "/v1/route/candidates", h.token)
	defer resp.Body.Close()
	var cands []promote.Candidate
	if err := json.NewDecoder(resp.Body).Decode(&cands); err != nil {
		t.Fatalf("decode /v1/route/candidates: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("candidates = %+v, want exactly 1", cands)
	}
	return cands[0].ID
}

// TestIntentsDraftEndpoint: a real candidate, a fake model reply, one cold
// model call, a file written under Home's pending/ directory, and a
// ValidateLearned verdict returned alongside the YAML.
func TestIntentsDraftEndpoint(t *testing.T) {
	h, _ := newIntentsHarness(t, true)
	h.fake.Reply = func(req backend.Request) string { return draftReplyYAML }

	now := time.Now()
	insertCandidateFixture(t, h, "d1", now.Add(-48*time.Hour), "quick.next_event", 3)
	insertCandidateFixture(t, h, "d2", now.Add(-24*time.Hour), "quick.next_event", 2)
	candID := firstCandidateID(t, h)

	resp := h.post(t, "/v1/intents/draft", `{"candidate_id":"`+candID+`"}`, h.token)
	if resp.StatusCode != http.StatusOK {
		body := readBody(t, resp)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var out struct {
		ID              string `json:"id"`
		YAML            string `json:"yaml"`
		Path            string `json:"path"`
		Valid           bool   `json:"valid"`
		ValidationError string `json:"validation_error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()

	if h.fake.Calls() != 1 {
		t.Fatalf("provider calls = %d, want exactly 1 (one cold draft call)", h.fake.Calls())
	}
	if out.ID == "" || out.ID[:len("learned.")] != "learned." {
		t.Fatalf("id = %q, want a learned.* id", out.ID)
	}
	if !out.Valid {
		t.Fatalf("draft did not validate: %s", out.ValidationError)
	}
	wantPath := filepath.Join(h.dir, "twins", "test", "intents", "pending", candID+".yaml")
	if out.Path != wantPath {
		t.Fatalf("path = %q, want %q", out.Path, wantPath)
	}
	b, err := os.ReadFile(out.Path)
	if err != nil {
		t.Fatalf("read pending file: %v", err)
	}
	if string(b) != out.YAML {
		t.Fatalf("file content does not match the response YAML")
	}
}

func TestIntentsDraftRefusesWhenPromotionDisabled(t *testing.T) {
	h, _ := newIntentsHarness(t, false)
	resp := h.post(t, "/v1/intents/draft", `{"candidate_id":"anything"}`, h.token)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (router.promotion.enabled=false)", resp.StatusCode)
	}
}

func TestIntentsDraftUnknownCandidate(t *testing.T) {
	h, _ := newIntentsHarness(t, true)
	resp := h.post(t, "/v1/intents/draft", `{"candidate_id":"does-not-exist"}`, h.token)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestIntentsDraftMultiToolCandidateRefused(t *testing.T) {
	h, _ := newIntentsHarness(t, true)
	now := time.Now()
	insertCandidateFixture(t, h, "d1", now.Add(-48*time.Hour), "quick.calendar+quick.next_event", 3)
	insertCandidateFixture(t, h, "d2", now.Add(-24*time.Hour), "quick.calendar+quick.next_event", 2)
	candID := firstCandidateID(t, h)

	resp := h.post(t, "/v1/intents/draft", `{"candidate_id":"`+candID+`"}`, h.token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (multi-tool candidate)", resp.StatusCode)
	}
}

// TestIntentsReloadEndpoint: a successful reload reports the registry's
// hash and candidate count.
func TestIntentsReloadEndpoint(t *testing.T) {
	h, _ := newIntentsHarness(t, true)
	resp := h.post(t, "/v1/intents/reload", `{}`, h.token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if out["reloaded"] != true {
		t.Fatalf("reloaded = %v, want true", out["reloaded"])
	}
	if _, ok := out["hash"]; !ok {
		t.Fatalf("response missing hash: %+v", out)
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var buf [4096]byte
	n, _ := resp.Body.Read(buf[:])
	return string(buf[:n])
}
