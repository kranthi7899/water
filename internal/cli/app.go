// Package cli is the cobra command tree. It wires the packages together and
// owns nothing else.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"

	"water/internal/agent"
	"water/internal/backend"
	"water/internal/config"
	"water/internal/identity"
	"water/internal/memory"
	"water/internal/persona"
	"water/internal/roles"
	"water/internal/surface"
	"water/internal/tools"
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
	themes   fs.FS

	cfg     *config.Resolved
	src     persona.Source
	mem     memory.Provider
	roles   *roles.Registry
	keyring *identity.Keyring
	keyErr  error
}

// NewApp builds an App around the embedded agents tree (rooted at agents/)
// and the embedded themes tree (rooted at the repo root).
func NewApp(embedded, themes fs.FS) *App { return &App{embedded: embedded, themes: themes} }

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

// localAgentsDir returns the real local agents directory if one is in play:
// --agents-dir, or ./agents when running from a source checkout. This is the
// gate for `persona show/edit` (invariant #5: personas stay hidden from end
// users unless a real local source directory exists).
func (a *App) localAgentsDir() string {
	if a.flags.agentsDir != "" {
		return a.flags.agentsDir
	}
	if info, err := os.Stat(filepath.Join("agents", "ceo", "role.yaml")); err == nil && !info.IsDir() {
		if abs, err := filepath.Abs("agents"); err == nil {
			return abs
		}
	}
	return ""
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

// keys loads the machine keyring once (nil when absent).
func (a *App) keys() *identity.Keyring {
	if a.keyring != nil || a.keyErr != nil {
		return a.keyring
	}
	a.keyring, a.keyErr = identity.LoadKeyring(config.Home())
	return a.keyring
}

// roleRegistry loads roles, failing loudly on any invariant violation.
// Signatures are required only when roles come from a real local agents dir
// (--agents-dir) and a keyring exists; the embedded tree is trusted by virtue
// of being in the binary and is checked for role_id/content_hash only.
func (a *App) roleRegistry() (*roles.Registry, error) {
	if a.roles != nil {
		return a.roles, nil
	}
	mem, err := a.memoryProvider()
	if err != nil {
		return nil, err
	}
	opts := roles.LoadOptions{}
	if k := a.keys(); k != nil {
		opts.Key = k.Key
		opts.RequireSignatures = a.flags.agentsDir != ""
	}
	reg, err := roles.LoadWith(a.source(), mem, opts)
	if err != nil {
		return nil, err
	}
	a.roles = reg
	return reg, nil
}

// selectBackend resolves the GLOBAL backend via the single selection function.
func (a *App) selectBackend(ctx context.Context) (backend.Selection, error) {
	return a.selectBackendFor(ctx, nil)
}

// selectBackendFor resolves the backend for one role (Part 8): --backend
// flag → role.yaml backend: → config → auto, all inside backend.Select.
func (a *App) selectBackendFor(ctx context.Context, role *roles.Role) (backend.Selection, error) {
	cfg, err := a.config()
	if err != nil {
		return backend.Selection{}, err
	}
	a.configureBackends(cfg)
	sc := backend.SelectConfig{Flag: a.flags.backend, AllowMetered: cfg.Backend.AllowMetered}
	if a.flags.backend == "" {
		sc.Preferred = cfg.Backend.Preferred
	}
	if role != nil {
		sc.Role, sc.RoleSlug = role.Backend, role.Slug
	}
	sel, err := backend.Select(ctx, backend.Default, sc)
	if err != nil {
		return sel, exitWith(ExitBackend, err)
	}
	return sel, nil
}

// roleEnv builds the shared agent.Env for a run: per-role backends resolved
// through Select, per-role models, tool policies, capability manifests, and
// the configured skill selector. It returns the resolutions for status output.
type roleResolution struct {
	Backend string
	Reason  string
	Model   string
	Tools   []string
}

func (a *App) roleEnv(ctx context.Context, reg *roles.Registry, def backend.Selection) (agent.Env, map[string]roleResolution, error) {
	cfg, err := a.config()
	if err != nil {
		return agent.Env{}, nil, err
	}
	env := agent.Env{Backend: def.Backend, Timeout: cfg.CallTimeoutDuration(), RoleBackends: map[string]backend.Backend{}, RoleModels: map[string]string{}, RoleTools: map[string]*tools.Policy{}, Manifests: map[string]string{}}
	if sel, ok := persona.Selectors()[cfg.Skills.Selector]; ok {
		env.Selector = sel
	}
	res := map[string]roleResolution{}
	for _, r := range reg.All() {
		rr := roleResolution{Backend: def.Backend.Name(), Reason: def.Reason, Model: r.Model}
		if r.Backend != "" {
			sel, err := a.selectBackendFor(ctx, r)
			if err != nil {
				return agent.Env{}, nil, fmt.Errorf("role %s: %w", r.Slug, err)
			}
			env.RoleBackends[r.Slug] = sel.Backend
			rr.Backend, rr.Reason = sel.Backend.Name(), sel.Reason
		}
		if r.Model != "" {
			env.RoleModels[r.Slug] = r.Model
		}
		if cfg.Tools.Enabled && r.Tools != nil {
			pol := tools.FromGrant(r.Slug, r.RoleID, r.Tools, cfg.RootList())
			if !pol.Empty() {
				env.RoleTools[r.Slug] = pol
				rr.Tools = pol.ToolNames()
			}
		}
		env.Manifests[r.Slug] = r.Capability.Render(r.Slug, r.Name)
		res[r.Slug] = rr
	}
	return env, res, nil
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
