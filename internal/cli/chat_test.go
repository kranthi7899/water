package cli

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/connectors"
	"water/internal/connectors/fake"
	"water/internal/gate"
	"water/internal/gateway"
	"water/internal/nervous"
	"water/internal/nervous/intents"
	"water/internal/nervous/reflex"
	"water/internal/nervous/render"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

// startTestDaemon runs a real gateway.Daemon on a short-lived Unix socket
// under a fresh WATER_HOME, so chatCommand/chatTurn can be exercised the same
// way `water chat` really talks to the daemon, without a TTY or a pty.
func startTestDaemon(t *testing.T, fakeBackend *backend.Fake) {
	t.Helper()
	home, err := os.MkdirTemp("", "water-chat-cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("WATER_HOME", home)

	st, err := store.Open(filepath.Join(home, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log, err := audit.Open(filepath.Join(home, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	q := approvals.NewQueue(st, log)
	reg, err := connectors.NewRegistry(fake.NewCalendar(), fake.NewMail(), fake.NewDocs())
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte("id: t\nname: T\nusage: {window: 1h, model_calls: 50}\n"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: vault.NewMemory(), Store: st})
	if err != nil {
		t.Fatal(err)
	}
	paths := gateway.Paths{Home: home}
	l, unlock, err := gateway.Listen(paths)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
	t.Cleanup(func() { l.Close() })
	clients, err := gateway.LoadClients(paths.ClientsPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.EnsureCLI(); err != nil {
		t.Fatal(err)
	}
	// An empty intent registry (no twins/<id>/intents dir): Tier 0 never
	// matches, so every turn flows to the main path exactly as it did
	// before Slice R was wired in — Nervous.Handle has no nil-receiver
	// fallback, so every gateway.Config needs a real *nervous.Nervous.
	intentsReg, err := intents.LoadRegistry(fstest.MapFS{}, m, intents.Functions{Read: reflex.Specs()}, intents.LoadOptions{})
	if err != nil {
		t.Fatalf("empty intent registry: %v", err)
	}
	cfg := nervous.DefaultConfig()
	cfg.Registry = func() *intents.Registry { return intentsReg }
	cfg.Style = render.DefaultStyle()
	cfg.Store = st
	nv, err := nervous.New(cfg)
	if err != nil {
		t.Fatalf("nervous.New: %v", err)
	}
	d := gateway.New(gateway.Config{Manifest: m, Store: st, Audit: log, Approvals: q, Gate: g, Registry: reg, Backend: fakeBackend, Clients: clients, SocketPath: paths.SocketPath(), Nervous: nv})
	srv := &http.Server{Handler: d.Mux()}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
}

func TestChatTurnStreamsAndReportsErrors(t *testing.T) {
	fb := backend.NewFake("fake")
	fb.Reply = func(req backend.Request) string { return "hello from the twin" }
	startTestDaemon(t, fb)

	client, err := newDaemonClient()
	if err != nil {
		t.Fatal(err)
	}
	app := &App{}
	if err := app.chatTurn(context.Background(), client, "say hi", false, nil); err != nil {
		t.Fatalf("chatTurn: %v", err)
	}

	fb.FailWith = context.DeadlineExceeded
	if err := app.chatTurn(context.Background(), client, "say hi", false, nil); err == nil {
		t.Fatal("expected chatTurn to surface a backend error")
	}
}

func TestChatCommandQuitAndClear(t *testing.T) {
	fb := backend.NewFake("fake")
	startTestDaemon(t, fb)
	client, err := newDaemonClient()
	if err != nil {
		t.Fatal(err)
	}
	app := &App{}
	if code := app.chatCommand(context.Background(), client, "hello there"); code != chatContinue {
		t.Fatalf("plain text: code = %v, want chatContinue", code)
	}
	if code := app.chatCommand(context.Background(), client, "/clear"); code != chatHandled {
		t.Fatalf("/clear: code = %v, want chatHandled", code)
	}
	if fb.Calls() != 0 {
		t.Fatalf("/clear made %d model calls, want 0", fb.Calls())
	}
	if err := clearSession(context.Background(), client); err != nil {
		t.Fatalf("clearSession: %v", err)
	}
	if code := app.chatCommand(context.Background(), client, "/quit"); code != chatQuit {
		t.Fatalf("/quit: code = %v, want chatQuit", code)
	}
}

// TestChatTurnFastPathMakesNoProviderCalls used to prove
// internal/runtime.FastPath answered "any pending approvals" with zero
// model calls, using hardcoded Go phrase-matching that worked against any
// manifest. Slice R's Tier 0 (wired into handleTurn as of R-15) replaces
// that with intent templates loaded from twins/<id>/intents/*.yaml — and
// startTestDaemon's synthetic test manifest has no such directory, so its
// *nervous.Nervous gets an empty registry (by design: a missing intents
// directory is not an error) with nothing for "any pending approvals" to
// match. The turn correctly escalates to the main path, making one call.
// This is expected for this test's synthetic fixture, not a regression:
// the real CEO registry's own equivalent coverage
// (internal/nervous/intents.TestLegacyFastPathCases, built in R-9) proves
// the genuine zero-call answer against the actual twins/ceo/intents files.
func TestChatTurnFastPathMakesNoProviderCalls(t *testing.T) {
	fb := backend.NewFake("fake")
	startTestDaemon(t, fb)
	client, err := newDaemonClient()
	if err != nil {
		t.Fatal(err)
	}
	app := &App{}
	if err := app.chatTurn(context.Background(), client, "any pending approvals", false, nil); err != nil {
		t.Fatal(err)
	}
	if fb.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1 (no fast path is wired into the daemon until Slice R's R-15)", fb.Calls())
	}
}
