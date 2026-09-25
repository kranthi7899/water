package nervous

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// assertNoQueueDecideCalls is the structural proof that no source file under
// internal/nervous (this package and every subpackage) calls
// approvals.Queue.Decide directly: every decision must go through the
// narrower nervous.Approver interface instead (internal/gateway is the only
// place that ever calls Queue.Decide, via decideAndExecute). Test files are
// excluded: a test's own fake Approver is allowed to call the real Decide
// to get realistic Pending/Bound behavior across turns (see
// voiceapprove_bind_test.go's fakeApprover), since it stands in for
// internal/gateway, not for anything internal/nervous itself does at
// runtime.
func TestNoDecideCallUnderNervousTree(t *testing.T) {
	assertNoQueueDecideCalls(t)
}

func assertNoQueueDecideCalls(t *testing.T) {
	t.Helper()
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		node, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
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
			if sel.Sel.Name == "Decide" {
				t.Errorf("%s: forbidden call to a method named %q (internal/nervous must never decide directly; go through Approver.DecideBound instead)", path, sel.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
