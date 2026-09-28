package guards_test

import (
	"strings"
	"testing"

	"water/internal/backend"
	"water/internal/tools"
)

// TestStrictMCPConfigSurvives — every claude invocation carries
// --strict-mcp-config and --tools "", with and without Water's MCP tools
// (the twin's connector functions, proxied to the daemon's gate).
func TestStrictMCPConfigSurvives(t *testing.T) {
	c := &backend.ClaudeSubscription{}
	fs := map[string]bool{"--print": true, "--output-format": true, "--system-prompt": true, "--tools": true, "--strict-mcp-config": true, "--mcp-config": true, "--allowedTools": true, "--input-format": true, "--model": true, "--no-session-persistence": true}
	for _, mcp := range []string{"", "/tmp/x.mcp.json"} {
		req := backend.Request{System: "s", Prompt: "p"}
		if mcp != "" {
			schema := []byte(`{"type":"object","properties":{}}`)
			req.Tools = &tools.Policy{Role: "ceo", Twin: []tools.TwinFunction{{ID: "fake_docs.read_doc", Tool: "fake_docs__read_doc", Description: "Read a document.", Schema: schema}}, TwinSocket: "/tmp/w.sock", TwinToken: "tok"}
		}
		args, err := c.BuildArgs(backend.FlagSetForTest(fs), req, mcp)
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(args, "\x00")
		for _, f := range backend.LoadBearingFlags {
			if !strings.Contains(joined, f) {
				t.Fatalf("missing %s in %q", f, args)
			}
		}
		// --tools must be the EMPTY list.
		for i, a := range args {
			if a == "--tools" && args[i+1] != "" {
				t.Fatalf("--tools is %q, want empty", args[i+1])
			}
		}
		if mcp != "" {
			if !strings.Contains(joined, "--mcp-config\x00"+mcp) || !strings.Contains(joined, "mcp__water__fake_docs__read_doc") {
				t.Fatalf("mcp wiring missing: %q", args)
			}
		} else if strings.Contains(joined, "--mcp-config") {
			t.Fatal("no tools → no mcp-config")
		}
	}
}

func TestClaudeIsolationFlagsAreMandatory(t *testing.T) {
	base := map[string]bool{"--print": true, "--output-format": true, "--system-prompt": true, "--tools": true, "--strict-mcp-config": true}
	for _, missing := range backend.LoadBearingFlags {
		fs := map[string]bool{}
		for k, v := range base {
			fs[k] = v
		}
		delete(fs, missing)
		if _, err := (&backend.ClaudeSubscription{}).BuildArgs(backend.FlagSetForTest(fs), backend.Request{System: "s", Prompt: "p"}, ""); err == nil {
			t.Fatalf("missing %s did not fail closed", missing)
		}
	}
}
