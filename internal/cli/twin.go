package cli

import (
	"fmt"

	"water"
	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/connectors"
	"water/internal/connectors/fake"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

// twinDeps bundles what the CEO twin's daemon needs to run. A2 wires the
// fake_* connectors (the only ones that exist so far); A3 replaces them with
// real ones without changing this shape or anything that depends on it.
type twinDeps struct {
	manifest  *twins.Manifest
	store     *store.Store
	audit     *audit.Log
	approvals *approvals.Queue
	gate      *gate.Gate
	registry  *connectors.Registry
	vault     vault.Vault
	roleMD    string
}

func (d *twinDeps) Close() {
	d.audit.Close()
	d.store.Close()
}

// loadCEORoleMD returns twins/ceo/role.md, or "" if it has not been written
// yet (tolerated until Phase 4 of this slice writes it).
func loadCEORoleMD() string {
	b, err := water.TwinsFS().ReadFile("twins/ceo/role.md")
	if err != nil {
		return ""
	}
	return string(b)
}

// buildCEORegistry constructs the connector registry the CEO twin's manifest
// is checked against. Every function twin.yaml lists must be provided here.
func buildCEORegistry() (*connectors.Registry, error) {
	return connectors.NewRegistry(fake.NewCalendar(), fake.NewMail(), fake.NewDocs())
}

// buildTwinDeps opens the store and the anchored, hash-chained audit log at
// their default ~/.water locations and wires the gate over them. Callers must
// Close() the result.
func buildTwinDeps() (*twinDeps, error) {
	m, err := twins.Load(water.TwinsFS(), "ceo")
	if err != nil {
		return nil, fmt.Errorf("twin manifest: %w", err)
	}
	st, err := store.Open(store.DefaultPath())
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	// *store.Store implements audit.Anchor directly (LoadAuditAnchor /
	// SaveAuditAnchor), so the audit log's tail is anchored in the same
	// database the gate persists rate/usage windows to.
	log, err := audit.Open(audit.DefaultPath(), audit.WithAnchor(st))
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("audit: %w", err)
	}
	q := approvals.NewQueue(st, log)
	reg, err := buildCEORegistry()
	if err != nil {
		log.Close()
		st.Close()
		return nil, err
	}
	v := vault.Default()
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: v, Store: st})
	if err != nil {
		log.Close()
		st.Close()
		return nil, err
	}
	return &twinDeps{manifest: m, store: st, audit: log, approvals: q, gate: g, registry: reg, vault: v, roleMD: loadCEORoleMD()}, nil
}
