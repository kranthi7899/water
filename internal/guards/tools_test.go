package guards_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"water"
	"water/internal/backend"
	"water/internal/persona"
	"water/internal/roles"
	"water/internal/tools"
)

// TestStrictMCPConfigSurvives — every claude invocation carries
// --strict-mcp-config and --tools "", with and without Water's MCP tools.
func TestStrictMCPConfigSurvives(t *testing.T) {
	c := &backend.ClaudeSubscription{}
	fs := map[string]bool{"--print": true, "--output-format": true, "--system-prompt": true, "--tools": true, "--strict-mcp-config": true, "--mcp-config": true, "--allowedTools": true, "--input-format": true, "--model": true, "--no-session-persistence": true}
	for _, mcp := range []string{"", "/tmp/x.mcp.json"} {
		req := backend.Request{System: "s", Prompt: "p"}
		if mcp != "" {
			req.Tools = &tools.Policy{Role: "cto", Filesystem: tools.FSPolicy{Mode: "read-only", Roots: []string{"/tmp"}}}
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
			if !strings.Contains(joined, "--mcp-config\x00"+mcp) || !strings.Contains(joined, "mcp__water__read_file") {
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

// TestRoleWithoutToolsInvokesNothing — a role with no tools: block exposes
// zero tools and every call is denied and traced.
func TestRoleWithoutToolsInvokesNothing(t *testing.T) {
	reg, err := roles.Load(hierTree(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reg.All() {
		if r.Tools != nil {
			t.Fatalf("%s unexpectedly has tools", r.Slug)
		}
		pol := tools.FromGrant(r.Slug, r.RoleID, r.Tools, nil)
		if !pol.Empty() || len(pol.ToolNames()) != 0 {
			t.Fatalf("%s: empty grant should expose nothing: %v", r.Slug, pol.ToolNames())
		}
		svc := tools.NewService(pol, nil)
		if _, err := svc.Call(context.Background(), tools.ToolReadFile, map[string]any{"path": "/etc/hosts"}); err == nil {
			t.Fatal("read without a grant succeeded")
		}
		evs := svc.Events()
		if len(evs) != 1 || evs[0].Allowed || evs[0].Basis == "" {
			t.Fatalf("denial not traced with a basis: %+v", evs)
		}
	}
}

// TestReadOutsideRootsFails — including via .. and symlinks.
func TestReadOutsideRootsFails(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(root, "ok.txt"), []byte("fine"), 0o644)
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("nope"), 0o644)
	os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt"))
	os.Symlink(outside, filepath.Join(root, "dirlink"))
	pol := &tools.Policy{Role: "cto", Filesystem: tools.FSPolicy{Mode: "read-only", Roots: []string{root}}}
	svc := tools.NewService(pol, nil)
	ctx := context.Background()
	if out, err := svc.Call(ctx, tools.ToolReadFile, map[string]any{"path": "ok.txt"}); err != nil || !strings.HasSuffix(out, "\nfine") || !strings.HasPrefix(out, "[water call_id: call-cto-") {
		t.Fatalf("in-root read: %q %v", out, err)
	}
	for _, p := range []string{
		filepath.Join(root, "..", filepath.Base(outside), "secret.txt"),
		"../" + filepath.Base(outside) + "/secret.txt",
		filepath.Join(outside, "secret.txt"),
		"link.txt",
		"dirlink/secret.txt",
		"/etc/passwd",
	} {
		if _, err := svc.Call(ctx, tools.ToolReadFile, map[string]any{"path": p}); err == nil {
			t.Fatalf("read of %s should be denied", p)
		}
	}
	// read-only denies writes even inside the root.
	if _, err := svc.Call(ctx, tools.ToolWriteFile, map[string]any{"path": "new.txt", "content": "x"}); err == nil {
		t.Fatal("write under read-only succeeded")
	}
	// shell is denied when mode is none.
	if _, err := svc.Call(ctx, tools.ToolRun, map[string]any{"command": "ls"}); err == nil {
		t.Fatal("shell ran with mode none")
	}
	// Every call is in the trace with a decision.
	for _, ev := range svc.Events() {
		if ev.Basis == "" || ev.Tool == "" {
			t.Fatalf("event missing decision basis: %+v", ev)
		}
	}
	// Roots from config are applied only to roles that declare a grant.
	grant := &roles.ToolGrant{}
	grant.Filesystem.Mode = "read-only"
	p2 := tools.FromGrant("cto", "", grant, []string{root})
	if p2.Empty() || len(p2.Filesystem.Roots) != 1 {
		t.Fatalf("config roots not applied: %+v", p2)
	}
	if p3 := tools.FromGrant("ceo", "", nil, []string{root}); !p3.Empty() {
		t.Fatal("roots must not grant anything to a role without a tools block")
	}
}

// TestSkillSchemaValid — every embedded SKILL.md validates against the
// Anthropic schema: name and description present and within caps, no
// angle-bracket tags, body under the line cap. Loading the embedded tree is
// the check (DiscoverSkills fails loudly otherwise).
func TestSkillSchemaValid(t *testing.T) {
	fsys, err := subAgents()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := roles.Load(persona.NewEmbedded(fsys), nil)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, r := range reg.All() {
		if r.Persona == nil {
			continue
		}
		for _, sk := range r.Persona.Skills {
			total++
			if sk.Name != sk.Slug || sk.Description == "" || len(sk.Description) > persona.SkillDescriptionMax {
				t.Fatalf("%s/%s: bad name/description", r.Slug, sk.Slug)
			}
			if strings.ContainsAny(sk.Body, "<>") {
				t.Fatalf("%s/%s: angle brackets in body", r.Slug, sk.Slug)
			}
		}
		if r.RoleID == "" {
			t.Fatalf("%s has no role_id", r.Slug)
		}
	}
	if total < 20 {
		t.Fatalf("expected the shipped skill set, found %d", total)
	}
	// Embedded role_ids are unique.
	seen := map[string]bool{}
	for _, r := range reg.All() {
		if seen[r.RoleID] {
			t.Fatalf("duplicate role_id %s", r.RoleID)
		}
		seen[r.RoleID] = true
	}
}

func subAgents() (fs.FS, error) { return fs.Sub(water.AgentsFS(), "agents") }
