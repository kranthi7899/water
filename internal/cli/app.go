// Package cli is the cobra command tree. It wires the packages together and
// owns nothing else.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"

	"water/internal/backend"
	"water/internal/config"
	"water/internal/memory"
	"water/internal/persona"
	"water/internal/roles"
	"water/internal/surface"
)

// Exit codes that mean something.
const (
	ExitOK           = 0
	ExitError        = 1
	ExitUsage        = 2 // bad args / prerequisites missing
	ExitBackend      = 3 // no usable backend or metered refused
	ExitUnconfigured = 4 // run `water onboard`
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
	agentsDir    string
	output       string
	jsonOut      bool
	backend      string
	traceDir     string
	yes          bool
	quiet        bool
	verbose      bool
	voice        bool
	allowMetered bool
}

// App holds lazily-built shared state for one invocation.
type App struct {
	flags    globalFlags
	embedded fs.FS

	cfg   *config.Resolved
	src   persona.Source
	mem   memory.Provider
	roles *roles.Registry
}

// NewApp builds an App around the embedded agents tree (rooted at agents/).
func NewApp(embedded fs.FS) *App { return &App{embedded: embedded} }

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

func (a *App) source() persona.Source {
	if a.src != nil {
		return a.src
	}
	emb := persona.NewEmbedded(a.embedded)
	if a.flags.agentsDir != "" {
		a.src = persona.NewOverlay(persona.NewDir(a.flags.agentsDir), emb)
	} else {
		a.src = emb
	}
	return a.src
}

func (a *App) memoryProvider() (memory.Provider, error) {
	if a.mem != nil {
		return a.mem, nil
	}
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	m, err := memory.Open(cfg.Memory.Provider, memory.Options{
		Root:   config.MemoryDir(),
		Seed:   a.source().FS(),
		Limits: memory.Limits{MaxEntries: cfg.Memory.MaxEntries, MaxBytes: cfg.Memory.MaxBytes},
	})
	if err != nil {
		return nil, err
	}
	a.mem = m
	return m, nil
}

// roleRegistry loads roles, failing loudly on any invariant violation.
func (a *App) roleRegistry() (*roles.Registry, error) {
	if a.roles != nil {
		return a.roles, nil
	}
	mem, err := a.memoryProvider()
	if err != nil {
		return nil, err
	}
	reg, err := roles.Load(a.source(), mem)
	if err != nil {
		return nil, err
	}
	a.roles = reg
	return reg, nil
}

// selectBackend resolves the backend via the single selection function and
// applies per-backend settings from config.
func (a *App) selectBackend(ctx context.Context) (backend.Selection, error) {
	cfg, err := a.config()
	if err != nil {
		return backend.Selection{}, err
	}
	a.configureBackends(cfg)
	sel, err := backend.Select(ctx, backend.Default, backend.SelectConfig{
		Preferred: cfg.Backend.Preferred, AllowMetered: cfg.Backend.AllowMetered,
	})
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

// surfaces returns the output surface implied by flags.
func (a *App) surfaces() surface.Surface {
	name := "terminal"
	if a.flags.jsonOut || strings.EqualFold(a.flags.output, "json") {
		name = "json"
	}
	s, ok := surface.Open(name, surface.Options{Quiet: a.flags.quiet, Verbose: a.flags.verbose})
	if !ok {
		s, _ = surface.Open("terminal", surface.Options{})
	}
	return s
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
	return fmt.Sprintf("%s exported while %s is selected. water strips these from subprocess environments, but other tools may bill the key. Unset it to be safe.",
		strings.Join(leaked, ", "), sel.Backend.Name())
}

func isTTY(f *os.File) bool { return term.IsTerminal(f.Fd()) }

func readStdin() (string, error) {
	if isTTY(os.Stdin) {
		return "", errors.New("no prompt given and stdin is a terminal")
	}
	b, err := readAllStdin()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func readAllStdin() ([]byte, error) {
	var buf []byte
	tmp := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return buf, nil
}

func runTimeout(cfg *config.Resolved) time.Duration { return cfg.TimeoutDuration() }
