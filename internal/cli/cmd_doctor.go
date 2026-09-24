package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"water"
	"water/internal/backend"
	"water/internal/config"
	"water/internal/connectors/google/gapi"
	"water/internal/gateway"
	"water/internal/vault"
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
		Short: "Deep diagnostics: backends, auth, metered-leak warnings, the twin and daemon",
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

			// The twin's manifest and connectors.
			checks = append(checks, doctorTwinCheck(a.twinID(), cfg.Agent.MailAddress))

			// Google (Calendar/Gmail/Drive): presence only, no network call.
			if _, err := vault.Default().Get(gapi.Service, gapi.DefaultAccount); err != nil {
				add("google", "warn", "not connected; run `water connect google` (see docs/google-setup.md)")
			} else {
				add("google", "ok", fmt.Sprintf(
					"connected (%s/%s) — this app's OAuth consent screen stays in Testing mode (avoids Google's verification review), so this login expires every 7 days; run `water connect google --client-file ...` again if `water connect google --status` reports invalid_grant",
					gapi.Service, gapi.DefaultAccount))
			}

			// The daemon.
			paths := gateway.Paths{Home: config.Home()}
			if _, err := os.Stat(paths.SocketPath()); err == nil {
				add("daemon", "ok", "socket present at "+paths.SocketPath())
			} else {
				add("daemon", "warn", "not running; start it with `water daemon`")
			}

			// Voice.
			if vp, verr := a.voiceProvider(cfg); verr != nil {
				add("voice", "fail", verr.Error())
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

// doctorTwinCheck validates id's manifest and connectors read-only (see
// loadTwinManifest), so it works while the daemon is running.
func doctorTwinCheck(id, mailAddress string) check {
	m, err := loadTwinManifest(water.TwinsFS(), id, mailAddress)
	if err != nil {
		return check{"twin", "fail", err.Error()}
	}
	return check{"twin", "ok", fmt.Sprintf("%s: %d function(s) across %d connector(s)", m.ID, len(m.FunctionIDs()), len(m.Connectors))}
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
			mark := styleOK.Render("✓")
			switch c.Status {
			case "warn":
				mark = styleWarn.Render("!")
			case "fail":
				mark = styleErr.Render("×")
			}
			fmt.Printf("  %s %-24s %s\n", mark, c.Name, styleDim.Render(c.Detail))
		}
	}
	if failed {
		return exitWith(ExitError, errors.New("doctor found failures"))
	}
	return nil
}
