package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestRunDaemonShutsDownResearchPool pins that the daemon kills research's
// warm spare on its way out (docs/slices/W.md §15). Without the deferred
// research.Shutdown() a graceful stop leaves the spare to exit only on stdin
// EOF, and its scratch directory (removed only by the pool's kill) is left
// behind in $TMPDIR on every daemon stop.
func TestRunDaemonShutsDownResearchPool(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "cmd_daemon.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body *ast.BlockStmt
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "runDaemon" {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatal("runDaemon not found in cmd_daemon.go")
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		def, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}
		if sel, ok := def.Call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Shutdown" {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "research" {
				found = true
			}
		}
		return true
	})
	if !found {
		t.Fatal("runDaemon never defers research.Shutdown(): the warm research spare outlives a graceful daemon stop and its scratch dir leaks")
	}
}
