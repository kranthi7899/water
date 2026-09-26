package cli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"water"
	"water/internal/agentmail"
	"water/internal/approvals"
	"water/internal/backend"
	"water/internal/config"
	"water/internal/connectors"
	"water/internal/connectors/google/gapi"
	"water/internal/gate"
	"water/internal/gateway"
	"water/internal/needsyou"
	"water/internal/nervous"
	"water/internal/nervous/intents"
	"water/internal/nervous/promote"
	"water/internal/nervous/render"
	"water/internal/nervous/turn"
	"water/internal/runtime"
	"water/internal/store"
	watersync "water/internal/sync"
	"water/internal/twins"
)

// daemonCmd is the top-level `water daemon` command group.
func (a *App) daemonCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "daemon",
		Short: "Run the water daemon: gate, approvals, audit, store and model sessions behind one socket",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runDaemon(cmd.Context())
		},
	}
	c.AddCommand(a.daemonInstallCmd(), a.daemonUninstallCmd(), a.daemonTokenCmd())
	return c
}

// daemonTaskControl satisfies reflex.TaskControl (control.stop's access to
// in-flight-turn cancellation) by forwarding to *gateway.Daemon. It exists
// only to break the construction order: *nervous.Nervous is built before
// the *gateway.Daemon that owns the real task bookkeeping, since Nervous is
// itself one of gateway.Config's fields. d is set once, right after
// gateway.New returns.
type daemonTaskControl struct{ d *gateway.Daemon }

func (t *daemonTaskControl) Running() int {
	if t.d == nil {
		return 0
	}
	return t.d.RunningTasks()
}

func (t *daemonTaskControl) CancelAllExcept(taskID string) int {
	if t.d == nil {
		return 0
	}
	return t.d.CancelTasksExcept(taskID)
}

// daemonActionSink satisfies nervous.ActionSink (a matched write intent's
// proposal reaching the approval queue) by forwarding to *gateway.Daemon's
// ProposeAction, which queues an envelope through the exact same
// proposeEnvelope path a model-initiated tool call uses. Same
// construction-order break as daemonTaskControl/daemonPrewarmer above: d is
// set once, right after gateway.New returns.
type daemonActionSink struct{ d *gateway.Daemon }

func (s *daemonActionSink) ProposeAction(ctx context.Context, fn string, payload map[string]any, ch runtime.Channel) (approvals.Envelope, error) {
	if s.d == nil {
		return approvals.Envelope{}, fmt.Errorf("nervous: ActionSink not ready yet")
	}
	return s.d.ProposeAction(ctx, fn, payload, ch)
}

// daemonIntentsReloader rebuilds the twin's intents registry from its
// current on-disk state — the embedded intent files, the learned overlay
// directory (only when router.promotion.enabled), and the store's
// intent_state table — and atomically swaps it into *nervous.Nervous
// (gateway.Config.ReloadIntents; POST /v1/intents/reload, R-23). It is the
// same assembly buildTwinDepsFS does once at startup, run again on demand
// after `water intent promote`/`demote`/`enable` (R-26) writes a change, and
// once here at startup (right after nv is built) so a daemon that starts
// with the flag already on, or with existing disabled-intent rows, picks
// them up before it ever serves a turn — buildTwinDepsFS's own
// intents.LoadRegistry call never sets LoadOptions.Learned/Disabled at all.
type daemonIntentsReloader struct {
	manifest    *twins.Manifest
	connectors  *connectors.Registry
	store       *store.Store
	home        string
	promotionOn bool
	nv          *nervous.Nervous
}

func (r *daemonIntentsReloader) Reload(ctx context.Context) error {
	opts := intents.LoadOptions{Schema: schemaFromRegistry(r.connectors)}
	if r.promotionOn {
		opts.Learned = os.DirFS(promote.LearnedDir(r.home, r.manifest.ID))
	}
	if r.store != nil {
		disabled, err := r.store.ListIntentStates(ctx)
		if err != nil {
			return fmt.Errorf("intents reload: %w", err)
		}
		opts.Disabled = disabled
	}
	reg, err := intents.LoadRegistry(water.TwinsFS(), r.manifest, intentFunctions(), opts)
	if err != nil {
		return fmt.Errorf("intents reload: %w", err)
	}
	r.nv.Reload(reg)
	return nil
}

// daemonApprover satisfies nervous.Approver (a bound voice yes/no deciding a
// pending envelope, R-21) by forwarding to *gateway.Daemon's DecideBound,
// which runs the exact same decideAndExecute path POST
// /v1/approvals/{id}/decision uses. Same construction-order break as
// daemonActionSink/daemonTaskControl/daemonPrewarmer above: d is set once,
// right after gateway.New returns.
type daemonApprover struct{ d *gateway.Daemon }

func (a *daemonApprover) DecideBound(ctx context.Context, id, payloadHash, reply string) (nervous.DecisionOutcome, error) {
	if a.d == nil {
		return nervous.DecisionOutcome{}, fmt.Errorf("nervous: Approver not ready yet")
	}
	return a.d.DecideBound(ctx, id, payloadHash, reply)
}

// voiceApproveDomains parses router.voice_approve.internal_domains (a
// comma-separated string) into the []string VoiceApprovalTier scans.
// Whitespace around each entry is trimmed, and empty entries are dropped, so
// "" (the default) and "a.com, ,b.com" both behave as documented: no domain
// at all means every recipient is external.
func voiceApproveDomains(csv string) []string {
	var out []string
	for _, d := range strings.Split(csv, ",") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// buildNervousConfig maps every docs/slices/R.md Design §17 router.* key
// this task (R-25) settles onto the subset of nervous.Config that has a
// real field for it, starting from nervous.DefaultConfig() so every field
// Design §17 does not name a key for (Clock, SenderWindow, SenderLimit,
// Breaker.MissSample/MinMissSamples, ...) keeps its existing code default.
// It performs no I/O, so it can be called directly by a test without
// booting a real daemon — the regression test this task's own brief asks
// for (a resolved config with non-default values produces a matching
// nervous.Config) targets this function.
func buildNervousConfig(cfg *config.Resolved) nervous.Config {
	nvCfg := nervous.DefaultConfig()
	nvCfg.Tier0Enabled = cfg.Router.Tier0.Enabled
	nvCfg.MainEnabled = cfg.Router.Main.Enabled
	nvCfg.Breaker = nervous.BreakerConfig{
		Failures:       cfg.Router.Breaker.Failures,
		Cooldown:       time.Duration(cfg.Router.Breaker.CooldownSeconds) * time.Second,
		MaxMissRatePct: cfg.Router.Breaker.MaxMissRatePct,
		// MissSample/MinMissSamples have no config key (Design §17's table
		// doesn't list them): keep the code defaults.
		MissSample:     nervous.DefaultBreakerConfig().MissSample,
		MinMissSamples: nervous.DefaultBreakerConfig().MinMissSamples,
	}
	nvCfg.MissWindow = time.Duration(cfg.Router.PossibleMissWindowSeconds) * time.Second
	nvCfg.Retention = time.Duration(cfg.Router.LogRetentionDays) * 24 * time.Hour
	nvCfg.AckAfter = time.Duration(cfg.Router.AckMS) * time.Millisecond
	nvCfg.Speculation = cfg.Router.Speculation.Enabled
	nvCfg.VoiceApprove = nervous.VoiceApproveConfig{
		Enabled:         cfg.Router.VoiceApprove.Enabled,
		Window:          time.Duration(cfg.Router.VoiceApprove.WindowSeconds) * time.Second,
		InternalDomains: voiceApproveDomains(cfg.Router.VoiceApprove.InternalDomains),
	}
	nvCfg.Promotion = nervous.PromotionConfig{
		DemoteMinSamples:  cfg.Router.Promotion.DemoteMinSamples,
		DemoteMissRatePct: cfg.Router.Promotion.DemoteMissRatePct,
	}
	return nvCfg
}

// Note: router.tier0.timeout_ms and router.quick_tools.enabled are settable
// (they round-trip through internal/config exactly like every other Design
// §17 key, and TestEveryKeyRoundTrips/TestRouterConfigDefaults cover them)
// but have no live consumer wired here: nervous.Config has no per-tier
// context-deadline field (Tier 0 runs no I/O to bound), and
// router.quick_tools.enabled's only real gate point is
// gateway.Daemon.TwinToolPolicy's Quick field, in internal/gateway/
// daemon.go, outside this task's file list (internal/config/config.go,
// config_test.go, internal/cli/cmd_daemon.go). Wiring either belongs to a
// later task, not invented here.

// daemonPrewarmer wires nervous.Config.Prewarm to the real warm session,
// warming it with exactly the request — system prompt, model, tool policy —
// a real main-path turn would use (Design §11.5), so a successful prewarm
// actually avoids the real turn's own cold start rather than warming under
// a different key that just gets restarted anyway. Like daemonTaskControl
// above, d is set once, right after gateway.New returns, to break the same
// construction-order cycle (Prewarm needs the twin's tool policy, which
// only *gateway.Daemon can build).
type daemonPrewarmer struct {
	warm     *backend.WarmSession
	roleMD   string
	manifest *twins.Manifest
	d        *gateway.Daemon
	// styleBlock must be the same string gateway.Config.StyleBlock gets:
	// WarmSession restarts its process whenever System differs, so a
	// prewarm without it would be thrown away on the first real turn
	// (docs/slices/V.md D5; TestPrewarmSystemMatchesTurnSystem).
	styleBlock string
}

func (p *daemonPrewarmer) Prewarm(ctx context.Context) (string, error) {
	if p.warm == nil {
		return "skipped", nil
	}
	return p.warm.Prewarm(ctx, p.request())
}

// request is the prewarm request: the system prompt, model and tool policy
// a real main-path turn sends. Once the daemon is wired (always, by the
// time a prewarm fires) the system prompt is the daemon's own
// SystemPrompt, so the two cannot drift; before that it is built from the
// same role and style strings runDaemon hands gateway.Config.
func (p *daemonPrewarmer) request() backend.Request {
	req := backend.Request{
		System: runtime.RoleSystem(runtime.Env{RoleMD: p.roleMD, StyleBlock: p.styleBlock}),
		Model:  p.manifest.ModelFor(twins.TierFast),
	}
	if p.d != nil {
		req.System = p.d.SystemPrompt()
		req.Tools = p.d.TwinToolPolicy()
	}
	return req
}

// styleMaxChars is style.yaml's max_chars for each turn channel.
func styleMaxChars(s *render.Style) map[runtime.Channel]int {
	out := map[runtime.Channel]int{}
	for _, ch := range []runtime.Channel{runtime.ChannelCLI, runtime.ChannelTextBar, runtime.ChannelVoice} {
		if n := s.MaxChars(string(ch)); n > 0 {
			out[ch] = n
		}
	}
	return out
}

func (a *App) runDaemon(ctx context.Context) error {
	cfg, err := a.config()
	if err != nil {
		return err
	}
	a.configureBackends(cfg)

	// The single-instance lock comes first, so a second daemon fails on it
	// with a clear message rather than on the audit log's writer lock.
	paths := gateway.Paths{Home: config.Home()}
	l, unlock, err := gateway.Listen(paths)
	if err != nil {
		return exitWith(ExitError, fmt.Errorf("another water daemon may already be running: %w", err))
	}
	defer unlock()
	defer l.Close()

	deps, err := buildTwinDeps(a.twinID(), cfg.Agent.MailAddress, cfg.Agent.SignatureName, cfg.GitHub.Repo)
	if err != nil {
		return exitWith(ExitError, err)
	}
	defer deps.Close()

	sel, err := a.selectBackend(ctx)
	if err != nil {
		return err
	}
	if w := meteredLeakWarning(sel); w != "" {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}

	clients, err := gateway.LoadClients(paths.ClientsPath())
	if err != nil {
		return err
	}
	cliToken, err := clients.EnsureCLI()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "water daemon: twin=%s socket=%s cli-token=%s\n", deps.manifest.ID, paths.SocketPath(), cliToken)

	// Defined early (moved ahead of its original single use, agentWatcher's
	// Config.Logf, below) so nvCfg.Logf (R-23's auto-demotion bookkeeping
	// errors) can use the same "water daemon: ..." stderr convention every
	// other background error in this process already uses.
	logf := func(format string, args ...any) { fmt.Fprintf(os.Stderr, "water daemon: "+format+"\n", args...) }

	var warm *backend.WarmSession
	if sel.Backend.Name() == backend.ClaudeSubscriptionName {
		warm = backend.NewWarmSession(backend.WarmSessionConfig{WorkDir: config.Home()})
		defer warm.Close()
	}

	// trigger wires the decision registry loaded in buildTwinDeps to this
	// process's own gate/store/backend; shared between the daemon's
	// /v1/decisions endpoint and the morning brief's open-cards signal so
	// both see the same in-memory classification cache.
	trigger := buildDecisionsTrigger(deps, sel.Backend, cfg.Decider.Provider)

	// needsYouSvc computes docs/slices/V.md §5's shared "needs you"
	// threshold (Slice V-3/V-4): decisions ranked/filtered by
	// notify.min_severity, approvals past notify.approval_grace_seconds.
	// GET /v1/today (gateway.Config.NeedsYou, below) only ever reads its
	// last snapshot; recomputation happens on its own tick, started below
	// once sigCtx exists, deliberately independent of the
	// gcal.list_events-gated refresher (docs/slices/V.md's own finding:
	// nesting this inside watersync.Refresher would mean a twin with no
	// calendar grant — the demo twin — never gets notifications at all). A
	// nil trigger (buildDecisionsTrigger's one documented, practically
	// unreachable failure mode) leaves needsYouSvc nil rather than boxing a
	// nil *decisions.Trigger into needsyou.DecisionSource, matching the
	// same nil-interface guard briefEnv.Decisions uses below.
	var needsYouSvc *needsyou.Service
	if trigger != nil {
		needsYouSvc = needsyou.NewService(trigger, deps.approvals, deps.store, cfg.Notify.MinSeverity, cfg.Notify.ApprovalGrace())
	}

	// Slice R's sous chef reads through a separate read-only connection pool
	// (Design §1(e)'s defense in depth: a write attempt fails at the SQLite
	// level even if a reflex handler somehow tried one), opened on the same
	// file the writer above already migrated.
	readStore, err := store.OpenReadOnly(twinStorePath(deps.manifest.ID), 4)
	if err != nil {
		return exitWith(ExitError, fmt.Errorf("route read pool: %w", err))
	}
	defer readStore.Close()

	// tc is a thin reflex.TaskControl adapter over the daemon's existing
	// task-cancellation bookkeeping: it needs *gateway.Daemon, which doesn't
	// exist until after *nervous.Nervous is built (Nervous itself is a
	// gateway.Config field), so it's wired in two steps.
	tc := &daemonTaskControl{}
	// prewarmer wires R-19's speculative prefetch to the same warm session
	// and startup path a real main-path turn uses (Design §11.5): it needs
	// *gateway.Daemon for the twin's tool policy, which doesn't exist until
	// after *nervous.Nervous — a gateway.Config field — is built, so it's
	// wired in the same two-step way tc is, just below.
	// styleBlock and maxChars carry style.yaml into the main path
	// (docs/slices/V.md D5): the block goes into the system prompt of every
	// turn AND of the prewarm (both from this one string), the caps into
	// each turn's channel hint.
	styleBlock := deps.style.PromptBlock()
	maxChars := styleMaxChars(deps.style)
	prewarmer := &daemonPrewarmer{warm: warm, roleMD: deps.roleMD, manifest: deps.manifest, styleBlock: styleBlock}
	// as wires a matched write intent's proposal to the approval queue
	// (Design §12); it needs *gateway.Daemon, which doesn't exist until
	// after *nervous.Nervous is built, so it's wired in the same two-step
	// way tc/prewarmer are, just below.
	as := &daemonActionSink{}
	// ap wires a bound voice yes/no to decideAndExecute (Design §13, R-21);
	// same two-step construction-order break as as/tc/prewarmer.
	ap := &daemonApprover{}
	// reloader rebuilds the intents registry on demand (POST
	// /v1/intents/reload, R-23) and also runs once, synchronously, right
	// after nv is built below, to pick up the learned overlay/intent_state
	// before this daemon ever serves a turn — same two-step
	// construction-order pattern as as/ap/tc/prewarmer (it needs
	// *nervous.Nervous itself, which doesn't exist until nervous.New
	// returns).
	reloader := &daemonIntentsReloader{
		manifest: deps.manifest, connectors: deps.registry, store: deps.store,
		home: config.Home(), promotionOn: cfg.Router.Promotion.Enabled,
	}
	nvCfg := buildNervousConfig(cfg)
	nvCfg.Registry = func() *intents.Registry { return deps.intents }
	nvCfg.Style = deps.style
	nvCfg.Turns = turn.NewTable(time.Now)
	nvCfg.Store = deps.store
	nvCfg.ReadStore = readStore
	nvCfg.Tasks = tc
	nvCfg.Manifest = deps.manifest
	nvCfg.Approvals = deps.approvals
	nvCfg.Prewarm = prewarmer.Prewarm
	nvCfg.Actions = as
	nvCfg.Approver = ap
	nvCfg.Logf = logf
	nv, err := nervous.New(nvCfg)
	if err != nil {
		return exitWith(ExitError, fmt.Errorf("nervous: %w", err))
	}
	reloader.nv = nv
	// Run once at startup, synchronously: a daemon that starts with
	// router.promotion.enabled already on (or with pre-existing disabled
	// learned intents from a previous run) must serve its very first turn
	// against the real overlay/disabled state, not the bare registry
	// buildTwinDepsFS loaded with no LoadOptions.Learned/Disabled at all.
	// Failure here is logged, never fatal: the daemon still starts and
	// serves from the base registry, exactly the same "owner data must
	// never block the daemon" posture the learned overlay's own loader
	// already has.
	if err := reloader.Reload(context.Background()); err != nil {
		logf("initial intents overlay load: %v", err)
	}

	d := gateway.New(gateway.Config{
		Manifest: deps.manifest, Store: deps.store, Audit: deps.audit, Approvals: deps.approvals,
		Gate: deps.gate, Registry: deps.registry, Backend: sel.Backend, Warm: warm, RoleMD: deps.roleMD,
		Decisions: trigger, ProactiveCues: cfg.Meetings.ProactiveCues, Clients: clients, SocketPath: paths.SocketPath(),
		Nervous: nv,
		Home:    config.Home(), PromotionEnabled: cfg.Router.Promotion.Enabled, MaxLearned: cfg.Router.Promotion.MaxLearned,
		ReloadIntents: reloader.Reload,
		NeedsYou:      needsYouSvc,
		StyleBlock:    styleBlock,
		MaxChars:      maxChars,
	})
	tc.d = d
	prewarmer.d = d
	as.d = d
	ap.d = d

	// Every request context derives from baseCtx, which shutdown cancels
	// first: http.Server.Shutdown alone never cancels in-flight handlers, so
	// a streaming turn would otherwise outlive the 5s shutdown bound, keep
	// the warm session busy, and leave its claude process group (its own
	// pgid) orphaned if launchd then SIGKILLs the daemon.
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	srv := &http.Server{Handler: d.Mux(), BaseContext: func(net.Listener) context.Context { return baseCtx }}
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// briefEnv is a plain (non-per-turn) runtime.Env, just enough to compute
	// and cache the morning brief from the background sync loop — the same
	// ComputeAndCacheBrief the on-demand fast path calls, so the two share
	// the compute-once guard for the same day.
	// No Warm: the brief has its own system prompt and no tools, so it runs
	// on the cold backend path (doComputeBrief enforces this too) and never
	// restarts the chat's warm process.
	briefEnv := runtime.Env{
		Manifest: deps.manifest, Store: deps.store, Approvals: deps.approvals,
		RoleMD: deps.roleMD, Backend: sel.Backend,
	}
	// A nil *decisions.Trigger boxed straight into the DecisionSource
	// interface would be a non-nil interface over a nil pointer; only set
	// the field when trigger is actually non-nil (see buildDecisionsTrigger).
	if trigger != nil {
		briefEnv.Decisions = trigger
	}

	// The agent-mailbox inbound-triage watcher (internal/agentmail): its own
	// vault credential (account "agent"), its own model classification
	// (charged against the same usage cap every other model call uses), its
	// own tick (see AgentMail below). ForwardTo empty just means a
	// CEO-directed message is logged, never staged, until the CEO sets
	// agent.forward_to.
	agentWatcher := agentmail.NewWatcher(agentmail.Config{
		Gate: deps.gate, Store: deps.store, Vault: deps.vault, Approvals: deps.approvals,
		Classifier: &agentmail.Classifier{
			Backend: sel.Backend, Model: deps.manifest.ModelFor(twins.TierFast),
			Charge: func() error { return deps.gate.ModelCall(gate.P1) },
		},
		ForwardTo: cfg.Agent.ForwardTo,
		Logf:      logf,
	})

	// The background Google refresh (internal/sync): P2, gate-mediated, skips
	// quietly if nothing is connected, and stops with the rest of the daemon
	// because it shares sigCtx. Mail and calendar refresh on independent
	// intervals; the (slower) calendar tick also drives the morning brief's
	// background precompute once it's past brief.ready_after. AgentMail is a
	// third, independent tick following the exact same pattern.
	refresher := watersync.New(watersync.Config{
		Gate: deps.gate, Vault: deps.vault, Store: deps.store,
		Service: gapi.Service, Account: gapi.DefaultAccount,
		EventsInterval: cfg.Sync.Interval(), MailInterval: cfg.Sync.MailInterval(),
		Logf: logf,
		Brief: func(ctx context.Context) error {
			// Bounded even though each model call is: the background
			// precompute must never hold the refresher loop indefinitely.
			bctx, cancel := context.WithTimeout(ctx, briefPrecomputeTimeout)
			defer cancel()
			_, err := runtime.ComputeAndCacheBrief(bctx, briefEnv)
			return err
		},
		BriefReadyAfter: cfg.Brief.ReadyAfter,
		AgentMail:       agentWatcher.Tick,
	})
	// A twin whose manifest grants no Google reads at all (Slice E's
	// counterparty) has nothing to sync: running the refresher would only
	// log a gate denial every tick, since on a shared machine the CEO's own
	// Google credential is in the same Keychain.
	if _, ok := deps.manifest.Function("gcal.list_events"); ok {
		go refresher.Run(sigCtx)
	}

	// needsYouSvc's own tick: deliberately not gated on gcal.list_events (see
	// its construction comment above) and not nested inside refresher, so a
	// twin with no calendar grant still gets decision/approval
	// notifications. Ticks immediately, then on notify.interval_seconds,
	// matching watersync.Refresher.loop's own "tick now, then on the
	// ticker" shape (internal/sync/sync.go); stops with the rest of the
	// daemon since it shares sigCtx. A tick failure is logged and retried
	// next interval, never fatal — the same posture every other background
	// loop in this function already has.
	if needsYouSvc != nil {
		go func() {
			tick := func() {
				if err := needsYouSvc.Tick(sigCtx, time.Now()); err != nil {
					logf("needsyou tick: %v", err)
				}
			}
			tick()
			t := time.NewTicker(cfg.Notify.Interval())
			defer t.Stop()
			for {
				select {
				case <-sigCtx.Done():
					return
				case <-t.C:
					tick()
				}
			}
		}()
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(l) }()
	select {
	case <-sigCtx.Done():
		// Cancel in-flight turns and requests first (a warm turn then kills
		// its process and releases the session), then drain. An approved
		// action's execution is detached from its request and keeps its own
		// bound; if it outlasts the drain, Close drops the connections and
		// the deferred warm.Close/deps.Close still run in order.
		cancelBase()
		if warm != nil {
			warm.Close()
		}
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shCtx); err != nil {
			_ = srv.Close()
			return err
		}
		return nil
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	}
}

// briefPrecomputeTimeout bounds one background morning-brief compute.
const briefPrecomputeTimeout = 3 * time.Minute

func (a *App) daemonTokenCmd() *cobra.Command {
	c := &cobra.Command{Use: "token", Short: "Manage daemon client bearer tokens"}
	c.AddCommand(&cobra.Command{
		Use:   "new <name>",
		Short: "Mint a bearer token for a new client (a Shortcut, the Swift app, ...)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clients, err := gateway.LoadClients(gateway.Paths{Home: config.Home()}.ClientsPath())
			if err != nil {
				return err
			}
			tok, err := clients.New(args[0])
			if err != nil {
				return err
			}
			fmt.Println(tok)
			return nil
		},
	})
	return c
}

const launchAgentLabel = "com.water.daemon"

// launchdDomain is the per-user GUI launchd domain the agent loads into.
func launchdDomain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

// launchdTarget is the agent's service target within launchdDomain.
func launchdTarget() string { return launchdDomain() + "/" + launchAgentLabel }

// launchAgentLoaded reports whether launchd currently has the agent loaded.
func launchAgentLoaded() bool {
	return exec.Command("launchctl", "print", launchdTarget()).Run() == nil
}

// waitLaunchAgentUnloaded polls until a bootout has taken effect (bootout
// is asynchronous) or d passes.
func waitLaunchAgentUnloaded(d time.Duration) {
	deadline := time.Now().Add(d)
	for launchAgentLoaded() && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
}

func launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist"), nil
}

func (a *App) daemonInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install the daemon as a macOS LaunchAgent (KeepAlive, logs under ~/.water/logs)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			waterBin, err := os.Executable()
			if err != nil {
				return err
			}
			// launchd's PATH is minimal, so claude's absolute path is
			// resolved now and baked into the plist rather than relied on at
			// launch time.
			claudeBin, lookErr := exec.LookPath("claude")
			claudeDir := "/usr/local/bin"
			if lookErr == nil {
				claudeDir = filepath.Dir(claudeBin)
			}
			home := config.Home()
			logDir := filepath.Join(home, "logs")
			if err := os.MkdirAll(logDir, 0o755); err != nil {
				return err
			}
			path, err := launchAgentPath()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			pa := launchAgentPlistArgs{
				Label: launchAgentLabel, WaterBin: waterBin, ExtraPathDir: claudeDir,
				StdoutLog: filepath.Join(logDir, "daemon.log"), StderrLog: filepath.Join(logDir, "daemon.err.log"),
				Demo: a.twinID() == demoTwinID,
			}
			if os.Getenv("WATER_HOME") != "" {
				if pa.WaterHome, err = filepath.Abs(home); err != nil {
					return err
				}
			}
			// A reinstall (e.g. after a rebuild) must replace a loaded
			// daemon: launchd refuses to bootstrap a label that is already
			// loaded, and the old daemon would keep serving the old binary.
			// Unload first, before the plist on disk changes.
			restarted := launchAgentLoaded()
			if restarted {
				_, _ = exec.Command("launchctl", "bootout", launchdTarget()).CombinedOutput()
				waitLaunchAgentUnloaded(5 * time.Second)
			}
			if err := os.WriteFile(path, []byte(renderLaunchAgentPlist(pa)), 0o644); err != nil {
				return err
			}
			if lookErr != nil {
				fmt.Fprintln(os.Stderr, "warning: claude was not found on PATH at install time; add it to PATH before the daemon starts")
			}
			var out []byte
			for attempt := 0; attempt < 5; attempt++ {
				if out, err = exec.Command("launchctl", "bootstrap", launchdDomain(), path).CombinedOutput(); err == nil {
					break
				}
				time.Sleep(500 * time.Millisecond)
			}
			if err != nil {
				return fmt.Errorf("launchctl bootstrap: %w: %s (try `water daemon uninstall` then install again)", err, out)
			}
			if restarted {
				fmt.Println("reinstalled (restarted running daemon)", path)
			} else {
				fmt.Println("installed", path)
			}
			return nil
		},
	}
}

func (a *App) daemonUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the daemon's LaunchAgent",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := launchAgentPath()
			if err != nil {
				return err
			}
			_, _ = exec.Command("launchctl", "bootout", launchdDomain(), path).CombinedOutput()
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			fmt.Println("uninstalled", path)
			return nil
		},
	}
}
