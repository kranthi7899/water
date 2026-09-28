package cli

import (
	"context"
	"encoding/xml"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"water/internal/backend"
	"water/internal/gateway"
)

// shortHome makes a WATER_HOME short enough that a Unix socket under it
// stays inside sun_path's 104-byte limit on macOS.
func shortHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "wq")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("WATER_HOME", home)
	return home
}

// Every persistent flag must map to config keys Load accepts: a stale
// override key (the retired telemetry.trace_dir) fails every command.
func TestRootConfigRunsWithPersistentFlags(t *testing.T) {
	shortHome(t)
	for _, args := range [][]string{
		{"config"},
		{"--backend", "claude-subscription", "config"},
		{"--allow-metered", "config", "get", "backend.allow_metered"},
	} {
		if code := Execute(args); code != ExitOK {
			t.Fatalf("water %v exited %d", args, code)
		}
	}
	for _, dead := range []string{"trace", "quiet", "verbose"} {
		if NewApp().rootCmd().PersistentFlags().Lookup(dead) != nil {
			t.Fatalf("--%s is registered but nothing reads it", dead)
		}
	}
}

func TestNewDaemonClientStaleSocketIsNotRunning(t *testing.T) {
	home := shortHome(t)
	sock := gateway.Paths{Home: home}.SocketPath()
	if _, err := newDaemonClient(); !errors.Is(err, errDaemonNotRunning) {
		t.Fatalf("no socket: err = %v, want errDaemonNotRunning", err)
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := newDaemonClient()
	if !errors.Is(err, errDaemonNotRunning) || !strings.Contains(err.Error(), "stale socket") {
		t.Fatalf("stale socket file: err = %v, want errDaemonNotRunning naming the stale socket", err)
	}
	if got := probeDaemon(sock); got != daemonStale {
		t.Fatalf("probe = %v, want stale", got)
	}
	os.Remove(sock)
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if got := probeDaemon(sock); got != daemonUp {
		t.Fatalf("probe with a listener = %v, want up", got)
	}
}

// A daemon that dies mid-session must surface the start-it hint, not a raw
// dial error.
func TestDaemonClientDoMapsRefusedToNotRunning(t *testing.T) {
	home := shortHome(t)
	sock := gateway.Paths{Home: home}.SocketPath()
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newDaemonClient()
	if err != nil {
		t.Fatal(err)
	}
	l.Close() // the daemon goes away; Go unlinks the socket
	_, err = c.do(context.Background(), "GET", "/v1/health", nil)
	if !errors.Is(err, errDaemonNotRunning) {
		t.Fatalf("err = %v, want errDaemonNotRunning", err)
	}
}

// A first-run onboard has no daemon; setup still succeeded.
func TestChatAfterOnboardWithoutDaemonSucceeds(t *testing.T) {
	shortHome(t)
	if err := NewApp().chatAfterOnboard(context.Background()); err != nil {
		t.Fatalf("chatAfterOnboard with no daemon = %v, want nil", err)
	}
}

func TestOnboardSelectHonoursFlagAndPreference(t *testing.T) {
	ctx := context.Background()
	reg := backend.NewRegistry()
	reg.Register(backend.NewFake("claude-subscription"))
	reg.Register(backend.NewFake("codex-subscription"))
	metered := backend.NewFake("api")
	metered.Avail.Metered = true
	reg.Register(metered)

	sel, warn, err := onboardSelect(ctx, reg, "codex-subscription", "")
	if err != nil || sel.Backend.Name() != "codex-subscription" || warn != "" {
		t.Fatalf("flag: %v %q %v", sel.Backend, warn, err)
	}
	sel, warn, err = onboardSelect(ctx, reg, "", "codex-subscription")
	if err != nil || sel.Backend.Name() != "codex-subscription" || warn != "" {
		t.Fatalf("preferred: %v %q %v", sel.Backend, warn, err)
	}
	sel, _, err = onboardSelect(ctx, reg, "", "auto")
	if err != nil || sel.Backend.Name() != "claude-subscription" {
		t.Fatalf("auto: %v %v", sel.Backend, err)
	}
	if _, _, err := onboardSelect(ctx, reg, "api", ""); err == nil {
		t.Fatal("onboard must never select a metered backend, even when asked")
	}
	if _, _, err := onboardSelect(ctx, reg, "nope", ""); err == nil {
		t.Fatal("an explicit unknown --backend must fail, not fall back")
	}
	sel, warn, err = onboardSelect(ctx, reg, "", "gone-backend")
	if err != nil || sel.Backend.Name() != "claude-subscription" || warn == "" {
		t.Fatalf("stale preference should fall back to auto with a warning: %v %q %v", sel.Backend, warn, err)
	}
}

func TestRenderLaunchAgentPlistCarriesWaterHomeOnlyWhenSet(t *testing.T) {
	base := launchAgentPlistArgs{Label: "l", WaterBin: "/w", ExtraPathDir: "/x", StdoutLog: "/o", StderrLog: "/e"}
	out := renderLaunchAgentPlist(base)
	if strings.Contains(out, "WATER_HOME") || strings.Contains(out, demoEnvVar) {
		t.Fatalf("default install must not pin WATER_HOME/WATER_DEMO:\n%s", out)
	}
	base.WaterHome, base.Demo = "/Users/ceo/water-dev", true
	out = renderLaunchAgentPlist(base)
	var v any
	if err := xml.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("not well-formed XML: %v\n%s", err, out)
	}
	for _, want := range []string{"<key>WATER_HOME</key>", "<string>/Users/ceo/water-dev</string>", "<key>WATER_DEMO</key>", "<key>PATH</key>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("plist missing %q:\n%s", want, out)
		}
	}
}
