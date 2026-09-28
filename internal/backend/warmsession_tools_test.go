package backend

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"water/internal/tools"
)

func TestWarmSessionWiresToolPolicyIntoMCPConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	argvFile := filepath.Join(dir, "argv.txt")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then echo \"" + warmHelp + " --mcp-config --allowedTools\"; exit 0; fi\n" +
		"echo \"$@\" > " + shellQuote(argvFile) + "\n" +
		"n=0\n" +
		"while IFS= read -r line; do\n" +
		"  n=$((n+1))\n" +
		"  printf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"turn-%s\"}\\n' \"$n\"\n" +
		"done\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	pol := &tools.Policy{
		Role:       "ceo",
		Twin:       []tools.TwinFunction{{ID: "fake_mail.list_messages", Tool: "fake_mail__list_messages", Description: "List messages.", Schema: []byte(`{"type":"object","properties":{}}`)}},
		TwinSocket: filepath.Join(dir, "water.sock"),
		TwinToken:  "tok-abc",
	}
	w := NewWarmSession(WarmSessionConfig{Bin: bin, ScratchDir: dir, SelfExe: "/usr/bin/true"})
	defer w.Close()

	if _, err := w.RunTurn(context.Background(), Request{System: "s", Prompt: "hi", Tools: pol}, nil); err != nil {
		t.Fatal(err)
	}

	argv, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(argv)
	if !contains(got, "--mcp-config") {
		t.Fatalf("argv did not include --mcp-config: %q", got)
	}
	if !contains(got, "--allowedTools") || !contains(got, "mcp__water__fake_mail__list_messages") {
		t.Fatalf("argv did not include the twin tool in --allowedTools: %q", got)
	}
}
