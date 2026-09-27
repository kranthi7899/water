package display_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/connectors"
	"water/internal/connectors/display"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

// TestShowDeclaration: display.show is one level-R, low-risk, non-external
// function with a fixed Activity label and a title/body schema that
// requires both, and it needs no credential.
func TestShowDeclaration(t *testing.T) {
	c := display.New()
	if c.Name() != "display" {
		t.Fatalf("name = %q", c.Name())
	}
	if s, a := c.Credential(); s != "" || a != "" {
		t.Fatalf("credential = %q/%q, want none", s, a)
	}
	fns := c.Functions()
	if len(fns) != 1 {
		t.Fatalf("functions = %+v, want exactly show", fns)
	}
	f := fns[0]
	if f.Name != "show" || f.Level != twins.R || f.Risk != connectors.RiskLow || f.External {
		t.Fatalf("show = %+v", f)
	}
	if f.Activity != "Showing you this" || f.Label("display.show") != "Showing you this" {
		t.Fatalf("activity = %q", f.Activity)
	}
	if f.Schema.Properties["title"].Type != "string" || f.Schema.Properties["body"].Type != "string" || len(f.Schema.Properties) != 2 {
		t.Fatalf("schema = %+v", f.Schema)
	}
	if strings.Join(f.Schema.Required, ",") != "title,body" {
		t.Fatalf("required = %v", f.Schema.Required)
	}
	if _, err := connectors.NewRegistry(c); err != nil {
		t.Fatal(err)
	}
}

// TestContent: Content accepts a non-blank title of at most MaxTitle
// characters and a non-blank body of at most MaxBody characters (runes,
// not bytes), trimmed; anything else is refused.
func TestContent(t *testing.T) {
	okTitle := strings.Repeat("é", display.MaxTitle) // 120 runes, 240 bytes
	okBody := strings.Repeat("ü", display.MaxBody)
	title, body, err := display.Content(map[string]any{"title": "  " + okTitle + "\n", "body": okBody})
	if err != nil || title != okTitle || body != okBody {
		t.Fatalf("at the limits: %q %d %v", title, len(body), err)
	}
	bad := []struct {
		name string
		args map[string]any
	}{
		{"nil", nil},
		{"missing title", map[string]any{"body": "b"}},
		{"missing body", map[string]any{"title": "t"}},
		{"blank title", map[string]any{"title": "  \n", "body": "b"}},
		{"blank body", map[string]any{"title": "t", "body": "\t"}},
		{"non-string title", map[string]any{"title": 7, "body": "b"}},
		{"non-string body", map[string]any{"title": "t", "body": []any{"b"}}},
		{"title too long", map[string]any{"title": okTitle + "x", "body": "b"}},
		{"body too long", map[string]any{"title": "t", "body": okBody + "x"}},
	}
	for _, c := range bad {
		if _, _, err := display.Content(c.args); err == nil {
			t.Fatalf("%s: accepted", c.name)
		}
	}
}

const manifest = `
id: test
name: Test twin
usage: {window: 1h, model_calls: 50, auto_model_calls: 10}
connectors:
  - name: display
    functions:
      - {name: show, level: R, rate: {max: 30, per: 1h}}
`

func newGate(t *testing.T) *gate.Gate {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log, err := audit.Open(filepath.Join(dir, "audit", "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	reg, err := connectors.NewRegistry(display.New())
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(manifest))
	if err != nil {
		t.Fatal(err)
	}
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: approvals.NewQueue(st, log), Audit: log, Vault: vault.NewMemory(), Store: st})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// TestShowThroughTheGate: a valid call runs inline at level R (no envelope,
// no credential) and returns a small acknowledgement; missing or oversized
// arguments are refused and nothing is returned.
func TestShowThroughTheGate(t *testing.T) {
	g := newGate(t)
	ctx := context.Background()
	res, err := g.Invoke(ctx, gate.Call{Function: "display.show", Args: map[string]any{"title": "Runway", "body": "14 months at current burn."}, Origin: gate.P0, Taint: gate.Clean})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(res.Output, &out); err != nil || out["shown"] != true {
		t.Fatalf("output = %s (%v)", res.Output, err)
	}
	if res.Untrusted {
		t.Fatal("display output marked untrusted")
	}
	for _, args := range []map[string]any{
		{"body": "b"},
		{"title": "t"},
		{"title": strings.Repeat("x", display.MaxTitle+1), "body": "b"},
		{"title": "t", "body": strings.Repeat("x", display.MaxBody+1)},
	} {
		res, err := g.Invoke(ctx, gate.Call{Function: "display.show", Args: args, Origin: gate.P0, Taint: gate.Clean})
		if err == nil || res.Output != nil {
			t.Fatalf("args %v: out %s err %v, want refused", args, res.Output, err)
		}
	}
	// A tainted session may still show something: it is level R.
	if _, err := g.Invoke(ctx, gate.Call{Function: "display.show", Args: map[string]any{"title": "t", "body": "b"}, Origin: gate.P0, Taint: gate.Tainted}); err != nil {
		t.Fatal(err)
	}
}
