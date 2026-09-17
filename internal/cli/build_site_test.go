package cli

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"water/internal/tools"
)

func sitePlan(root string) tools.ApprovalRequest {
	return tools.ApprovalRequest{Role: "design", Tool: tools.ToolApplyActions, Summary: "Build the Water site", Actions: []tools.PlannedAction{
		{Tool: tools.ToolWriteFile, Args: map[string]any{"path": filepath.Join(root, "site", "index.html"), "content": "<h1>Water</h1>"}},
		{Tool: tools.ToolWriteFile, Args: map[string]any{"path": filepath.Join(root, "site", "styles.css"), "content": "h1{}"}},
		{Tool: tools.ToolOpenPage, Args: map[string]any{"path": filepath.Join(root, "site", "index.html")}},
	}}
}

// TestReviewPlanApprovesOnlyAnExplicitYes — silence, EOF and anything
// unrecognised decline; d shows the files and asks again.
func TestReviewPlanApprovesOnlyAnExplicitYes(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	req := sitePlan(root)
	for _, answer := range []string{"", "\n", "n\n", "no\n", "sure\n", "d\n"} {
		var out strings.Builder
		if reviewPlan(bufio.NewReader(strings.NewReader(answer)), &out, req, root, false) {
			t.Fatalf("answer %q approved the plan", answer)
		}
	}
	for _, answer := range []string{"y\n", "YES\n"} {
		var out strings.Builder
		if !reviewPlan(bufio.NewReader(strings.NewReader(answer)), &out, req, root, false) {
			t.Fatalf("answer %q did not approve", answer)
		}
	}
	var out strings.Builder
	if !reviewPlan(bufio.NewReader(strings.NewReader("d\ny\n")), &out, req, root, false) {
		t.Fatal("d then y did not approve")
	}
	if !strings.Contains(out.String(), "<h1>Water</h1>") {
		t.Fatal("d did not show the file contents")
	}
	out.Reset()
	if !reviewPlan(bufio.NewReader(strings.NewReader("")), &out, req, root, true) || !strings.Contains(out.String(), "write") {
		t.Fatal("--yes must approve and still print the plan")
	}
}

// TestPlanTextShowsEveryEffect — each write with its size, an overwrite
// warning, and the browser open with its sandbox note.
func TestPlanTextShowsEveryEffect(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	os.MkdirAll(filepath.Join(root, "site"), 0o755)
	os.WriteFile(filepath.Join(root, "site", "styles.css"), []byte("old"), 0o644)
	text := planText(sitePlan(root), root)
	for _, want := range []string{
		"REVIEW 3 ACTIONS · DESIGN",
		"Build the Water site",
		"no shell · no network",
		"write  " + filepath.Join("site", "index.html"),
		"14 B",
		"replaces existing file",
		"open   " + filepath.Join("site", "index.html"),
		"outside the sandbox",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plan text missing %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "replaces existing file") != 1 {
		t.Fatalf("only styles.css exists:\n%s", text)
	}
}

func TestSiteFolderStaysInsideTheWorkspace(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "../site", "/tmp/site", "site/../../x"} {
		if _, err := siteFolder(bad); err == nil {
			t.Fatalf("--out %q accepted", bad)
		}
	}
	if got, err := siteFolder("demo/site/"); err != nil || got != "demo/site" {
		t.Fatalf("siteFolder(demo/site/) = %q, %v", got, err)
	}
}

func TestSitePagePrefersTheOpenedPage(t *testing.T) {
	evs := []tools.Event{
		{Tool: tools.ToolWriteFile, Allowed: true, Args: map[string]any{"path": "/w/site/index.html"}},
		{Tool: tools.ToolOpenPage, Allowed: true, Args: map[string]any{"path": "/w/site/about.html"}},
	}
	if p, opened := sitePage(evs); p != "/w/site/about.html" || !opened {
		t.Fatalf("sitePage = %q %v", p, opened)
	}
	evs[1].Error = "denied by tool policy: not self-contained"
	if p, opened := sitePage(evs); p != "/w/site/index.html" || opened {
		t.Fatalf("after a refused open, sitePage = %q %v", p, opened)
	}
	if p, _ := sitePage([]tools.Event{{Tool: tools.ToolWriteFile, Allowed: false, Args: map[string]any{"path": "/w/site/index.html"}}}); p != "" {
		t.Fatalf("a denied write produced a link: %q", p)
	}
}

func TestFirstParagraphKeepsTheStageShort(t *testing.T) {
	got := firstParagraph("I built the page and it's open (evidence: call-design-1a2b3c).\n\n**Accessibility**\n- lots of detail")
	if got != "I built the page and it's open." {
		t.Fatalf("firstParagraph = %q", got)
	}
	if u := fileURL("/tmp/my site/index.html"); u != "file:///tmp/my%20site/index.html" {
		t.Fatalf("fileURL = %q", u)
	}
}
