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
