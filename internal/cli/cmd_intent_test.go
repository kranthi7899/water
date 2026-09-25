package cli

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"water/internal/gateway"
)

// TestRenderIntentListFromCannedJSON exercises the CLI's own rendering
// logic directly against canned IntentListItem data (the same
// "rendering from canned JSON" pattern this file's sibling commands use),
// rather than standing up a daemon.
func TestRenderIntentListFromCannedJSON(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		got := renderIntentList(nil)
		if !strings.Contains(got, "no intents") {
			t.Fatalf("renderIntentList(nil) = %q, want a no-intents message", got)
		}
	})

	t.Run("mixed states", func(t *testing.T) {
		items := []IntentListItem{
			{ID: "schedule.on_date", Origin: "embedded", Active: true},
			{ID: "learned.mail_from_priya", Origin: "learned", Active: true},
			{ID: "control.stop", Origin: "embedded", Active: false, InactiveReason: "write action not granted"},
			{ID: "learned.noisy_one", Origin: "learned", Active: true, Disabled: "auto-demoted: miss rate 25% over 12 samples"},
		}
		got := renderIntentList(items)
		for _, want := range []string{
			"ID", "ORIGIN", "STATE", "REASON",
			"schedule.on_date", "embedded", "active",
			"learned.mail_from_priya", "learned",
			"control.stop", "inactive", "write action not granted",
			"learned.noisy_one", "disabled", "auto-demoted: miss rate 25% over 12 samples",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("renderIntentList output missing %q:\n%s", want, got)
			}
		}
		lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
		if len(lines) != 5 { // header + 4 rows
			t.Fatalf("got %d lines, want 5 (header + 4 rows):\n%s", len(lines), got)
		}
	})
}

// TestRenderIntentDraftFromCannedJSON covers both the valid and invalid
// verdicts `water intent draft` can print.
func TestRenderIntentDraftFromCannedJSON(t *testing.T) {
	valid := renderIntentDraft(IntentDraftResult{
		ID: "learned.next_event", YAML: "id: learned.next_event\n", Path: "/home/pending/cand1.yaml", Valid: true,
	})
	for _, want := range []string{"id: learned.next_event", "/home/pending/cand1.yaml", "valid"} {
		if !strings.Contains(valid, want) {
			t.Errorf("valid draft render missing %q:\n%s", want, valid)
		}
	}

	invalid := renderIntentDraft(IntentDraftResult{
		ID: "learned.next_event", YAML: "id: learned.next_event\n", Path: "/home/pending/cand1.yaml",
		Valid: false, ValidationError: "ambiguous with schedule.on_date",
	})
	if !strings.Contains(invalid, "invalid") || !strings.Contains(invalid, "ambiguous with schedule.on_date") {
		t.Errorf("invalid draft render missing verdict/reason:\n%s", invalid)
	}
}

// TestPromptYesNo is the direct, deterministic proof behind `water intent
// promote`'s "refuses ... on N" requirement: promptYesNo is the exact
// function confirmYesNo defers to once --yes is absent and stdin is a TTY,
// so exercising it directly (rather than faking a pty) is the reliable way
// to prove the parsing itself refuses on "N" and everything but an explicit
// yes.
func TestPromptYesNo(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{"  y  \n", true},
		{"n\n", false},
		{"N\n", false},
		{"no\n", false},
		{"\n", false},
		{"", false},
		{"maybe\n", false},
	}
	for _, tc := range cases {
		var out strings.Builder
		got := promptYesNo(strings.NewReader(tc.in), &out, "promote cand1?")
		if got != tc.want {
			t.Errorf("promptYesNo(%q) = %v, want %v", tc.in, got, tc.want)
		}
		if !strings.Contains(out.String(), "promote cand1? [y/N] ") {
			t.Errorf("promptYesNo(%q) did not print the expected prompt: %q", tc.in, out.String())
		}
	}
}

// TestConfirmYesNoRespectsGlobalYesFlag: --yes always answers yes without
// ever touching stdin.
func TestConfirmYesNoRespectsGlobalYesFlag(t *testing.T) {
	a := &App{flags: globalFlags{yes: true}}
	if !a.confirmYesNo("anything?") {
		t.Fatal("--yes must always confirm")
	}
}

// fakeDaemon serves mux over a real Unix socket at WATER_HOME's expected
// path (the same one newDaemonClient dials), so a command under test can go
// through its normal, unmodified newDaemonClient() call. It never checks
// the Authorization header — CLI-side token minting is exercised elsewhere
// (quality_sweep_test.go); here only the command's own request logic and
// response handling are under test.
func fakeDaemon(t *testing.T, mux *http.ServeMux) {
	t.Helper()
	home := shortHome(t)
	sock := gateway.Paths{Home: home}.SocketPath()
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
}

// TestIntentPromoteRefusesWhenFlagIsOff: `water intent promote` checks GET
// /v1/router's promotion_enabled field before ever asking for confirmation,
// and refuses immediately (ExitUsage) when it's false — never reaching the
// pending-file read or the y/N prompt.
func TestIntentPromoteRefusesWhenFlagIsOff(t *testing.T) {
	var promoteHit bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/router", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"promotion_enabled": false,
			"tier0":             map[string]any{"state": "closed"},
		})
	})
	mux.HandleFunc("POST /v1/intents/promote", func(w http.ResponseWriter, r *http.Request) {
		promoteHit = true
		w.WriteHeader(http.StatusOK)
	})
	fakeDaemon(t, mux)

	cmd := NewApp().intentPromoteCmd()
	cmd.SetArgs([]string{"aaaaaaaaaaaa"}) // a validly-shaped (12 hex) candidate id
	err := cmd.Execute()
	if err == nil {
		t.Fatal("want an error when router.promotion.enabled=false")
	}
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v (%T), want an *exitError", err, err)
	}
	if ee.code != ExitUsage {
		t.Fatalf("exit code = %d, want ExitUsage (%d)", ee.code, ExitUsage)
	}
	if !strings.Contains(err.Error(), "router.promotion.enabled=false") {
		t.Fatalf("error message = %q, want it to name the disabled flag", err.Error())
	}
	if promoteHit {
		t.Fatal("POST /v1/intents/promote must never be called when the flag is off")
	}
}

// TestIntentPromoteRefusesWithoutPendingDraft: with the flag on but no
// pending draft file on disk for the candidate, promote refuses before ever
// reaching the confirmation prompt or the promote endpoint.
func TestIntentPromoteRefusesWithoutPendingDraft(t *testing.T) {
	var promoteHit bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/router", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"promotion_enabled": true, "tier0": map[string]any{"state": "closed"}})
	})
	mux.HandleFunc("POST /v1/intents/promote", func(w http.ResponseWriter, r *http.Request) {
		promoteHit = true
		w.WriteHeader(http.StatusOK)
	})
	fakeDaemon(t, mux)

	cmd := NewApp().intentPromoteCmd()
	cmd.SetArgs([]string{"bbbbbbbbbbbb"}) // a validly-shaped (12 hex) candidate id with no pending file
	err := cmd.Execute()
	if err == nil {
		t.Fatal("want an error with no pending draft file")
	}
	if promoteHit {
		t.Fatal("POST /v1/intents/promote must never be called with no pending draft")
	}
}

// TestIntentPromoteRejectsPathTraversalCandidateID: an id shaped like a
// path-traversal attempt is refused before ever touching the filesystem or
// contacting the daemon, mirroring the same check
// handleIntentsPromote enforces server-side.
func TestIntentPromoteRejectsPathTraversalCandidateID(t *testing.T) {
	for _, bad := range []string{"../outside-secret", "../../etc/passwd", "not-hex-id"} {
		cmd := NewApp().intentPromoteCmd()
		cmd.SetArgs([]string{bad})
		err := cmd.Execute()
		if err == nil {
			t.Fatalf("candidate id %q: want an error", bad)
		}
		var ee *exitError
		if !errors.As(err, &ee) || ee.code != ExitUsage {
			t.Fatalf("candidate id %q: err = %v, want ExitUsage", bad, err)
		}
	}
}
