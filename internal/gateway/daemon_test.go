package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/connectors"
	"water/internal/connectors/fake"
	"water/internal/gate"
	"water/internal/gate/permit"
	"water/internal/runtime"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

const testManifest = `
id: test
name: Test twin
usage: {window: 1h, model_calls: 50, auto_model_calls: 10}
connectors:
  - name: fake_mail
    functions:
      - {name: list_messages, level: R}
      - {name: draft_reply, level: D}
      - {name: send_email, level: A}
  - name: notes
    functions:
      - {name: save_note, level: S}
auto_allowlist: [fake_mail.list_messages, fake_mail.draft_reply]
`

// notes is a minimal S-level connector, so tests can exercise the
// tainted-S-escalates-to-approval path (the fake_* connectors have no S
// function).
type notes struct{ saved []string }

func (*notes) Name() string                 { return "notes" }
func (*notes) Credential() (string, string) { return "", "" }
func (*notes) Functions() []connectors.Function {
	return []connectors.Function{
		{Name: "save_note", Level: twins.S, Risk: connectors.RiskLow,
			Schema: connectors.Schema{Properties: map[string]connectors.Property{"text": {Type: "string"}}, Required: []string{"text"}}},
	}
}
func (n *notes) Invoke(_ context.Context, p permit.Permit) (json.RawMessage, error) {
	call, err := p.Open()
	if err != nil {
		return nil, err
	}
	n.saved = append(n.saved, call.Args["text"].(string))
	return json.RawMessage(`{}`), nil
}
func (*notes) Normalize(string, json.RawMessage) ([]store.Record, error) { return nil, nil }

type harness struct {
	d     *Daemon
	srv   *httptest.Server
	token string
	st    *store.Store
	log   *audit.Log
	q     *approvals.Queue
	fake  *backend.Fake
	mail  *fake.Mail
	notes *notes
	dir   string
}

func newHarness(t *testing.T) *harness {
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

	mail := fake.NewMail(fake.Message{ID: "m1", From: "dana@acme.com", To: []string{"ceo@water.dev"}, Subject: "Hi", Body: "hello"})
	nt := &notes{}
	reg, err := connectors.NewRegistry(mail, nt)
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(testManifest))
	if err != nil {
		t.Fatal(err)
	}
	v := vault.NewMemory()
	if err := v.Set(fake.MailService, fake.MailAccount, vault.NewSecret("tok-123")); err != nil {
		t.Fatal(err)
	}
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: v, Store: st})
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
	d := New(Config{Manifest: m, Store: st, Audit: log, Approvals: q, Gate: g, Registry: reg, Backend: fb, Clients: clients, SocketPath: "unused-in-http-tests.sock"})
	srv := httptest.NewServer(d.Mux())
	t.Cleanup(srv.Close)
	return &harness{d: d, srv: srv, token: tok, st: st, log: log, q: q, fake: fb, mail: mail, notes: nt, dir: dir}
}

func (h *harness) post(t *testing.T, path, body, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readEvents(t *testing.T, resp *http.Response) []runtime.Event {
	t.Helper()
	defer resp.Body.Close()
	var out []runtime.Event
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var e runtime.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("bad event line %q: %v", sc.Text(), err)
		}
		out = append(out, e)
	}
	return out
}

func TestTurnStreamsDeltasBeforeDone(t *testing.T) {
	h := newHarness(t)
	h.fake.Reply = func(req backend.Request) string { return "hello there friend" }
	resp := h.post(t, "/v1/turns", `{"channel":"cli","prompt":"tell me something"}`, h.token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	events := readEvents(t, resp)
	if len(events) < 3 || events[0].Kind != runtime.EventAck {
		t.Fatalf("events = %+v", events)
	}
	doneIdx := -1
	for i, e := range events {
		if e.Kind == runtime.EventDone {
			doneIdx = i
			break
		}
	}
	if doneIdx == -1 {
		t.Fatal("no done event")
	}
	deltas := 0
	for _, e := range events[:doneIdx] {
		if e.Kind == runtime.EventDelta {
			deltas++
		}
	}
	if deltas == 0 {
		t.Fatal("expected at least one delta before done")
	}
}

func TestFastPathTurnMakesZeroProviderCalls(t *testing.T) {
	h := newHarness(t)
	resp := h.post(t, "/v1/turns", `{"channel":"cli","prompt":"any pending approvals"}`, h.token)
	events := readEvents(t, resp)
	if len(events) == 0 || events[len(events)-1].Kind != runtime.EventDone {
		t.Fatalf("events = %+v", events)
	}
	if h.fake.Calls() != 0 {
		t.Fatalf("fast path made %d provider calls", h.fake.Calls())
	}
}

func TestMissingOrBadTokenIsRejected(t *testing.T) {
	h := newHarness(t)
	if resp := h.post(t, "/v1/turns", `{"prompt":"hi"}`, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d", resp.StatusCode)
	}
	if resp := h.post(t, "/v1/turns", `{"prompt":"hi"}`, "not-a-real-token"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: status = %d", resp.StatusCode)
	}
}

// blockingBackend never finishes on its own; only ctx cancellation ends it.
type blockingBackend struct{ started chan struct{} }

func (b *blockingBackend) Name() string { return "block" }
func (b *blockingBackend) Available(context.Context) backend.Availability {
	return backend.Availability{Installed: true, Authed: true}
}
func (b *blockingBackend) Run(ctx context.Context, req backend.Request) (backend.Response, error) {
	close(b.started)
	<-ctx.Done()
	return backend.Response{}, ctx.Err()
}
func (b *blockingBackend) RunStream(ctx context.Context, req backend.Request, onDelta func(string)) (backend.Response, error) {
	onDelta("starting")
	close(b.started)
	<-ctx.Done()
	return backend.Response{}, ctx.Err()
}

func TestCancelStopsAnInFlightTurn(t *testing.T) {
	h := newHarness(t)
	bb := &blockingBackend{started: make(chan struct{})}
	h.d.cfg.Backend = bb

	req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/v1/turns", strings.NewReader(`{"channel":"cli","prompt":"do a long thing"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	taskID := resp.Header.Get("X-Water-Task-Id")
	if taskID == "" {
		t.Fatal("no task id header")
	}

	done := make(chan []runtime.Event, 1)
	go func() { done <- readEvents(t, resp) }()

	select {
	case <-bb.started:
	case <-time.After(2 * time.Second):
		t.Fatal("backend never started")
	}
	cancelResp := h.post(t, "/v1/tasks/"+taskID+"/cancel", "", h.token)
	if cancelResp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d", cancelResp.StatusCode)
	}

	select {
	case events := <-done:
		found := false
		for _, e := range events {
			if e.Kind == runtime.EventError {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected an error event after cancel, got %+v", events)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("turn did not stop after cancel")
	}
}

func TestHealthIsUnauthenticated(t *testing.T) {
	h := newHarness(t)
	resp, err := http.Get(h.srv.URL + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestListenSocketPermsAndSingleInstance(t *testing.T) {
	// A short-lived dir outside t.TempDir(): the unix socket path length
	// limit (~104 bytes on macOS) is easily blown by TempDir's long,
	// test-name-qualified paths.
	dir, err := os.MkdirTemp("", "water-home")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	paths := Paths{Home: dir}
	l1, unlock1, err := Listen(paths)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { l1.Close(); unlock1() }()

	info, err := os.Stat(paths.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, want 0600", info.Mode().Perm())
	}
	rdir, err := os.Stat(paths.RunDir())
	if err != nil {
		t.Fatal(err)
	}
	if rdir.Mode().Perm() != 0o700 {
		t.Fatalf("run dir mode = %v, want 0700", rdir.Mode().Perm())
	}

	if _, _, err := Listen(paths); err == nil {
		t.Fatal("a second daemon instance was allowed to start")
	}
}
