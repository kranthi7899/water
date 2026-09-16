package sitebuild

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"water/internal/tools"
)

func testPolicy(out string) *tools.Policy {
	return &tools.Policy{
		Role:       "build-site",
		Filesystem: tools.FSPolicy{Mode: "read-write", Roots: []string{out}},
		Network:    "none",
		Protected:  []string{filepath.Join(t0(), ".water")},
	}
}

// t0 keeps the protected path off the workspace in tests that do not care
// about it.
func t0() string { return os.TempDir() }

func threeFiles(t *testing.T) []GeneratedFile {
	t.Helper()
	p, err := ParsePlan(goodPlan)
	if err != nil {
		t.Fatal(err)
	}
	files, err := Render(p, "site")
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// TestBuildSiteRequiresApproval — declining writes nothing at all. The files
// only exist once a person has said yes to the whole plan.
func TestBuildSiteRequiresApproval(t *testing.T) {
	out := t.TempDir()
	files := threeFiles(t)
	pol := testPolicy(out)
	table, err := Inspect(pol, files)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}

	for _, answer := range []string{"n\n", "\n", "", "no\n", "maybe\n"} {
		if Confirm(strings.NewReader(answer), io.Discard, table, false) {
			t.Fatalf("answer %q was treated as approval", answer)
		}
	}
	// Nothing was written: inspection alone must not touch the disk.
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(out, f.Path)); !os.IsNotExist(err) {
			t.Fatalf("%s exists after a declined plan", f.Path)
		}
	}

	if !Confirm(strings.NewReader("y\n"), io.Discard, table, false) {
		t.Fatal("y was not treated as approval")
	}
	if err := Apply(context.Background(), tools.NewService(pol, nil), files); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(out, f.Path))
		if err != nil {
			t.Fatalf("%s missing after approval: %v", f.Path, err)
		}
		if string(b) != f.Content {
			t.Fatalf("%s content differs from the approved plan", f.Path)
		}
	}
}

// TestApprovalTextDisclosesEffects — the person sees every file, its size, and
// any overwrite, before deciding.
func TestApprovalTextDisclosesEffects(t *testing.T) {
	out := t.TempDir()
	files := threeFiles(t)
	os.WriteFile(filepath.Join(out, "index.html"), []byte("old page"), 0o644)

	table, err := Inspect(testPolicy(out), files)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if table.Overwrites() != 1 {
		t.Fatalf("expected 1 overwrite, got %d", table.Overwrites())
	}
	table.Intent = "build a static site"
	text := ApprovalText(table)
	for _, want := range []string{
		"REVIEW 3 ACTIONS · BUILD-SITE",
		"build a static site",
		"no network, no shell",
		"write index.html",
		"OVERWRITES 8 bytes",
		"write styles.css",
		"write README.md",
		"Approve this plan once",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("approval text missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, out) {
		t.Fatal("approval text does not name the workspace")
	}
}

// TestConfirmHonoursYes — --yes approves without a prompt, and still prints
// the plan so the run's effects stay visible in the transcript.
func TestConfirmHonoursYes(t *testing.T) {
	out := t.TempDir()
	table, err := Inspect(testPolicy(out), threeFiles(t))
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if !Confirm(strings.NewReader(""), &sb, table, true) {
		t.Fatal("--yes did not approve")
	}
	if !strings.Contains(sb.String(), "write index.html") {
		t.Fatal("--yes suppressed the plan")
	}
}
