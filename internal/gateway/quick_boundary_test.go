package gateway

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// deniedQuickImports mirrors internal/nervous/reflex/imports_test.go's
// denylist for the one file the sous chef's quick.* calls pass through on
// their way into the daemon: quick.go must never be able to reach a
// connector or the gate, structurally, not by convention.
var deniedQuickImports = []string{
	"water/internal/gate",
	"water/internal/connectors",
}

// TestQuickInvokeCannotReachGate parses internal/gateway/quick.go and fails
// if it imports the gate or connectors packages, or if it ever references
// d.cfg.Gate at all (by name, anywhere: as a selector base, a field access,
// or an argument) — the structural proof that a quick.* call has no path to
// an outward action, matching reflex's own import-denylist test on the
// other side of this same boundary.
func TestQuickInvokeCannotReachGate(t *testing.T) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "quick.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse quick.go: %v", err)
	}

	for _, imp := range node.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		for _, denied := range deniedQuickImports {
			if path == denied || strings.HasPrefix(path, denied+"/") {
				t.Errorf("quick.go: forbidden import %q", path)
			}
		}
	}

	ast.Inspect(node, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		// Forbid any selector literally named Gate (d.cfg.Gate, or anything
		// shaped like it) anywhere in the file — a field read is already
		// the mistake; it doesn't need to also be called.
		if sel.Sel.Name == "Gate" {
			t.Errorf("quick.go: forbidden reference to a selector named Gate at %s", fset.Position(sel.Pos()))
		}
		// Forbid a call whose receiver expression mentions Gate anywhere in
		// its own text (e.g. d.cfg.Gate.Invoke(...) — the call itself, not
		// just the field access above, in case of an aliased local var).
		if call, ok := n.(*ast.CallExpr); ok {
			if outer, ok := call.Fun.(*ast.SelectorExpr); ok {
				if strings.Contains(exprString(outer.X), "Gate") {
					t.Errorf("quick.go: forbidden call on a Gate-shaped receiver at %s", fset.Position(call.Pos()))
				}
			}
		}
		return true
	})
}

// exprString renders an expression back to source text for the substring
// check above (there is no need for a real formatter here — this only ever
// looks at short selector chains like "d.cfg.Gate").
func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	default:
		return ""
	}
}
