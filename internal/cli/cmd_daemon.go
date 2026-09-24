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
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"water/internal/agentmail"
	"water/internal/backend"
	"water/internal/config"
	"water/internal/connectors/google/gapi"
	"water/internal/gate"
	"water/internal/gateway"
	"water/internal/runtime"
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

	deps, err := buildTwinDeps(a.twinID(), cfg.Agent.MailAddress, cfg.Agent.SignatureName)
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

	var warm *backend.WarmSession
	if sel.Backend.Name() == backend.ClaudeSubscriptionName {
		warm = backend.NewWarmSession(backend.WarmSessionConfig{WorkDir: config.Home()})
		defer warm.Close()
	}

	// trigger wires the decision registry loaded in buildTwinDeps to this
	// process's own gate/store/backend; shared between the daemon's
	// /v1/decisions endpoint and the morning brief's open-cards signal so
	// both see the same in-memory classification cache.
	trigger := buildDecisionsTrigger(deps, sel.Backend)

	d := gateway.New(gateway.Config{
		Manifest: deps.manifest, Store: deps.store, Audit: deps.audit, Approvals: deps.approvals,
		Gate: deps.gate, Registry: deps.registry, Backend: sel.Backend, Warm: warm, RoleMD: deps.roleMD,
		Decisions: trigger, ProactiveCues: cfg.Meetings.ProactiveCues, Clients: clients, SocketPath: paths.SocketPath(),
	})

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

	logf := func(format string, args ...any) { fmt.Fprintf(os.Stderr, "water daemon: "+format+"\n", args...) }

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
	go refresher.Run(sigCtx)

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
			plist := renderLaunchAgentPlist(launchAgentPlistArgs{
				Label: launchAgentLabel, WaterBin: waterBin, ExtraPathDir: claudeDir,
				StdoutLog: filepath.Join(logDir, "daemon.log"), StderrLog: filepath.Join(logDir, "daemon.err.log"),
			})
			if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
				return err
			}
			if lookErr != nil {
				fmt.Fprintln(os.Stderr, "warning: claude was not found on PATH at install time; add it to PATH before the daemon starts")
			}
			out, err := exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), path).CombinedOutput()
			if err != nil {
				return fmt.Errorf("launchctl bootstrap: %w: %s", err, out)
			}
			fmt.Println("installed", path)
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
			_, _ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d", os.Getuid()), path).CombinedOutput()
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			fmt.Println("uninstalled", path)
			return nil
		},
	}
}
