package intents

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// parsePlannedActionsFromDecisions parses internal/decisions/registry.go
// with go/parser and extracts the keys of its plannedActions map literal,
// so TestPlannedActionsMatchDecisions can prove the two lists agree
// without this package ever importing internal/decisions (which Slice R
// must not depend on or edit).
func parsePlannedActionsFromDecisions(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	const path = "../../decisions/registry.go"
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	keys := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range vs.Names {
			if name.Name != "plannedActions" {
				continue
			}
			if i >= len(vs.Values) {
				continue
			}
			cl, ok := vs.Values[i].(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, elt := range cl.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				lit, ok := kv.Key.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				keys[s] = true
			}
		}
		return true
	})
	return keys
}
