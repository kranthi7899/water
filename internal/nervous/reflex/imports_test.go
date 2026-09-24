package reflex

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// deniedImports is Design §1(a): no reflex-package source file may import
// anything that could reach a subprocess, the network, or a connector/gate
// path. A write attempt through the store is separately blocked at the
// SQLite level (store.OpenReadOnly); this test blocks it at the Go level
// too, so the two enforcement layers don't have to be trusted alone.
var deniedImports = []string{
	"os/exec",
	"syscall",
	"net",
	"net/http",
	"water/internal/backend",
	"water/internal/gate",
	"water/internal/connectors",
	"water/internal/vault",
}

// deniedSelectors is Design §1(b): no reflex-package source file may call a
// mutating method on the store or approvals queue. Reading (Pending, the
// StoreView methods) is fine; only names that change state are listed.
var deniedSelectors = []string{
	"Decide", "Edit", "Claim", "Abandon", "Propose",
	"Upsert", "SetCursor", "SetBrief", "InsertApproval", "TransitionApproval",
}

func isDenied(imp string) bool {
	for _, d := range deniedImports {
		if imp == d || strings.HasPrefix(imp, d+"/") {
			return true
		}
	}
	return false
}

func TestNoForbiddenImportsOrMutatingCalls(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		node, err := parser.ParseFile(fset, f, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}

		for _, imp := range node.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if isDenied(path) {
				t.Errorf("%s: forbidden import %q (reflex must never reach a subprocess, the network, a connector, the gate, or the vault)", f, path)
			}
		}

		ast.Inspect(node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			for _, name := range deniedSelectors {
				if sel.Sel.Name == name {
					t.Errorf("%s: forbidden call to a method named %q (reflex handlers must never mutate the store or approvals queue)", f, name)
				}
			}
			return true
		})
	}
}
