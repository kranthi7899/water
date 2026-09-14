package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"water/internal/backend"
	"water/internal/config"
	"water/internal/memory"
	"water/internal/orchestrator"
	"water/internal/surface"
	"water/internal/theme"
	"water/internal/voice"
)

type check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | fail
	Detail string `json:"detail"`
}

func (a *App) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Deep diagnostics: backends, auth, metered-leak warnings, roles",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := context.Background()
			var checks []check
			add := func(name, status, detail string) { checks = append(checks, check{name, status, detail}) }

			cfg, err := a.config()
			if err != nil {
				add("config", "fail", err.Error())
				return a.printChecks(checks)
			}
			if cfg.FileExists {
				add("config", "ok", cfg.FilePath)
			} else {
				add("config", "warn", "no config file; using defaults — run `water onboard`")
			}
			a.configureBackends(cfg)

			// Backends.
			for _, b := range backend.Default.All() {
				av := b.Available(ctx)
				st := "warn"
				switch {
				case av.Usable() && !av.Metered:
					st = "ok"
				case av.Usable() && av.Metered:
					st = "warn"
				case !av.Installed && b.Name() == backend.APIName:
					st = "ok"
				}
				add("backend "+b.Name(), st, av.Detail)
			}
			sel, selErr := backend.Select(ctx, backend.Default, backend.SelectConfig{Preferred: cfg.Backend.Preferred, AllowMetered: cfg.Backend.AllowMetered})
			switch {
			case selErr != nil:
				add("selection", "fail", selErr.Error())
			case sel.Availability.Metered:
				add("selection", "warn", fmt.Sprintf("%s (%s) — METERED: every call bills per token", sel.Backend.Name(), sel.Reason))
			default:
				add("selection", "ok", fmt.Sprintf("%s (%s)", sel.Backend.Name(), sel.Reason))
			}

			// The credential trap.
			if leaked := backend.LeakedKeys(); len(leaked) > 0 {
				if selErr == nil && !sel.Availability.Metered {
					add("credential leak", "warn", meteredLeakWarning(sel))
				} else {
					add("credential leak", "warn", fmt.Sprintf("%v exported; other tools may bill it", leaked))
				}
			} else {
				add("credential leak", "ok", "no metered API keys exported in this environment")
			}

			// Roles.
			reg, err := a.roleRegistry()
			if err != nil {
				add("roles", "fail", err.Error())
			} else {
				orch := reg.Orchestrator()
				blank := 0
				for _, r := range reg.All() {
					if r.Blank() {
						blank++
					}
				}
				add("roles", "ok", fmt.Sprintf("%d discovered from %s; orchestrator %s; %d persona(s) unwritten", len(reg.All()), reg.Source(), orch.Slug, blank))
				if _, ok := orchestrator.NewRouter(cfg.Orchestration.Router, orch.Slug, nil); !ok {
					add("router", "fail", fmt.Sprintf("%q is not a registered router (%v)", cfg.Orchestration.Router, orchestrator.RouterNames()))
				} else {
					add("router", "ok", cfg.Orchestration.Router)
				}
				// Memory bounds per role.
				if mp, err := a.memoryProvider(); err == nil {
					if sz, ok := mp.(memory.Sizer); ok {
						for _, r := range reg.All() {
							n, b, err := sz.Size(r.Slug)
							switch {
							case err != nil:
								add("memory "+r.Slug, "fail", err.Error())
							case n > cfg.Memory.MaxEntries || b > cfg.Memory.MaxBytes:
								add("memory "+r.Slug, "fail", fmt.Sprintf("%d entries / %d bytes exceeds bounds — `water memory %s prune`", n, b, r.Slug))
							default:
								add("memory "+r.Slug, "ok", fmt.Sprintf("%d entries / %d bytes (max %d / %d)", n, b, cfg.Memory.MaxEntries, cfg.Memory.MaxBytes))
							}
						}
					}
				}
			}

			// Trace dir writable.
			if err := os.MkdirAll(cfg.Telemetry.TraceDir, 0o755); err != nil {
				add("traces", "fail", err.Error())
			} else if f, err := os.CreateTemp(cfg.Telemetry.TraceDir, ".probe-*"); err != nil {
				add("traces", "fail", err.Error())
			} else {
				f.Close()
				os.Remove(f.Name())
				add("traces", "ok", cfg.Telemetry.TraceDir+" (local only; nothing leaves this machine)")
			}

			// Checkpoints dir.
			if cfg.Orchestration.Checkpointer == "file" {
				if err := os.MkdirAll(cfg.Orchestration.CheckpointDir, 0o755); err != nil {
					add("checkpoints", "fail", err.Error())
				} else {
					add("checkpoints", "ok", cfg.Orchestration.CheckpointDir)
				}
			} else {
				add("checkpoints", "warn", "checkpointer is noop; runs cannot be resumed")
			}

			// Identity keyring + local agents dir.
			switch {
			case a.keys() != nil:
				add("keyring", "ok", filepath.Join(config.Home(), "keyring")+" (persona signatures enforced for --agents-dir)")
			case a.localAgentsDir() != "":
				add("keyring", "warn", "no keyring yet; persona files are hash-checked but not signed (created on first `water persona edit`)")
			default:
				add("keyring", "ok", "not needed (personas are embedded)")
			}

			// Tools.
			switch {
			case !cfg.Tools.Enabled:
				add("tools", "ok", "disabled (tools.enabled=false); every role invokes nothing")
			case len(cfg.RootList()) == 0:
				add("tools", "warn", "tools.enabled but tools.roots is empty; roles with a tools block can read nothing")
			default:
				add("tools", "ok", fmt.Sprintf("read roots: %s (roles: cto, design read-only; no shell)", strings.Join(cfg.RootList(), ", ")))
			}

			// Themes.
			if names := theme.Names(a.themes); len(names) == 0 {
				add("themes", "fail", "no themes embedded")
			} else {
				add("themes", "ok", fmt.Sprintf("%s · terminal colour profile %s", strings.Join(names, ", "), theme.EnvProfile()))
			}

			// Sessions.
			add("sessions", "ok", fmt.Sprintf("%s (keep %d, max age %s, pinned exempt)", filepath.Join(config.Home(), "sessions"), cfg.Sessions.Keep, cfg.Sessions.MaxAge))

			// Voice.
			if vp, ok := voice.Open(cfg.Voice.Provider); !ok {
				add("voice", "fail", fmt.Sprintf("provider %q not registered", cfg.Voice.Provider))
			} else if !vp.Available() {
				add("voice", "warn", voice.Absence(vp))
			} else {
				add("voice", "ok", vp.Name()+" (speak only; listen is a documented no-op)")
			}
			if cfg.Onboard.VerifiedAt != "" {
				add("onboard", "ok", "verified round trip at "+cfg.Onboard.VerifiedAt)
			} else {
				add("onboard", "warn", "no verified round trip recorded; run `water onboard`")
			}
			return a.printChecks(checks)
		},
	}
}

func (a *App) printChecks(checks []check) error {
	failed := false
	for _, c := range checks {
		if c.Status == "fail" {
			failed = true
		}
	}
	if a.jsonMode() {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"checks": checks, "ok": !failed})
	} else {
		for _, c := range checks {
			mark := surface.StyleOK.Render("✓")
			switch c.Status {
			case "warn":
				mark = surface.StyleWarn.Render("!")
			case "fail":
				mark = surface.StyleErr.Render("×")
			}
			fmt.Printf("  %s %-24s %s\n", mark, c.Name, surface.StyleDim.Render(c.Detail))
		}
	}
	if failed {
		return exitWith(ExitError, errors.New("doctor found failures"))
	}
	return nil
}
