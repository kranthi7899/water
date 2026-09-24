package fake_test

import (
	"path/filepath"
	"testing"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/connectors"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

// newGateHarness builds a real gate (real store, real hash-chained audit
// log, both in a fresh temp dir) over manifestYAML and cs, the same way
// internal/gate/gate_test.go's harness does for the calendar/mail/docs
// fakes. It is what "a full Invoke-through-the-gate test" means for the
// github/linear/hubspot fakes: no shortcuts, no permit built by hand.
func newGateHarness(t *testing.T, manifestYAML string, cs ...connectors.Connector) *gate.Gate {
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
	q := approvals.NewQueue(st, log)
	reg, err := connectors.NewRegistry(cs...)
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(manifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: vault.NewMemory(), Store: st})
	if err != nil {
		t.Fatal(err)
	}
	return g
}
