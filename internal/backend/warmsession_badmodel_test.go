package backend

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// badModelCLI is a fake claude that rejects any --model. mode "result"
// answers each turn with an is_error "model not found" result and stays up;
// mode "exit" prints the rejection on stderr and exits after reading the
// turn. Without --model it answers normally. Every launch appends its
// arguments to a log so a test can see which models were tried.
func badModelCLI(t *testing.T, mode string) (bin, argLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	bin = filepath.Join(dir, "claude")
	argLog = filepath.Join(dir, "args.log")
	reject := `    printf '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"model not found: retired-model"}\n'` + "\n"
	if mode == "exit" {
		reject = `    echo "Error: model 'retired-model' not found" >&2; exit 1` + "\n"
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then echo \"" + warmHelp + "\"; exit 0; fi\n" +
		"echo \"$*\" >> " + shellQuote(argLog) + "\n" +
		"bad=0\n" +
		"for a in \"$@\"; do [ \"$a\" = \"--model\" ] && bad=1; done\n" +
		"while IFS= read -r line; do\n" +
		"  if [ $bad = 1 ]; then\n" + reject +
		"  else\n" +
		`    printf '{"type":"result","subtype":"success","is_error":false,"result":"ok-default"}\n'` + "\n" +
		"  fi\n" +
		"done\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, argLog
}

// TestWarmSessionRetriesWithoutRejectedModel is the review finding: the cold
// path strips a model the CLI rejects and retries, but the warm session
// failed every turn for the daemon's life. The retry must succeed, and later
// turns must not pass the rejected model again.
func TestWarmSessionRetriesWithoutRejectedModel(t *testing.T) {
	for _, mode := range []string{"result", "exit"} {
		t.Run(mode, func(t *testing.T) {
			bin, argLog := badModelCLI(t, mode)
			w := NewWarmSession(WarmSessionConfig{Bin: bin})
			defer w.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			for i := 0; i < 3; i++ {
				resp, err := w.RunTurn(ctx, Request{System: "s", Prompt: "hi", Model: "retired-model"}, nil)
				if err != nil {
					t.Fatalf("turn %d: %v", i, err)
				}
				if resp.Text != "ok-default" {
					t.Fatalf("turn %d text = %q, want ok-default", i, resp.Text)
				}
			}
			b, err := os.ReadFile(argLog)
			if err != nil {
				t.Fatal(err)
			}
			launches := strings.Split(strings.TrimSpace(string(b)), "\n")
			withModel := 0
			for _, l := range launches {
				if strings.Contains(l, "--model") {
					withModel++
				}
			}
			if withModel != 1 {
				t.Fatalf("launches with --model = %d, want exactly 1 (the rejected first try): %q", withModel, launches)
			}
			if len(launches) != 2 {
				t.Fatalf("launches = %d, want 2 (rejected, then one reused default-model process): %q", len(launches), launches)
			}
		})
	}
}

// TestWarmSessionWaitForSlotHonorsTimeout: a turn waiting behind another
// gives up after its own Timeout, not only when ctx ends.
func TestWarmSessionWaitForSlotHonorsTimeout(t *testing.T) {
	w := NewWarmSession(WarmSessionConfig{Bin: "claude-not-used"})
	w.sem <- struct{}{} // another turn holds the session
	defer w.release()
	start := time.Now()
	_, err := w.RunTurn(context.Background(), Request{System: "s", Prompt: "x", Timeout: 100 * time.Millisecond}, nil)
	if err == nil {
		t.Fatal("expected the waiting turn to give up")
	}
	if !strings.Contains(err.Error(), "busy") {
		t.Fatalf("err = %v, want a session-busy error", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("waited %v, want about the 100ms timeout", d)
	}
}
