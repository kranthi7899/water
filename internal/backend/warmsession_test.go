package backend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const warmHelp = "--print --output-format --system-prompt --tools --strict-mcp-config --input-format --output-format --verbose --model"

// pidEchoCLI answers each stdin line with a result naming this process's PID
// and a per-process turn counter, so a test can tell whether two turns were
// served by the same OS process (warm reuse) or different ones (restart).
func pidEchoCLI(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then echo \"" + warmHelp + "\"; exit 0; fi\n" +
		"n=0\n" +
		"while IFS= read -r line; do\n" +
		"  n=$((n+1))\n" +
		"  printf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"turn-%s-pid-%s\"}\\n' \"$n\" \"$$\"\n" +
		"done\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestWarmSessionReusesOneProcess(t *testing.T) {
	bin := pidEchoCLI(t)
	w := NewWarmSession(WarmSessionConfig{Bin: bin})
	defer w.Close()

	resp1, err := w.RunTurn(context.Background(), Request{System: "s", Prompt: "one"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp2, err := w.RunTurn(context.Background(), Request{System: "s", Prompt: "two"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp1.Text == "" || resp2.Text == "" {
		t.Fatalf("empty responses: %q %q", resp1.Text, resp2.Text)
	}
	if resp1.Text == resp2.Text {
		t.Fatalf("turns produced identical text: %q", resp1.Text)
	}
	var n1, pid1, n2, pid2 int
	if _, err := fmt.Sscanf(resp1.Text, "turn-%d-pid-%d", &n1, &pid1); err != nil {
		t.Fatalf("parse resp1: %v (%q)", err, resp1.Text)
	}
	if _, err := fmt.Sscanf(resp2.Text, "turn-%d-pid-%d", &n2, &pid2); err != nil {
		t.Fatalf("parse resp2: %v (%q)", err, resp2.Text)
	}
	if pid1 != pid2 {
		t.Fatalf("turns used different processes: pid %d then %d", pid1, pid2)
	}
	if n1 != 1 || n2 != 2 {
		t.Fatalf("turn counters = %d, %d; want 1, 2 (same process incrementing)", n1, n2)
	}
}

func TestWarmSessionRestartsAfterMaxTurns(t *testing.T) {
	bin := pidEchoCLI(t)
	w := NewWarmSession(WarmSessionConfig{Bin: bin, MaxTurns: 1})
	defer w.Close()

	var pids []int
	for i := 0; i < 3; i++ {
		resp, err := w.RunTurn(context.Background(), Request{System: "s", Prompt: "x"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var n, pid int
		if _, err := fmt.Sscanf(resp.Text, "turn-%d-pid-%d", &n, &pid); err != nil {
			t.Fatalf("parse: %v (%q)", err, resp.Text)
		}
		if n != 1 {
			t.Fatalf("turn %d: counter = %d, want 1 (fresh process each time)", i, n)
		}
		pids = append(pids, pid)
	}
	if pids[0] == pids[1] || pids[1] == pids[2] {
		t.Fatalf("MaxTurns=1 did not restart the process: pids %v", pids)
	}
}

func TestWarmSessionRestartsAfterCrash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	marker := filepath.Join(dir, "ran-once")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then echo \"" + warmHelp + "\"; exit 0; fi\n" +
		"if [ -f " + shellQuote(marker) + " ]; then\n" +
		"  n=0\n" +
		"  while IFS= read -r line; do\n" +
		"    n=$((n+1))\n" +
		"    printf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"ok-%s\"}\\n' \"$n\"\n" +
		"  done\n" +
		"else\n" +
		"  touch " + shellQuote(marker) + "\n" +
		"  read -r line\n" +
		"  exit 1\n" +
		"fi\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	w := NewWarmSession(WarmSessionConfig{Bin: bin})
	defer w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := w.RunTurn(ctx, Request{System: "s", Prompt: "boom"}, nil); err == nil {
		t.Fatal("expected the first (crashing) turn to fail")
	}
	resp, err := w.RunTurn(ctx, Request{System: "s", Prompt: "again"}, nil)
	if err != nil {
		t.Fatalf("turn after crash should cold-start cleanly: %v", err)
	}
	if resp.Text != "ok-1" {
		t.Fatalf("resp.Text = %q, want ok-1", resp.Text)
	}
}

func shellQuote(s string) string { return "'" + s + "'" }

// noStdinReadCLI answers --help normally, but if it ever actually reads a
// line from stdin, it drops a marker file: Prewarm must never write a turn
// to stdin, so that marker must never appear no matter how long the process
// runs.
func noStdinReadCLI(t *testing.T, marker string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then echo \"" + warmHelp + "\"; exit 0; fi\n" +
		"read -r line\n" +
		"touch " + shellQuote(marker) + "\n" +
		"printf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"got-stdin\"}\\n'\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

// TestWarmSessionPrewarmStartsWithoutStdin: Prewarm starts a fresh process
// and reports "started", and that process never sees anything on stdin —
// proving, against a real (fake CLI) subprocess, that Prewarm truly never
// sends a turn.
func TestWarmSessionPrewarmStartsWithoutStdin(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "got-stdin")
	bin := noStdinReadCLI(t, marker)
	w := NewWarmSession(WarmSessionConfig{Bin: bin})
	defer w.Close()

	state, err := w.Prewarm(context.Background(), Request{System: "s"})
	if err != nil {
		t.Fatalf("Prewarm: %v", err)
	}
	if state != "started" {
		t.Fatalf("state = %q, want started", state)
	}

	// Give the (nonexistent) write a moment it would need if Prewarm ever
	// mistakenly sent one; the marker must still be absent.
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("Prewarm wrote a turn to the process's stdin")
	}
}

// TestWarmSessionPrewarmBusyWhileTurnInFlight: Prewarm try-acquires the
// semaphore without blocking, so it reports "busy" rather than waiting
// behind a real in-flight turn.
func TestWarmSessionPrewarmBusyWhileTurnInFlight(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	block := filepath.Join(dir, "block")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then echo \"" + warmHelp + "\"; exit 0; fi\n" +
		"read -r line\n" +
		"while [ -f " + shellQuote(block) + " ]; do sleep 0.05; done\n" +
		"printf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"ok\"}\\n'\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(block, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := NewWarmSession(WarmSessionConfig{Bin: bin})
	defer w.Close()

	done := make(chan struct{})
	go func() {
		_, _ = w.RunTurn(context.Background(), Request{System: "s", Prompt: "hold"}, nil)
		close(done)
	}()

	// Wait until the turn actually holds the semaphore before asserting.
	deadline := time.Now().Add(2 * time.Second)
	for !w.busyForTest() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !w.busyForTest() {
		t.Fatal("the blocking turn never took the semaphore")
	}

	state, err := w.Prewarm(context.Background(), Request{System: "s"})
	if err != nil {
		t.Fatalf("Prewarm: %v", err)
	}
	if state != "busy" {
		t.Fatalf("state = %q, want busy", state)
	}

	_ = os.Remove(block)
	<-done
}

// TestWarmSessionPrewarmAliveWhenKeyMatches: a process already live with the
// same system/model/tools key reports "alive" and is left untouched (no
// restart).
func TestWarmSessionPrewarmAliveWhenKeyMatches(t *testing.T) {
	bin := pidEchoCLI(t)
	w := NewWarmSession(WarmSessionConfig{Bin: bin})
	defer w.Close()

	resp1, err := w.RunTurn(context.Background(), Request{System: "s", Prompt: "one"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var n1, pid1 int
	if _, err := fmt.Sscanf(resp1.Text, "turn-%d-pid-%d", &n1, &pid1); err != nil {
		t.Fatalf("parse resp1: %v (%q)", err, resp1.Text)
	}

	state, err := w.Prewarm(context.Background(), Request{System: "s"})
	if err != nil {
		t.Fatalf("Prewarm: %v", err)
	}
	if state != "alive" {
		t.Fatalf("state = %q, want alive", state)
	}

	resp2, err := w.RunTurn(context.Background(), Request{System: "s", Prompt: "two"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var n2, pid2 int
	if _, err := fmt.Sscanf(resp2.Text, "turn-%d-pid-%d", &n2, &pid2); err != nil {
		t.Fatalf("parse resp2: %v (%q)", err, resp2.Text)
	}
	if pid1 != pid2 {
		t.Fatalf("Prewarm restarted the process: pid %d then %d", pid1, pid2)
	}
	if n2 != 2 {
		t.Fatalf("turn counter after Prewarm = %d, want 2 (same process, no extra turn sent)", n2)
	}
}

// TestWarmSessionPrewarmStartedWhenKeyDiffers: a different system prompt
// forces a restart, reported as "started".
func TestWarmSessionPrewarmStartedWhenKeyDiffers(t *testing.T) {
	bin := pidEchoCLI(t)
	w := NewWarmSession(WarmSessionConfig{Bin: bin})
	defer w.Close()

	if _, err := w.RunTurn(context.Background(), Request{System: "s1", Prompt: "one"}, nil); err != nil {
		t.Fatal(err)
	}
	state, err := w.Prewarm(context.Background(), Request{System: "s2"})
	if err != nil {
		t.Fatalf("Prewarm: %v", err)
	}
	if state != "started" {
		t.Fatalf("state = %q, want started", state)
	}
}
