package dashboards

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"

	"water"
)

// TestComputeHasNoHardcodedMetricValues is docs/slices/UI.md Phase 4's own
// acceptance line ("computed from connectors"), enforced as a real
// source-scanning test rather than a promise in a comment — mirroring
// internal/guards' own AST-scan guards (e.g. gate_test.go's
// checkSourceInvariants). It parses compute.go's AST and fails if any
// composite literal of type MetricTile, BreakdownItem or CalloutTile sets
// its Value field to a numeric literal other than 0 — 0 is the zero/
// default value every error branch in this file uses, never a real
// answer. Every real number a tile carries must come from a variable
// computed from fetched connector data (median, cellFloat, numberField,
// ...), never a literal typed directly into this file. The 1.5 median
// multiplier in costSpike is a named threshold on a local variable
// (threshold), not a tile Value, so it is untouched by this check.
func TestComputeHasNoHardcodedMetricValues(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "compute.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	tileTypes := map[string]bool{"MetricTile": true, "BreakdownItem": true, "CalloutTile": true}
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		id, ok := cl.Type.(*ast.Ident)
		if !ok || !tileTypes[id.Name] {
			return true
		}
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != "Value" {
				continue
			}
			lit, ok := kv.Value.(*ast.BasicLit)
			if !ok {
				continue
			}
			if (lit.Kind == token.INT || lit.Kind == token.FLOAT) && lit.Value != "0" {
				t.Errorf("compute.go:%s: %s{...Value: %s...} is a hardcoded metric value, not computed from a connector",
					fset.Position(lit.Pos()), id.Name, lit.Value)
			}
		}
		return true
	})
}

// TestDashboardYAMLFilesContainNoNumericValues re-checks every real
// twins/*/dashboards/*.yaml file's raw text as a second, independent guard
// alongside dashboards_test.go's structured ParseSpec tests (which already
// reject a numeric-literal-shaped metrics/breakdown/callout id at load
// time): no recognized dashboard-spec key's value looks like a bare
// number. Spec has no field that could carry one anyway (KnownFields(true)
// rejects anything else), so this is belt-and-suspenders against a future
// field this scan wouldn't otherwise catch.
func TestDashboardYAMLFilesContainNoNumericValues(t *testing.T) {
	matches, err := fs.Glob(water.TwinsFS(), "twins/*/dashboards/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no twins/*/dashboards/*.yaml files found; the scan is not seeing anything")
	}
	for _, m := range matches {
		b, err := fs.ReadFile(water.TwinsFS(), m)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, val, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			key, val = strings.TrimSpace(key), strings.TrimSpace(val)
			if key != "name" && key != "source" && key != "breakdown" && key != "callout" {
				continue
			}
			if val == "" {
				continue
			}
			if isNumericLiteralShaped(val) {
				t.Errorf("%s: %s: %q looks like a hardcoded number, not an id", m, key, val)
			}
		}
	}
}

// isNumericLiteralShaped reports whether s parses as a plain int or float
// (optionally quoted, matching dashboards.go's own idRe rejection of a
// value like "12" or "12.5").
func isNumericLiteralShaped(s string) bool {
	s = strings.Trim(s, `"'`)
	if s == "" {
		return false
	}
	sawDigit, sawDot := false, false
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
			sawDigit = true
		case r == '.' && !sawDot:
			sawDot = true
		case r == '-' && i == 0:
			// leading sign only
		default:
			return false
		}
	}
	return sawDigit
}
