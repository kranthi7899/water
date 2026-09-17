package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

const safePage = `<!DOCTYPE html><html><head><title>Water</title><link rel="stylesheet" href="styles.css"></head>
<body><h1>Water</h1><p>Each role keeps its own profile: memory stays private. HTTP status and file: names are fine in prose.</p>
<a href="#roles">Roles</a> <a href="about.html">About</a></body></html>`

// stubBrowser records launches instead of opening a real browser.
func stubBrowser(t *testing.T) *[]string {
	t.Helper()
	var opened []string
	prev := launchPage
	launchPage = func(_ context.Context, path string) error {
		opened = append(opened, path)
		return nil
	}
	t.Cleanup(func() { launchPage = prev })
	return &opened
}

func broker(t *testing.T) *ApprovalBroker {
	t.Helper()
	b, err := NewApprovalBroker()
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("the surrounding test sandbox forbids Unix-domain sockets")
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

// TestOpenPageRunsInsideOneApprovedPlan — the demo flow: write a page and its
// stylesheet, then open it, all under a single approval.
func TestOpenPageRunsInsideOneApprovedPlan(t *testing.T) {
	opened := stubBrowser(t)
	b := broker(t)
	root := t.TempDir()
	svc := NewService(InteractiveWorkspacePolicy("ceo", "id", root, t.TempDir(), b.Socket()), nil)
	done := make(chan error, 1)
	go func() {
		_, err := svc.Call(context.Background(), ToolApplyActions, map[string]any{
			"summary": "Build a website for Water and open it",
			"actions": []any{
				map[string]any{"tool": ToolWriteFile, "args": map[string]any{"path": "site/index.html", "content": safePage}},
				map[string]any{"tool": ToolWriteFile, "args": map[string]any{"path": "site/styles.css", "content": "body { color: #111; background: url(bg.png); }"}},
				map[string]any{"tool": ToolOpenPage, "args": map[string]any{"path": "site/index.html"}},
			},
		})
		done <- err
	}()
	pending := waitApproval(t, b)
	if len(pending.Request.Actions) != 3 || pending.Request.Actions[2].Tool != ToolOpenPage {
		t.Fatalf("approval plan = %+v", pending.Request)
	}
	if len(*opened) != 0 {
		t.Fatal("page opened before approval")
	}
	pending.Decide(true)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	realRoot, _ := filepath.EvalSymlinks(root)
	want := filepath.Join(realRoot, "site", "index.html")
	if len(*opened) != 1 || (*opened)[0] != want {
		t.Fatalf("opened = %v, want [%s]", *opened, want)
	}
	for _, ev := range svc.Events() {
		if ev.Basis == "" || ev.Tool == "" {
			t.Fatalf("event missing decision basis: %+v", ev)
		}
	}
}

// TestOpenPageDeniedOutsideAPlan — never a direct call, never headless.
func TestOpenPageDeniedOutsideAPlan(t *testing.T) {
	opened := stubBrowser(t)
	b := broker(t)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "index.html"), []byte(safePage), 0o644)

	interactive := NewService(InteractiveWorkspacePolicy("ceo", "id", root, t.TempDir(), b.Socket()), nil)
	if _, err := interactive.Call(context.Background(), ToolOpenPage, map[string]any{"path": "index.html"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("direct open_page err = %v", err)
	}
	if _, ok := b.Next(); ok {
		t.Fatal("direct open_page reached the approval UI")
	}
	headless := NewService(&Policy{Role: "ceo", Filesystem: FSPolicy{Mode: "read-write", Roots: []string{root}}}, nil)
	if _, err := headless.Call(context.Background(), ToolOpenPage, map[string]any{"path": "index.html"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("headless open_page err = %v", err)
	}
	for _, n := range headless.Policy.ToolNames() {
		if n == ToolOpenPage {
			t.Fatal("open_page is listed as a standalone tool")
		}
	}
	if len(*opened) != 0 {
		t.Fatalf("a page opened outside an approved plan: %v", *opened)
	}
}

// TestOpenPageRefusesBadTargetsBeforePrompt — only .html inside the workspace
// and outside water's own state; a bad target never reaches the person.
func TestOpenPageRefusesBadTargetsBeforePrompt(t *testing.T) {
	opened := stubBrowser(t)
	b := broker(t)
	root := t.TempDir()
	home := filepath.Join(root, ".water")
	os.MkdirAll(home, 0o755)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "page.html"), []byte(safePage), 0o644)
	os.Symlink(filepath.Join(outside, "page.html"), filepath.Join(root, "link.html"))
	svc := NewService(InteractiveWorkspacePolicy("ceo", "id", root, home, b.Socket()), nil)
	for _, p := range []string{
		"notes.txt",
		"../" + filepath.Base(outside) + "/page.html",
		filepath.Join(outside, "page.html"),
		"link.html",
		".water/memory/page.html",
	} {
		_, err := svc.Call(context.Background(), ToolApplyActions, map[string]any{
			"summary": "open a page",
			"actions": []any{map[string]any{"tool": ToolOpenPage, "args": map[string]any{"path": p}}},
		})
		if !errors.Is(err, ErrDenied) {
			t.Fatalf("open_page %s err = %v", p, err)
		}
		if _, ok := b.Next(); ok {
			t.Fatalf("open_page %s reached the approval UI", p)
		}
	}
	if len(*opened) != 0 {
		t.Fatalf("opened = %v", *opened)
	}
}

// TestOpenPageRefusesPagesThatCanReachOut — the browser is outside the
// sandbox, so nothing on the page may load code or contact a host.
func TestOpenPageRefusesPagesThatCanReachOut(t *testing.T) {
	opened := stubBrowser(t)
	for name, files := range map[string]map[string]string{
		"script":            {"index.html": `<html><body><script>fetch("x")</script></body></html>`},
		"remote url":        {"index.html": `<img src="https://evil.example/p.png">`},
		"protocol-relative": {"index.html": `<img src="//evil.example/p.png">`},
		"backslash scheme":  {"index.html": `<img src="http:\\evil.example\p.png">`},
		"event handler":     {"index.html": `<img src="a.png" onerror="location='x'">`},
		"javascript link":   {"index.html": `<a href="javascript:void(0)">x</a>`},
		"iframe":            {"index.html": `<iframe src="about.html"></iframe>`},
		"css import":        {"index.html": `<link rel="stylesheet" href="styles.css">`, "styles.css": `@import "more.css";`},
		"css remote":        {"index.html": `<p>x</p>`, "styles.css": `body { background: url(https://evil.example/b.png) }`},
		"sibling page":      {"index.html": `<a href="about.html">about</a>`, "about.html": `<script>1</script>`},
		"linked subfolder":  {"index.html": `<link rel="stylesheet" href="css/site.css">`, "css/site.css": `h1 { background: url(//evil.example/x) }`},
		"stylesheet escape": {"index.html": `<link rel="stylesheet" href="../../shared.css">`},
	} {
		root := t.TempDir()
		for rel, content := range files {
			p := filepath.Join(root, "site", rel)
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte(content), 0o644)
		}
		svc := NewService(&Policy{Role: "ceo", Filesystem: FSPolicy{Mode: "read-write", Roots: []string{root}}}, nil)
		page, _ := filepath.EvalSymlinks(filepath.Join(root, "site", "index.html"))
		if _, err := svc.openPage(context.Background(), page); !errors.Is(err, ErrDenied) {
			t.Fatalf("%s: open err = %v", name, err)
		}
	}
	if len(*opened) != 0 {
		t.Fatalf("an unsafe page was opened: %v", *opened)
	}

	// A plain page with ordinary prose and local assets opens.
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "site"), 0o755)
	os.WriteFile(filepath.Join(root, "site", "index.html"), []byte(safePage), 0o644)
	os.WriteFile(filepath.Join(root, "site", "styles.css"), []byte("/* theme */ body { color: #111; background: url(bg.png); }"), 0o644)
	svc := NewService(&Policy{Role: "ceo", Filesystem: FSPolicy{Mode: "read-write", Roots: []string{root}}}, nil)
	page, _ := filepath.EvalSymlinks(filepath.Join(root, "site", "index.html"))
	if _, err := svc.openPage(context.Background(), page); err != nil {
		t.Fatalf("self-contained page refused: %v", err)
	}
	if len(*opened) != 1 {
		t.Fatalf("opened = %v", *opened)
	}
}
