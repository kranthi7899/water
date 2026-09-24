package cli

import (
	"fmt"
	"io/fs"

	"water"
	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/connectors"
	"water/internal/connectors/google/gcal"
	"water/internal/connectors/google/gdrive"
	"water/internal/connectors/google/gmail"
	"water/internal/decisions"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

// twinDeps bundles what the CEO twin's daemon needs to run.
type twinDeps struct {
	manifest  *twins.Manifest
	store     *store.Store
	audit     *audit.Log
	approvals *approvals.Queue
	gate      *gate.Gate
	registry  *connectors.Registry
	vault     vault.Vault
	decisions *decisions.Registry
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
// gcal, gmail and gdrive all read one shared water.google/ceo Keychain
// credential (see internal/connectors/google/gapi); `water connect google`
// writes it.
func buildCEORegistry() (*connectors.Registry, error) {
	return connectors.NewRegistry(gcal.New(), gmail.New(), gdrive.New())
}

// loadTwinManifest loads and validates the CEO twin's manifest, decision
// registry and connectors without opening the store or the audit log. It is
// what read-only commands (`water status`, `water doctor`) use: the audit
// log has one exclusive writer — the running daemon — and taking its lock
// from a read-only command would both fail while the daemon runs and, in
// the moment it held the lock, make a (re)starting daemon fail.
func loadTwinManifest(fsys fs.FS) (*twins.Manifest, error) {
	m, err := twins.Load(fsys, "ceo")
	if err != nil {
		return nil, fmt.Errorf("twin manifest: %w", err)
	}
	if _, err := decisions.LoadRegistry(fsys, m); err != nil {
		return nil, fmt.Errorf("decision registry: %w", err)
	}
	reg, err := buildCEORegistry()
	if err != nil {
		return nil, err
	}
	if err := gate.ValidateManifest(m, reg); err != nil {
		return nil, err
	}
	return m, nil
}

// buildTwinDeps opens the store and the anchored, hash-chained audit log at
// their default ~/.water locations and wires the gate over them. Callers must
// Close() the result.
func buildTwinDeps() (*twinDeps, error) {
	return buildTwinDepsFS(water.TwinsFS())
}

// buildTwinDepsFS is buildTwinDeps parameterized over the twins filesystem,
// so a test can hand it a fixture tree (e.g. a deliberately malformed
// decisions/*.yaml) without touching the embedded twins/ceo/decisions
// directory. The manifest and the decision registry are both validated
// before anything else opens, so a bad file of either kind fails loudly here
// and never gets as far as touching the real store or audit log.
func buildTwinDepsFS(fsys fs.FS) (*twinDeps, error) {
	m, err := twins.Load(fsys, "ceo")
	if err != nil {
		return nil, fmt.Errorf("twin manifest: %w", err)
	}
	// The decision registry loads and validates against the same manifest,
	// the same fail-loudly posture twins.Load already has: a bad
	// decisions/*.yaml must stop the daemon from starting, not silently drop
	// a type or fall back to nothing.
	decisionsReg, err := decisions.LoadRegistry(fsys, m)
	if err != nil {
		return nil, fmt.Errorf("decision registry: %w", err)
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
	return &twinDeps{manifest: m, store: st, audit: log, approvals: q, gate: g, registry: reg, vault: v, decisions: decisionsReg, roleMD: loadCEORoleMD()}, nil
}

// buildDecisionsTrigger wires internal/decisions' classification-trigger
// orchestration to this process's own gate, store and selected backend: a
// fast-tier ModelClassifier (charged against the manifest's usage cap like
// any other model call) durably cached in the store so an item already seen
// is never reclassified, feeding a Builder that resolves each matched type's
// needs through the same gate every other read goes through. be is the
// backend selected for this run (only known after buildTwinDeps, since
// backend selection happens later in the daemon's startup); the result is
// always non-nil given a valid deps (NewTriager only errors on a nil
// classifier or candidate, and neither is ever nil here).
func buildDecisionsTrigger(deps *twinDeps, be backend.Backend) *decisions.Trigger {
	model := deps.manifest.ModelFor(twins.TierFast)
	charge := func() error { return deps.gate.ModelCall(gate.P1) }
	classifier := &decisions.ModelClassifier{Registry: deps.decisions, Backend: be, Model: model, Charge: charge}
	cached := &decisions.StoreCache{Store: deps.store, Inner: classifier}
	triager, err := decisions.NewTriager(cached, decisions.Candidate)
	if err != nil {
		return nil
	}
	builder := &decisions.Builder{
		Registry: deps.decisions, Gate: deps.gate, Origin: gate.P1,
		Phraser: &decisions.ModelPhraser{Backend: be, Model: model, Charge: charge},
	}
	return &decisions.Trigger{Store: deps.store, Triager: triager, Builder: builder}
}
