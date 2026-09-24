package cli

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/connectors"
	"water/internal/connectors/fake"
	"water/internal/gate"
	"water/internal/gateway"
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
	d := gateway.New(gateway.Config{Manifest: m, Store: st, Audit: log, Approvals: q, Gate: g, Registry: reg, Backend: fakeBackend, Clients: clients, SocketPath: paths.SocketPath()})
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
	if fb.Calls() != 0 {
		t.Fatalf("fast path made %d provider calls", fb.Calls())
	}
}
