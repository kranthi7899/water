package cli

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"water"
	"water/internal/agentmail"
	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/config"
	"water/internal/connectors"
	"water/internal/connectors/display"
	"water/internal/connectors/fake"
	"water/internal/connectors/github"
	"water/internal/connectors/google/gcal"
	"water/internal/connectors/google/gdrive"
	"water/internal/connectors/google/gmail"
	"water/internal/connectors/google/gsheets"
	"water/internal/connectors/hubspot"
	"water/internal/connectors/linear"
	"water/internal/connectors/research"
	"water/internal/decider"
	"water/internal/decisions"
	"water/internal/gate"
	"water/internal/nervous/intents"
	"water/internal/nervous/propose"
	"water/internal/nervous/reflex"
	"water/internal/nervous/render"
	"water/internal/roster"
	"water/internal/store"
	"water/internal/twinlink"
	"water/internal/twins"
	"water/internal/vault"
)

// realTwinID and demoTwinID are the only two twins/<id> directories the CLI
// ever loads. demoTwinID's manifest and decisions/ are additive (Slice
// "demo"): fake, in-memory GitHub/Linear/HubSpot connectors and a
// HubSpot-sourced investor_request card, for showcasing the twin without
// real credentials for services the owner doesn't use. Nothing about
// realTwinID changes: buildTwinDeps(realTwinID) loads exactly what it did
// before --demo existed.
const (
	realTwinID = "ceo"
	demoTwinID = "ceo-demo"
)

// demoEnvVar is the env-var fallback for --demo, for a shell or launchd
// context where passing a flag is awkward. Either one selects demoTwinID;
// neither is ever the default.
const demoEnvVar = "WATER_DEMO"

// twinID resolves which twin id a command should load: demoTwinID if --demo
// was passed or WATER_DEMO is set to anything non-empty, realTwinID
// otherwise. This is the only place that decides it, so every command reads
// the same answer.
//
// --twin <id> (or WATER_TWIN) names any other twins/<id> directory instead —
// Slice E's second twin (twins/counterparty) runs this way as its own daemon,
// with its own WATER_HOME. It takes precedence over --demo; an id naming no
// twins/<id>/twin.yaml fails at load exactly like a bad manifest does.
func (a *App) twinID() string {
	if id := a.flags.twin; id != "" {
		return id
	}
	if id := os.Getenv(twinEnvVar); id != "" {
		return id
	}
	if a.flags.demo || os.Getenv(demoEnvVar) != "" {
		return demoTwinID
	}
	return realTwinID
}

// twinEnvVar is the env-var fallback for --twin.
const twinEnvVar = "WATER_TWIN"

// twinDataDir is where id's store and audit log live. The real twin keeps
// the original ~/.water layout byte for byte; any other twin (the demo one,
// or a --twin-selected one like Slice E's counterparty) gets its own
// directory under ~/.water/twins/<id>, so its fictional connector records,
// decision cards and classification cache never land in the real twin's
// database (a demo "github" PR would otherwise sit next to the real GitHub
// connector's rows under the same source name).
func twinDataDir(id string) string {
	if id == realTwinID {
		return config.Home()
	}
	return filepath.Join(config.Home(), "twins", id)
}

// twinStorePath is id's database: store.DefaultPath() for the real twin.
func twinStorePath(id string) string {
	if id == realTwinID {
		return store.DefaultPath()
	}
	return filepath.Join(twinDataDir(id), "water.db")
}

// twinAuditPath is id's audit log: audit.DefaultPath() for the real twin.
func twinAuditPath(id string) string {
	if id == realTwinID {
		return audit.DefaultPath()
	}
	return filepath.Join(twinDataDir(id), "audit", "audit.jsonl")
}

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
	intents   *intents.Registry
	style     *render.Style
	roleMD    string
}

func (d *twinDeps) Close() {
	d.audit.Close()
	d.store.Close()
}

// loadRoleMD returns twins/<id>/role.md, falling back to twins/ceo/role.md
// for a twin that has none of its own (ceo-demo), or "" if neither exists.
func loadRoleMD(id string) string {
	if b, err := water.TwinsFS().ReadFile("twins/" + id + "/role.md"); err == nil {
		return string(b)
	}
	b, err := water.TwinsFS().ReadFile("twins/ceo/role.md")
	if err != nil {
		return ""
	}
	return string(b)
}

// buildCEORegistry constructs the connector registry a twin's manifest is
// checked against. Every function twin.yaml lists must be provided here.
// gcal, gmail and gdrive all read one shared water.google/ceo Keychain
// credential (see internal/connectors/google/gapi); `water connect google`
// writes it.
//
// demo adds the three fake, in-memory connectors twins/ceo-demo/twin.yaml
// grants (internal/connectors/fake/{github,linear,hubspot}.go): no network
// call, no OAuth, no API key, ever. False for the real ceo twin, exactly as
// before this existed. mailAddress is config's agent.mail_address (the
// agent's verified "Send mail as" alias): gmail.New reads it once here, not
// per call, and an empty value only matters if a write function is actually
// invoked (gmail.readMessageArgs then refuses with a clear error), so
// manifest-validation-only callers (loadTwinManifest) may pass "".
//
// id is the twin's own manifest id (the twinlink sender stamps it as
// from_twin and reads its peer table from the vault under it, and id ==
// demoTwinID selects the fake connectors), and st is the store twin
// messages are recorded in — nil for manifest-validation-only callers,
// which never send or read one.
//
// The real (non-demo) twin gets the real github/linear/hubspot connectors
// instead of the fakes, always registered — the same posture gcal/gmail/
// gdrive already have, deliberately not gated on vault.Default().Get(...)
// here: twins/ceo/twin.yaml grants their functions unconditionally (like
// every other function), so the gate's own ValidateManifest (New's first
// call) requires a connector to be registered for every non-B function the
// manifest lists, or daemon startup fails outright. Each real connector's
// own Invoke already resolves its vault credential lazily and returns a
// clear "not connected; run `water connect <name> --token ...`" error the
// instant it's actually called with nothing configured — so an owner who
// has never set up Linear or HubSpot sees exactly that per-call message,
// never a startup failure, which is what actually matters here. githubRepo
// is config's github.repo; github.New reads it once here, not per call, and
// an empty value only matters if list_prs/list_issues is actually invoked
// (github.Invoke then refuses with a clear "no repo configured" error), so
// manifest-validation-only callers (loadTwinManifest) may pass "".
//
// gsheets ("company_finance") is registered for the real twin only, no
// demo stand-in: it is read-only, named-lookup access to Renaissance_
// Finance, the company's real financial model (cash, burn, runway, budget,
// invoices, revenue, funding — see internal/connectors/google/gsheets'
// own doc comment for the full list), read through the Sheets API's
// values.get rather than Drive's files.export (which gdrive already uses
// for whole-file reads), since a specific named tab/range needs the
// Sheets API's own scope regardless of what Drive scope is granted. Rides
// the same shared water.google/ceo credential as gcal/gmail/gdrive — an
// owner connected before this scope existed needs one `water connect
// google` re-run to pick it up (see docs/google-setup.md).
//
// display (display.show) is registered for every CEO twin, real and demo:
// it has no network, credential or side effect beyond the UI (see
// internal/connectors/display).
//
// research (research.web) is registered for every CEO twin, real and demo:
// it runs a separate, cold `claude --print` process on the Claude
// subscription with only WebSearch/WebFetch (internal/connectors/research),
// no credential and no API key. buildCEORegistry registers it with the
// CLI's default model; callers that run the twin use buildCEORegistryModel
// with the manifest's fast model. Validation-only callers never invoke it.
func buildCEORegistry(id string, st *store.Store, mailAddress, signatureName, githubRepo string) (*connectors.Registry, error) {
	return buildCEORegistryModel(id, st, mailAddress, signatureName, githubRepo, "")
}

// buildCEORegistryModel is buildCEORegistry with the model research.web's
// subprocess runs on (the twin's fast tier, m.ModelFor(twins.TierFast)).
func buildCEORegistryModel(id string, st *store.Store, mailAddress, signatureName, githubRepo, researchModel string) (*connectors.Registry, error) {
	gm := gmail.New(mailAddress)
	gm.SetSignatureName(signatureName)
	cs := []connectors.Connector{gcal.New(), gm, gdrive.New(), agentmail.New(mailAddress),
		twinlink.NewSender(id, st), twinlink.NewInbox(st), display.New(),
		research.New(research.CLIRunner{Model: researchModel})}
	if id == demoTwinID {
		cs = append(cs,
			fake.NewGitHub(fake.DefaultGitHubPRs(), fake.DefaultGitHubIssues()),
			fake.NewLinear(fake.DefaultLinearIssues()...),
			fake.NewHubSpot(fake.DefaultHubSpotDeals(), fake.DefaultHubSpotContacts()),
		)
	} else {
		cs = append(cs, github.New(githubRepo), linear.New(), hubspot.New(), gsheets.New())
	}
	return connectors.NewRegistry(cs...)
}

// schemaFromRegistry adapts a connectors.Registry to the callback
// intents.LoadRegistry uses (LoadOptions.Schema) to check a write intent's
// proposer against the real payload shape its target function actually
// accepts, without intents (or internal/nervous/propose) ever importing
// internal/connectors themselves (docs/slices/R.md Design §1: the gateway/
// CLI wiring layer supplies connector schema lookups as a callback, not an
// import).
func schemaFromRegistry(reg *connectors.Registry) func(action string) (intents.SchemaInfo, bool) {
	return func(action string) (intents.SchemaInfo, bool) {
		_, fn, ok := reg.Lookup(action)
		if !ok {
			return intents.SchemaInfo{}, false
		}
		props := make([]string, 0, len(fn.Schema.Properties))
		for k := range fn.Schema.Properties {
			props = append(props, k)
		}
		return intents.SchemaInfo{Required: fn.Schema.Required, Properties: props}, true
	}
}

// intentFunctions is the Functions{Read, Write} every intents.LoadRegistry
// call against a real twin's embedded intents directory needs: once
// twins/ceo/intents/*.yaml declares a write intent (this task), omitting
// Write here would fail the whole load ("proposer ... is not registered"),
// not just leave write intents inactive.
func intentFunctions() intents.Functions {
	return intents.Functions{Read: reflex.Specs(), Write: propose.Specs()}
}

// loadTwinManifest loads and validates id's manifest, decision registry and
// connectors without opening the store or the audit log. It is what
// read-only commands (`water status`, `water doctor`) use: the audit log has
// one exclusive writer — the running daemon — and taking its lock from a
// read-only command would both fail while the daemon runs and, in the
// moment it held the lock, make a (re)starting daemon fail.
func loadTwinManifest(fsys fs.FS, id, mailAddress, signatureName, githubRepo string) (*twins.Manifest, error) {
	m, err := twins.Load(fsys, id)
	if err != nil {
		return nil, fmt.Errorf("twin manifest: %w", err)
	}
	if _, err := decisions.LoadRegistry(fsys, m); err != nil {
		return nil, fmt.Errorf("decision registry: %w", err)
	}
	reg, err := buildCEORegistry(id, nil, mailAddress, signatureName, githubRepo)
	if err != nil {
		return nil, err
	}
	if _, err := intents.LoadRegistry(fsys, m, intentFunctions(), intents.LoadOptions{Schema: schemaFromRegistry(reg)}); err != nil {
		return nil, fmt.Errorf("intent registry: %w", err)
	}
	if err := gate.ValidateManifest(m, reg); err != nil {
		return nil, err
	}
	return m, nil
}

// buildTwinDeps opens the store and the anchored, hash-chained audit log at
// their default ~/.water locations and wires the gate over them. Callers must
// Close() the result. id is realTwinID or demoTwinID (see (*App).twinID).
func buildTwinDeps(id, mailAddress, signatureName, githubRepo string) (*twinDeps, error) {
	return buildTwinDepsFS(water.TwinsFS(), id, mailAddress, signatureName, githubRepo)
}

// buildTwinDepsFS is buildTwinDeps parameterized over the twins filesystem,
// so a test can hand it a fixture tree (e.g. a deliberately malformed
// decisions/*.yaml) without touching the embedded twins/ceo/decisions
// directory. The manifest and the decision registry are both validated
// before anything else opens, so a bad file of either kind fails loudly here
// and never gets as far as touching the real store or audit log.
func buildTwinDepsFS(fsys fs.FS, id, mailAddress, signatureName, githubRepo string) (*twinDeps, error) {
	m, err := twins.Load(fsys, id)
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
	// A missing twins/<id>/style.yaml is not an error (render.DefaultStyle()),
	// so this only ever fails on a malformed file.
	style, err := render.LoadStyle(fsys, id)
	if err != nil {
		return nil, fmt.Errorf("style: %w", err)
	}
	st, err := store.Open(twinStorePath(id))
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	// The people roster (twins/<id>/seed/people.yaml, internal/roster) is
	// additive and optional — most twins (ceo-demo, counterparty) have
	// none, and roster.Load returns nil for that case. A present-but-
	// malformed file fails loudly here, before anything else opens, the
	// same posture every other twin file already has. Idempotent: safe to
	// re-run on every daemon startup.
	if err := roster.Load(context.Background(), fsys, id, st); err != nil {
		st.Close()
		return nil, fmt.Errorf("roster: %w", err)
	}
	reg, err := buildCEORegistryModel(id, st, mailAddress, signatureName, githubRepo, m.ModelFor(twins.TierFast))
	if err != nil {
		st.Close()
		return nil, err
	}
	// The intent registry loads and validates the same way, before anything
	// else opens: a bad twins/<id>/intents/*.yaml must stop the daemon here,
	// not surface later as a Tier 0 that silently never matches anything.
	// It needs reg (built just above) to check a write intent's proposer
	// against its target connector function's real payload schema.
	intentsReg, err := intents.LoadRegistry(fsys, m, intentFunctions(), intents.LoadOptions{Schema: schemaFromRegistry(reg)})
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("intent registry: %w", err)
	}
	// *store.Store implements audit.Anchor directly (LoadAuditAnchor /
	// SaveAuditAnchor), so the audit log's tail is anchored in the same
	// database the gate persists rate/usage windows to.
	log, err := audit.Open(twinAuditPath(id), audit.WithAnchor(st))
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("audit: %w", err)
	}
	q := approvals.NewQueue(st, log)
	v := vault.Default()
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: v, Store: st})
	if err != nil {
		log.Close()
		st.Close()
		return nil, err
	}
	return &twinDeps{manifest: m, store: st, audit: log, approvals: q, gate: g, registry: reg, vault: v, decisions: decisionsReg, intents: intentsReg, style: style, roleMD: loadRoleMD(id)}, nil
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
//
// R-24 wrapped the classifier with decider.Wrap(decider.Null{}, ...) before
// StoreCache sees it, hardcoded since decider.provider had no settable
// config key yet. R-25 sources the decider from deciderProvider
// (cfg.Decider.Provider) instead via decider.New: since apply() rejects
// anything but "none" (internal/config/config.go), decider.New always
// returns Null{} in practice today, and Wrap with a Null decider returns
// the ModelClassifier UNCHANGED (see decider.Wrap's doc comment) — so this
// is still provably a no-op, just sourced from config now rather than
// hardcoded. A decider.New error (unreachable given apply()'s own
// validation) falls back to Null{} rather than failing daemon startup over
// a decider nothing outward-facing depends on.
// TestBuildDecisionsTriggerWithNullDeciderMatchesUnwrappedClassifier in
// twin_test.go is the regression guard.
//
// cardTTL (config decisions.card_ttl_seconds) is how long the trigger
// reuses its last pass of cards; see decisions.Trigger.CardTTL.
func buildDecisionsTrigger(deps *twinDeps, be backend.Backend, deciderProvider string, cardTTL time.Duration) *decisions.Trigger {
	model := deps.manifest.ModelFor(twins.TierFast)
	charge := func() error { return deps.gate.ModelCall(gate.P1) }
	classifier := &decisions.ModelClassifier{Registry: deps.decisions, Backend: be, Model: model, Charge: charge}
	dec, err := decider.New(deciderProvider)
	if err != nil {
		dec = decider.Null{}
	}
	wrapped := decider.Wrap(dec, classifier, deps.decisions)
	cached := &decisions.StoreCache{Store: deps.store, Inner: wrapped}
	triager, err := decisions.NewTriager(cached, decisions.Candidate)
	if err != nil {
		return nil
	}
	builder := &decisions.Builder{
		Registry: deps.decisions, Gate: deps.gate, Origin: gate.P1,
		Phraser: &decisions.ModelPhraser{Backend: be, Model: model, Charge: charge},
	}
	return &decisions.Trigger{Store: deps.store, Triager: triager, Builder: builder, CardTTL: cardTTL}
}
