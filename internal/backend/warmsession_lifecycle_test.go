package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"water/internal/tools"
)

// writeWarmCLI writes a fake claude CLI whose --help advertises warmHelp
// (plus extra) and whose body is script, run once per process start.
func writeWarmCLI(t *testing.T, extraHelp, body string) string {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then echo \"" + warmHelp + " " + extraHelp + "\"; exit 0; fi\n" +
		body
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

// slowFirstCLI answers a line containing "slow" only after a delay, and any
// other line at once, naming the prompt kind and its own PID, so a test can
// tell which turn's result a RunTurn call actually returned.
func slowFirstCLI(t *testing.T) string {
	return writeWarmCLI(t, "", ""+
		"while IFS= read -r line; do\n"+
		"  case \"$line\" in\n"+
		"    *slow*) sleep 1; printf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"reply-slow-pid-%s\"}\\n' \"$$\" ;;\n"+
		"    *) printf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"reply-fast-pid-%s\"}\\n' \"$$\" ;;\n"+
		"  esac\n"+
		"done\n")
}

// TestWarmSessionCancelledTurnDoesNotDesyncNextTurn is the regression for a
// cancelled turn leaving its unread output in the pipe: the next turn must
// get its own reply, never the cancelled turn's late result line.
func TestWarmSessionCancelledTurnDoesNotDesyncNextTurn(t *testing.T) {
	bin := slowFirstCLI(t)
	w := NewWarmSession(WarmSessionConfig{Bin: bin})
	defer w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := w.RunTurn(ctx, Request{System: "s", Prompt: "slow"}, nil); err == nil {
		t.Fatal("expected the cancelled turn to fail")
	}
	resp, err := w.RunTurn(context.Background(), Request{System: "s", Prompt: "fast"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resp.Text, "reply-fast-") {
		t.Fatalf("turn 2 got %q; want its own reply, not the cancelled turn's", resp.Text)
	}
}

// hangCLI reads turns and never answers: a model stream that stalled.
func hangCLI(t *testing.T) string {
	return writeWarmCLI(t, "", "while IFS= read -r line; do :; done\n")
}

func TestWarmSessionHonorsRequestTimeout(t *testing.T) {
	w := NewWarmSession(WarmSessionConfig{Bin: hangCLI(t)})
	defer w.Close()

	start := time.Now()
	_, err := w.RunTurn(context.Background(), Request{System: "s", Prompt: "x", Timeout: 200 * time.Millisecond}, nil)
	if !errors.Is(err, ErrCallTimeout) {
		t.Fatalf("err = %v, want ErrCallTimeout", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("RunTurn took %s, want about the 200ms timeout", d)
	}
}

// TestWarmSessionWaiterHonorsContextAndClearInterruptsAHungTurn: a turn
// queued behind a hung one must give up when its own context ends, and
// /clear must be able to break the hung turn rather than queueing forever.
func TestWarmSessionWaiterHonorsContextAndClearInterruptsAHungTurn(t *testing.T) {
	w := NewWarmSession(WarmSessionConfig{Bin: hangCLI(t)})
	defer w.Close()

	hungErr := make(chan error, 1)
	go func() {
		_, err := w.RunTurn(context.Background(), Request{System: "s", Prompt: "x", Timeout: time.Minute}, nil)
		hungErr <- err
	}()
	// Let the hung turn take the session.
	deadline := time.Now().Add(3 * time.Second)
	for !w.busyForTest() {
		if time.Now().After(deadline) {
			t.Fatal("hung turn never started")
		}
		time.Sleep(10 * time.Millisecond)
	}

	wctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := w.RunTurn(wctx, Request{System: "s", Prompt: "y"}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter err = %v, want its own context's deadline", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("waiter blocked %s behind a hung turn", d)
	}

	cleared := make(chan struct{})
	go func() { w.Clear(); close(cleared) }()
	select {
	case <-cleared:
	case <-time.After(3 * time.Second):
		t.Fatal("Clear blocked behind a hung turn")
	}
	select {
	case err := <-hungErr:
		if err == nil {
			t.Fatal("hung turn should have failed when cleared")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hung turn never returned after Clear")
	}
}

// floodCLI answers "flood" with many partial-message lines and no result,
// then hangs, so a cancelled turn leaves far more than the reader channel's
// capacity unread.
func floodCLI(t *testing.T) string {
	return writeWarmCLI(t, "", ""+
		"while IFS= read -r line; do\n"+
		"  i=0\n"+
		"  while [ $i -lt 300 ]; do\n"+
		"    printf '{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}}\\n'\n"+
		"    i=$((i+1))\n"+
		"  done\n"+
		"done\n")
}

func TestWarmSessionKillDoesNotLeakTheReaderGoroutine(t *testing.T) {
	w := NewWarmSession(WarmSessionConfig{Bin: floodCLI(t)})
	defer w.Close()
	base := goruntime.NumGoroutine()

	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		n := 0
		_, _ = w.RunTurn(ctx, Request{System: "s", Prompt: "flood"}, func(string) {
			n++
			if n == 5 {
				cancel()
			}
		})
		cancel()
		// Let the CLI finish flooding, so far more than the reader
		// channel's capacity is left unread when the process is killed.
		time.Sleep(300 * time.Millisecond)
		w.Clear()
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if g := goruntime.NumGoroutine(); g <= base+1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d, baseline %d: the stdout reader leaked", goruntime.NumGoroutine(), base)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestWarmSessionCrashMidTurnRemovesToolPolicyFiles: a process that dies
// mid-turn must not leave its policy/MCP files (which hold the session's
// proxy token) behind when the next turn starts a new process.
func TestWarmSessionCrashMidTurnRemovesToolPolicyFiles(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran-once")
	bin := writeWarmCLI(t, "--mcp-config --allowedTools", ""+
		"if [ -f "+shellQuote(marker)+" ]; then\n"+
		"  while IFS= read -r line; do printf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"ok\"}\\n'; done\n"+
		"else\n"+
		"  touch "+shellQuote(marker)+"\n"+
		"  read -r line\n"+
		"  exit 1\n"+
		"fi\n")
	scratch := filepath.Join(dir, "scratch")
	pol := &tools.Policy{
		Role:       "ceo",
		Twin:       []tools.TwinFunction{{ID: "fake_mail.list_messages", Tool: "fake_mail__list_messages", Description: "List.", Schema: []byte(`{"type":"object","properties":{}}`)}},
		TwinSocket: filepath.Join(dir, "water.sock"),
		TwinToken:  "tok-abc",
	}
	w := NewWarmSession(WarmSessionConfig{Bin: bin, ScratchDir: scratch, SelfExe: "/usr/bin/true"})
	defer w.Close()

	if _, err := w.RunTurn(context.Background(), Request{System: "s", Prompt: "boom", Tools: pol}, nil); err == nil {
		t.Fatal("expected the crashing turn to fail")
	}
	if _, err := w.RunTurn(context.Background(), Request{System: "s", Prompt: "again", Tools: pol}, nil); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(scratch, "policy-*.json"))
	var policies []string
	for _, f := range files {
		if !strings.HasSuffix(f, ".mcp.json") {
			policies = append(policies, f)
		}
	}
	if len(policies) != 1 {
		t.Fatalf("policy files after crash + restart = %v, want exactly the live process's one", policies)
	}
}

// TestWarmArgsCarryTheColdPathsHardeningFlags keeps the warm and cold
// argument builders from drifting on the isolation flags: session
// persistence off and slash commands off whenever the CLI offers them.
func TestWarmArgsCarryTheColdPathsHardeningFlags(t *testing.T) {
	fs := flagSet{"--print": true, "--input-format": true, "--output-format": true, "--system-prompt": true,
		"--tools": true, "--strict-mcp-config": true, "--no-session-persistence": true, "--disable-slash-commands": true}
	warm, err := buildWarmArgs(fs, Request{System: "s"}, "")
	if err != nil {
		t.Fatal(err)
	}
	cold, err := (&ClaudeSubscription{}).BuildArgs(fs, Request{System: "s", Prompt: "p"}, "")
	if err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{"warm": warm, "cold": cold} {
		joined := strings.Join(args, "\x00")
		for _, f := range append([]string{"--no-session-persistence", "--disable-slash-commands"}, LoadBearingFlags...) {
			if !strings.Contains(joined, f) {
				t.Fatalf("%s args missing %s: %q", name, f, args)
			}
		}
	}
}
