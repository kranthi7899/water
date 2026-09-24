// Package cli is the cobra command tree. It wires the packages together and
// owns nothing else.
package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/term"

	"water/internal/backend"
	"water/internal/config"
)

// Exit codes that mean something.
const (
	ExitOK           = 0
	ExitError        = 1
	ExitUsage        = 2 // bad args / prerequisites missing
	ExitBackend      = 3 // no usable backend or metered refused
	ExitUnconfigured = 4 // run `water onboard`
	ExitRateLimited  = 5 // subscription window exhausted; resume later
)

// ExitError carries a code through cobra's error return.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func exitWith(code int, err error) error { return &exitError{code: code, err: err} }

// Version info, set via -ldflags at release time.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// globalFlags are the persistent flags shared by every command.
type globalFlags struct {
	output       string
	jsonOut      bool
	backend      string
	traceDir     string
	yes          bool
	quiet        bool
	verbose      bool
	voice        bool
	allowMetered bool
	debug        bool
	demo         bool
	twin         string
}

// App holds lazily-built shared state for one invocation.
type App struct {
	flags globalFlags
	cfg   *config.Resolved
}

// NewApp builds an App. There is one twin (the CEO); its manifest and
// connectors are loaded per-command via buildTwinDeps, not held here.
func NewApp() *App { return &App{} }

func (a *App) config() (*config.Resolved, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}
	over := map[string]string{}
	if a.flags.backend != "" {
		over["backend.preferred"] = a.flags.backend
	}
	if a.flags.traceDir != "" {
		over["telemetry.trace_dir"] = a.flags.traceDir
	}
	if a.flags.allowMetered {
		over["backend.allow_metered"] = "true"
	}
	cfg, err := config.Load(over)
	if err != nil {
		return nil, err
	}
	a.cfg = cfg
	return cfg, nil
}

// selectBackend resolves the one global backend via the single selection
// function. There are no more per-role overrides (a.k.a. role.yaml backend:)
// now that there is one twin; --backend / config.backend.preferred / auto is
// the whole precedence chain.
func (a *App) selectBackend(ctx context.Context) (backend.Selection, error) {
	cfg, err := a.config()
	if err != nil {
		return backend.Selection{}, err
	}
	a.configureBackends(cfg)
	sc := backend.SelectConfig{Flag: a.flags.backend, AllowMetered: cfg.Backend.AllowMetered}
	if a.flags.backend == "" {
		sc.Preferred = cfg.Backend.Preferred
	}
	sel, err := backend.Select(ctx, backend.Default, sc)
	if err != nil {
		return sel, exitWith(ExitBackend, err)
	}
	return sel, nil
}

func (a *App) configureBackends(cfg *config.Resolved) {
	home := config.Home()
	_ = os.MkdirAll(home, 0o755)
	if b, ok := backend.Default.Get(backend.ClaudeSubscriptionName); ok {
		if c, ok := b.(*backend.ClaudeSubscription); ok {
			c.WorkDir = home
			c.ScratchDir = filepath.Join(home, "tmp")
		}
	}
	if b, ok := backend.Default.Get(backend.CodexSubscriptionName); ok {
		if c, ok := b.(*backend.CodexSubscription); ok {
			c.WorkDir = home
		}
	}
	if b, ok := backend.Default.Get(backend.APIName); ok {
		if c, ok := b.(*backend.API); ok {
			c.Key, c.Model = cfg.API.Key, cfg.API.Model
		}
	}
}

func (a *App) jsonMode() bool {
	return a.flags.jsonOut || strings.EqualFold(a.flags.output, "json")
}

// meteredLeakWarning returns a warning when a metered key is exported while a
// subscription backend is selected — the credential trap.
func meteredLeakWarning(sel backend.Selection) string {
	leaked := backend.LeakedKeys()
	if len(leaked) == 0 || sel.Backend == nil || sel.Availability.Metered {
		return ""
	}
	return "exported while " + sel.Backend.Name() + " is selected: " + strings.Join(leaked, ", ") +
		". water strips these from subprocess environments, but other tools may bill the key. Unset it to be safe."
}

func isTTY(f *os.File) bool { return term.IsTerminal(f.Fd()) }
