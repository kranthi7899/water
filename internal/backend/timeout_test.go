package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func TestClaudeTimeoutWinsOverResultLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := `#!/bin/sh
if [ "$1" = "--help" ]; then
  echo "--print --output-format --system-prompt --tools --strict-mcp-config"
  exit 0
fi
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"premature success"}'
sleep 2
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := (&ClaudeSubscription{Bin: bin}).Run(context.Background(), Request{System: "s", Prompt: "p", Timeout: 100 * time.Millisecond})
	if !errors.Is(err, ErrCallTimeout) {
		t.Fatalf("timeout with result line returned %v", err)
	}
}

func TestRunScrubbedTimeoutKillsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "late.txt")
	script := fmt.Sprintf("(sleep 1; printf late > %s) & wait", strconv.Quote(marker))
	_, _, err := runScrubbed(context.Background(), 100*time.Millisecond, "", "", "/bin/sh", "-c", script)
	if !errors.Is(err, ErrCallTimeout) {
		t.Fatalf("timeout returned %v", err)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("timed-out subprocess left a child running: %v", err)
	}
}
