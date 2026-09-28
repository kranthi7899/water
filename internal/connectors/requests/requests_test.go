package requests_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/connectors"
	"water/internal/connectors/requests"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

// TestRespondDeclaration: requests.respond is one level-A, low-risk,
// non-external function needing request_id and answer (note optional), and
// no credential.
func TestRespondDeclaration(t *testing.T) {
	c := requests.New()
	if c.Name() != "requests" {
		t.Fatalf("name = %q", c.Name())
	}
	if s, a := c.Credential(); s != "" || a != "" {
		t.Fatalf("credential = %q/%q, want none", s, a)
	}
	fns := c.Functions()
	if len(fns) != 1 {
		t.Fatalf("functions = %+v, want exactly respond", fns)
	}
	f := fns[0]
	if f.Name != "respond" || f.Level != twins.A || f.Risk != connectors.RiskLow || f.External {
		t.Fatalf("respond = %+v", f)
	}
	if strings.Join(f.Schema.Required, ",") != "request_id,answer" {
		t.Fatalf("required = %v", f.Schema.Required)
	}
	if _, ok := f.Schema.Properties["note"]; !ok {
		t.Fatal("note is not an accepted (optional) property")
	}
	if _, err := connectors.NewRegistry(c); err != nil {
		t.Fatal(err)
	}
}

// TestContent: Content requires a non-blank request_id and answer within
// MaxAnswer characters; note is optional but, when present, must be a
// string within MaxNote characters.
func TestContent(t *testing.T) {
	id, answer, note, err := requests.Content(map[string]any{"request_id": " r1 ", "answer": " ok ", "note": " n "})
	if err != nil || id != "r1" || answer != "ok" || note != "n" {
		t.Fatalf("id=%q answer=%q note=%q err=%v", id, answer, note, err)
	}
	id, answer, note, err = requests.Content(map[string]any{"request_id": "r1", "answer": "ok"})
	if err != nil || note != "" {
		t.Fatalf("no note: %q %q %q %v", id, answer, note, err)
	}
	okAnswer := strings.Repeat("x", requests.MaxAnswer)
	if _, _, _, err := requests.Content(map[string]any{"request_id": "r1", "answer": okAnswer}); err != nil {
		t.Fatalf("at the limit: %v", err)
	}
	bad := []struct {
		name string
		args map[string]any
	}{
		{"nil", nil},
		{"missing request_id", map[string]any{"answer": "ok"}},
		{"missing answer", map[string]any{"request_id": "r1"}},
		{"blank request_id", map[string]any{"request_id": "  ", "answer": "ok"}},
		{"blank answer", map[string]any{"request_id": "r1", "answer": "\t"}},
		{"non-string request_id", map[string]any{"request_id": 1, "answer": "ok"}},
		{"non-string answer", map[string]any{"request_id": "r1", "answer": []any{"x"}}},
		{"answer too long", map[string]any{"request_id": "r1", "answer": okAnswer + "x"}},
		{"non-string note", map[string]any{"request_id": "r1", "answer": "ok", "note": 5}},
		{"note too long", map[string]any{"request_id": "r1", "answer": "ok", "note": strings.Repeat("x", requests.MaxNote+1)}},
	}
	for _, c := range bad {
		if _, _, _, err := requests.Content(c.args); err == nil {
			t.Fatalf("%s: accepted", c.name)
		}
	}
}

const manifest = `
id: test
name: Test twin
usage: {window: 1h, model_calls: 50, auto_model_calls: 10}
connectors:
  - name: requests
    functions:
      - {name: respond, level: A, rate: {max: 30, per: 1h}}
`

func newGate(t *testing.T) (*gate.Gate, *approvals.Queue) {
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
	reg, err := connectors.NewRegistry(requests.New())
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(manifest))
	if err != nil {
		t.Fatal(err)
	}
	q := approvals.NewQueue(st, log)
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: vault.NewMemory(), Store: st})
	if err != nil {
		t.Fatal(err)
	}
	return g, q
}

// TestRespondRequiresAnApprovedEnvelope: level A, so an unapproved call is
// refused, and a valid call only ever runs after Propose+Decide(yes),
// exactly like every other outward-shaped action.
func TestRespondRequiresAnApprovedEnvelope(t *testing.T) {
	g, _ := newGate(t)
	ctx := context.Background()
	_, err := g.Invoke(ctx, gate.Call{Function: "requests.respond", Args: map[string]any{"request_id": "r1", "answer": "ok"}, Origin: gate.P0, Taint: gate.Clean})
	if err == nil || !strings.Contains(err.Error(), "requires an approved envelope") {
		t.Fatalf("unapproved call: %v", err)
	}
}

// TestRespondThroughTheGate: an approved call runs, records nothing outward,
// and returns an acknowledgement echoing what was recorded.
func TestRespondThroughTheGate(t *testing.T) {
	g, q := newGate(t)
	ctx := context.Background()
	payload := map[string]any{"request_id": "req_1", "answer": "Approved: $3k/mo reallocation.", "note": "see budget note"}
	e, err := q.ProposeRequest(ctx, approvals.Envelope{RequestedBy: "lee", Origin: "p1", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Decide(ctx, e.ID, approvals.Yes); err != nil {
		t.Fatal(err)
	}
	res, err := g.Invoke(ctx, gate.Call{Function: e.Action, Args: e.Payload, Origin: gate.Origin(e.Origin), Taint: gate.Tainted, EnvelopeID: e.ID})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out["recorded"] != true || out["request_id"] != "req_1" || out["answer"] != "Approved: $3k/mo reallocation." || out["note"] != "see budget note" {
		t.Fatalf("output = %s", res.Output)
	}
	if res.Untrusted {
		t.Fatal("requests.respond output marked untrusted")
	}
}
