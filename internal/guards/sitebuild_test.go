package guards_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"water/internal/agent"
	"water/internal/backend"
	"water/internal/orchestrator"
	"water/internal/sitebuild"
	"water/internal/tools"
)

// buildSitePolicy is the policy `water build-site` runs under: one workspace,
// no shell, no approval socket, water's own state protected.
func buildSitePolicy(out, home string) *tools.Policy {
	return &tools.Policy{
		Role:       "build-site",
		Filesystem: tools.FSPolicy{Mode: "read-write", Roots: []string{out}},
		Network:    "none",
		Protected:  []string{home},
	}
}

// TestBuildSiteWritesOnlyUnderOutDir — a generated file may never name a
// destination outside --out, including through .., an absolute path, or a
// symlink planted in the output directory.
func TestBuildSiteWritesOnlyUnderOutDir(t *testing.T) {
	out := t.TempDir()
	outside := t.TempDir()
	home := filepath.Join(t.TempDir(), ".water")
	os.MkdirAll(home, 0o755)
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("nope"), 0o644)
	os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(out, "link.html"))
	os.Symlink(outside, filepath.Join(out, "dirlink"))

	pol := buildSitePolicy(out, home)
	// A plain rendered file is accepted.
	if _, err := sitebuild.Inspect(pol, []sitebuild.GeneratedFile{{Path: "index.html", Content: "<h1>ok</h1>"}}); err != nil {
		t.Fatalf("in-root write refused: %v", err)
	}
	for _, p := range []string{
		"../escape.html",
		filepath.Join(out, "..", filepath.Base(outside), "secret.txt"),
		filepath.Join(outside, "secret.txt"),
		"link.html",
		"dirlink/secret.txt",
		"/etc/passwd",
	} {
		if _, err := sitebuild.Inspect(pol, []sitebuild.GeneratedFile{{Path: p, Content: "x"}}); err == nil {
			t.Fatalf("write of %s should be denied", p)
		}
		if _, err := os.Stat(filepath.Join(outside, "escape.html")); !os.IsNotExist(err) {
			t.Fatalf("a refused plan touched disk for %s", p)
		}
	}
	// One bad file condemns the whole plan: nothing is written piecemeal.
	files := []sitebuild.GeneratedFile{
		{Path: "index.html", Content: "<h1>ok</h1>"},
		{Path: "../escape.html", Content: "x"},
	}
	if _, err := sitebuild.Inspect(pol, files); err == nil {
		t.Fatal("a plan containing an escaping path was accepted")
	}
}

// TestBuildSiteRefusesProtectedWaterState — the output directory may sit
// anywhere, but water's own state (memory, traces, keyring) stays unreachable
// even when it lies under the declared root.
func TestBuildSiteRefusesProtectedWaterState(t *testing.T) {
	out := t.TempDir()
	home := filepath.Join(out, ".water")
	os.MkdirAll(filepath.Join(home, "memory", "design"), 0o755)
	os.MkdirAll(filepath.Join(home, "traces"), 0o755)
	os.WriteFile(filepath.Join(home, "memory", "design", "session.md"), []byte("DESIGN-PRIVATE"), 0o644)
	os.WriteFile(filepath.Join(home, "keyring"), []byte("secret"), 0o600)
	os.Symlink(filepath.Join(home, "memory"), filepath.Join(out, "mem-link"))

	pol := buildSitePolicy(out, home)
	for _, p := range []string{
		".water/memory/design/session.md",
		".water/traces/run.jsonl",
		".water/keyring",
		"mem-link/design/session.md",
		filepath.Join(home, "memory", "design", "session.md"),
	} {
		if _, err := sitebuild.Inspect(pol, []sitebuild.GeneratedFile{{Path: p, Content: "x"}}); err == nil {
			t.Fatalf("write into protected state %s succeeded", p)
		}
	}
	if b, err := os.ReadFile(filepath.Join(home, "memory", "design", "session.md")); err != nil || string(b) != "DESIGN-PRIVATE" {
		t.Fatalf("protected file changed: %q %v", b, err)
	}
}

// TestBuildSiteNoShell — build-site grants a workspace and nothing else: there
// is no shell surface for model output to reach, in any mode.
func TestBuildSiteNoShell(t *testing.T) {
	out := t.TempDir()
	home := filepath.Join(t.TempDir(), ".water")
	os.MkdirAll(home, 0o755)
	pol := buildSitePolicy(out, home)

	for _, n := range pol.ToolNames() {
		if n == tools.ToolRun {
			t.Fatalf("build-site exposes %s", tools.ToolRun)
		}
	}
	svc := tools.NewService(pol, nil)
	ctx := context.Background()
	if _, err := svc.Call(ctx, tools.ToolRun, map[string]any{"command": "ls"}); err == nil {
		t.Fatal("shell ran under the build-site policy")
	}
	// apply_actions is an interactive-workspace tool; build-site has no
	// approval socket and must not reach it either.
	if _, err := svc.Call(ctx, tools.ToolApplyActions, map[string]any{"summary": "x", "actions": []any{}}); err == nil {
		t.Fatal("apply_actions ran without an approval socket")
	}
	for _, ev := range svc.Events() {
		if ev.Basis == "" || ev.Tool == "" {
			t.Fatalf("event missing decision basis: %+v", ev)
		}
	}
}

// TestBuildSiteRendersOnlyItsOwnPaths — the model names no destinations. Any
// path-like text it supplies stays content, and the written set is exactly the
// three files the renderer owns.
func TestBuildSiteRendersOnlyItsOwnPaths(t *testing.T) {
	out := t.TempDir()
	home := filepath.Join(t.TempDir(), ".water")
	os.MkdirAll(home, 0o755)

	plan, err := sitebuild.ParsePlan(`{"title":"../../etc/passwd","description":"/etc/shadow",
		"sections":[{"id":"hero","heading":"../escape","body":"/etc/passwd","bullets":["../../x"]}],
		"theme":{"name":"t","background":"#050505","foreground":"#E8E4D0","accent":"#D00000"}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	files, err := sitebuild.Render(plan, "site")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	pol := buildSitePolicy(out, home)
	table, err := sitebuild.Inspect(pol, files)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	// Resolved paths are canonical, so compare against the canonical root.
	realOut, err := filepath.EvalSymlinks(out)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range table.Rows {
		got = append(got, r.Path)
		if !strings.HasPrefix(r.Resolved, realOut) {
			t.Fatalf("%s resolved outside the workspace: %s", r.Path, r.Resolved)
		}
	}
	if strings.Join(got, ",") != "index.html,styles.css,README.md" {
		t.Fatalf("unexpected file set: %v", got)
	}
}

// TestBuildSiteOrchestrationPreservesCEOFinalWriter — the page is built from
// the CEO's final synthesis, which only an orchestrator may write. A
// specialist's own words never reach the site except through that decision.
func TestBuildSiteOrchestrationPreservesCEOFinalWriter(t *testing.T) {
	const ceoFinal = "Final answer: ship the landing page in two phases, narrow scope first."
	const ctoOnly = "Throughput ceiling measured at 400 rps."
	const designOnly = "Checkout fails 1.4.3 contrast."

	fake := backend.NewFake("fake-sub")
	fake.Reply = func(req backend.Request) string {
		switch req.Role {
		case "ceo":
			// The extraction turn is recognisable by its tagged input.
			if strings.Contains(req.Prompt, "<final_synthesis>") {
				if !strings.Contains(req.Prompt, ceoFinal) {
					t.Errorf("extraction turn did not receive the CEO final: %q", req.Prompt)
				}
				return `{"title":"Water","description":"d","sections":[
					{"id":"hero","heading":"Water","body":"ship in two phases, narrow scope first"}],
					"theme":{"name":"t","background":"#050505","foreground":"#E8E4D0","accent":"#D00000"}}`
			}
			if strings.Contains(req.Prompt, "[status]") {
				return agent.RouteFinal + "\n" + ceoFinal
			}
			return agent.RouteDelegate + "\nAssess the landing page brief."
		case "coo":
			if strings.Contains(req.Prompt, "[deliverable]") {
				return "VERIFIED: both specialists reported."
			}
			return "## cto\nJudge feasibility.\n## design\nJudge the experience."
		case "cto":
			return ctoOnly
		case "design":
			return designOnly
		}
		return "?"
	}

	env := agent.Env{Backend: fake}
	g, st, reg, _ := buildHierarchy(t, fake, env)
	ctx := context.Background()
	if err := (&orchestrator.Executor{MaxParallel: 4, MaxSteps: 24}).Run(ctx, g, st); err != nil {
		t.Fatal(err)
	}
	final, ok := st.FinalOutput()
	if !ok || !strings.Contains(final, ceoFinal) {
		t.Fatalf("final output is not the CEO's synthesis: %q", final)
	}
	// A specialist cannot author the final output, so it cannot author the page.
	if err := st.SetFinalOutput("cto", "a specialist's own words"); !errors.Is(err, orchestrator.ErrNotOrchestrator) {
		t.Fatalf("a specialist wrote FinalOutput: %v", err)
	}

	resp, _, err := agent.RunSingle(ctx, reg.Orchestrator(), env, sitebuild.BuildPrompt("build a landing page", final))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := sitebuild.ParsePlan(resp.Text)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	files, err := sitebuild.Render(plan, "site")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var html string
	for _, f := range files {
		if f.Path == "index.html" {
			html = f.Content
		}
	}
	if !strings.Contains(html, "ship in two phases") {
		t.Fatal("the page does not carry the CEO's decision")
	}
	for _, leaked := range []string{ctoOnly, designOnly, "400 rps", "1.4.3"} {
		if strings.Contains(html, leaked) {
			t.Fatalf("a specialist's own words reached the page: %q", leaked)
		}
	}
}
