package memory

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestStillUnwired asserts no package outside internal/memory imports it.
// Wiring memory into the daemon, prompt, tools, config or CLI is Slice G's
// job, behind G's gate/grant review; doing it early would skip that.
func TestStillUnwired(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found at %s: %v", root, err)
	}
	self := filepath.Join(root, "internal", "memory")
	fset := token.NewFileSet()
	checked := 0
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if p == self || name == ".git" || name == "node_modules" || name == "testdata" || (strings.HasPrefix(name, ".") && p != root) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
		if err != nil {
			return nil // another stream's file mid-edit; imports are what matter
		}
		checked++
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if path == "water/internal/memory" || strings.HasPrefix(path, "water/internal/memory/") {
				rel, _ := filepath.Rel(root, p)
				t.Errorf("%s imports %s; memory is wired in Slice G, not F", rel, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 50 {
		t.Fatalf("only %d Go files scanned; the walk is broken", checked)
	}
}
