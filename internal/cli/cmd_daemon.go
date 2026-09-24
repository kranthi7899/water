package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"water/internal/backend"
	"water/internal/config"
	"water/internal/connectors/google/gapi"
	"water/internal/gateway"
	watersync "water/internal/sync"
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

	deps, err := buildTwinDeps()
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

	paths := gateway.Paths{Home: config.Home()}
	l, unlock, err := gateway.Listen(paths)
	if err != nil {
		return exitWith(ExitError, fmt.Errorf("another water daemon may already be running: %w", err))
	}
	defer unlock()
	defer l.Close()

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

	d := gateway.New(gateway.Config{
		Manifest: deps.manifest, Store: deps.store, Audit: deps.audit, Approvals: deps.approvals,
		Gate: deps.gate, Registry: deps.registry, Backend: sel.Backend, Warm: warm, RoleMD: deps.roleMD,
		Clients: clients, SocketPath: paths.SocketPath(),
	})

	srv := &http.Server{Handler: d.Mux()}
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The background Google refresh (internal/sync): P2, gate-mediated, skips
	// quietly if nothing is connected, and stops with the rest of the daemon
	// because it shares sigCtx.
	refresher := watersync.New(watersync.Config{
		Gate: deps.gate, Vault: deps.vault, Service: gapi.Service, Account: gapi.DefaultAccount,
		Interval: cfg.Sync.Interval(),
		Logf:     func(format string, args ...any) { fmt.Fprintf(os.Stderr, "water daemon: "+format+"\n", args...) },
	})
	go refresher.Run(sigCtx)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(l) }()
	select {
	case <-sigCtx.Done():
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shCtx)
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	}
}

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
