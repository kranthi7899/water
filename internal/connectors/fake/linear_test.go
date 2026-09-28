package fake_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"water/internal/connectors/fake"
	"water/internal/gate"
	"water/internal/store"
)

const linearManifest = `
id: t
name: T
usage: {window: 1h, model_calls: 10}
connectors:
  - name: linear
    functions:
      - {name: list_issues, level: R}
`

func TestLinearFunctionsSchema(t *testing.T) {
	l := fake.NewLinear(fake.DefaultLinearIssues()...)
	fns := l.Functions()
	if len(fns) != 1 || fns[0].Name != "list_issues" {
		t.Fatalf("functions: %+v", fns)
	}
	f := fns[0]
	if f.Level != "R" || !f.External {
		t.Fatalf("list_issues: level=%s external=%v, want R/true", f.Level, f.External)
	}
	b, _ := json.Marshal(f.Schema)
	if !strings.Contains(string(b), `"additionalProperties":false`) {
		t.Fatalf("schema: %s", b)
	}
	if err := f.Schema.Validate(map[string]any{"query": "checkout"}); err != nil {
		t.Fatalf("valid args rejected: %v", err)
	}
	if err := f.Schema.Validate(map[string]any{"nope": 1}); err == nil {
		t.Fatal("unknown argument accepted")
	}
}

func TestLinearNormalize(t *testing.T) {
	l := fake.NewLinear(fake.DefaultLinearIssues()...)
	raw, err := json.Marshal(fake.DefaultLinearIssues())
	if err != nil {
		t.Fatal(err)
	}
	recs, err := l.Normalize("list_issues", raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != len(fake.DefaultLinearIssues()) {
		t.Fatalf("records: %d, want %d", len(recs), len(fake.DefaultLinearIssues()))
	}
	for _, r := range recs {
		is, ok := r.(*store.Issue)
		if !ok {
			t.Fatalf("did not normalize to *store.Issue: %T", r)
		}
		if !is.External || is.Project == "" || is.Priority == "" {
			t.Fatalf("issue: %+v", is)
		}
	}
}

func TestLinearInvokeThroughTheGateFiltersByQuery(t *testing.T) {
	l := fake.NewLinear(fake.DefaultLinearIssues()...)
	g := newGateHarness(t, linearManifest, l)

	res, err := g.Invoke(context.Background(), gate.Call{Function: "linear.list_issues", Origin: gate.P1, Taint: gate.Clean})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Untrusted || len(res.Records) != len(fake.DefaultLinearIssues()) {
		t.Fatalf("no filter: untrusted=%v records=%d", res.Untrusted, len(res.Records))
	}

	res, err = g.Invoke(context.Background(), gate.Call{Function: "linear.list_issues", Args: map[string]any{"query": "checkout"}, Origin: gate.P1, Taint: gate.Clean})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) == 0 {
		t.Fatal("expected the Checkout Revamp ticket to match")
	}
	for _, r := range res.Records {
		is := r.(*store.Issue)
		if is.Project != "Checkout Revamp" {
			t.Fatalf("query filter leaked a non-matching project: %+v", is)
		}
	}
}
