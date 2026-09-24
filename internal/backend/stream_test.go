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

// fakeStreamCLI writes a shell script that answers --help with the flags a
// streaming-capable claude advertises, then emits partial-message deltas
// before its result line.
func fakeStreamCLI(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then\n" +
		"  echo \"--print --output-format --system-prompt --tools --strict-mcp-config --include-partial-messages --input-format --verbose --model\"\n" +
		"  exit 0\n" +
		"fi\n" + body
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestRunStreamDeliversDeltasBeforeResult(t *testing.T) {
	bin := fakeStreamCLI(t, `
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hel"}}}'
sleep 0.02
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}}}'
sleep 0.02
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"Hello"}'
`)
	var deltas []string
	var resultSeen bool
	c := &ClaudeSubscription{Bin: bin}
	resp, err := c.RunStream(context.Background(), Request{System: "s", Prompt: "p"}, func(d string) {
		if resultSeen {
			t.Fatal("delta arrived after result was already available")
		}
		deltas = append(deltas, d)
	})
	if err != nil {
		t.Fatal(err)
	}
	resultSeen = true
	if strings.Join(deltas, "") != "Hello" {
		t.Fatalf("deltas = %q, want Hello", strings.Join(deltas, ""))
	}
	if resp.Text != "Hello" {
		t.Fatalf("resp.Text = %q", resp.Text)
	}
}

func TestRunStreamCancelKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "late.txt")
	bin := fakeStreamCLI(t, `
(sleep 1; printf late > `+quote(marker)+`) &
child=$!
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"go"}}}'
wait $child
`)
	c := &ClaudeSubscription{Bin: bin}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_, err := c.RunStream(ctx, Request{System: "s", Prompt: "p", Timeout: 50 * time.Millisecond}, nil)
	if err == nil {
		t.Fatal("expected an error from a killed stream")
	}
	time.Sleep(300 * time.Millisecond)
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("cancelled stream left a child running: %v", statErr)
	}
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
