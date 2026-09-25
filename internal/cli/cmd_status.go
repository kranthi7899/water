package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"water"
	"water/internal/backend"
	"water/internal/config"
)

// routerStatusTimeout bounds how long `water status`'s router line waits
// for the daemon before falling back to "(daemon not running)" — a var so
// tests can shrink it rather than actually waiting 300ms.
var routerStatusTimeout = 300 * time.Millisecond

// routerStatusResult carries fetch's outcome across the goroutine boundary
// in routerStatusLine: an error (including "no daemon") is reported
// immediately rather than waiting out the rest of routerStatusTimeout.
type routerStatusResult struct {
	health RouterHealth
	err    error
}

// routerStatusLine runs fetch (typically newDaemonClient + RouterHealth) in
// its own goroutine and gives it at most routerStatusTimeout, best-effort:
// a daemon that is not running, not yet listening, or simply slow all
// collapse into the same fallback rather than making `water status` hang or
// fail outright over one line. A fetch that fails fast (the common
// no-daemon case) reports the fallback immediately, without waiting out the
// rest of the budget. fetch is injected so this is testable without a real
// daemon socket.
func routerStatusLine(fetch func() (RouterHealth, error)) string {
	const fallback = "(daemon not running)"
	ch := make(chan routerStatusResult, 1)
	go func() {
		h, err := fetch()
		ch <- routerStatusResult{health: h, err: err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return fallback
		}
		return renderRouterStatusLine(r.health)
	case <-time.After(routerStatusTimeout):
		return fallback
	}
}

// renderRouterStatusLine formats one RouterHealth as `water status`'s
// single-line summary.
func renderRouterStatusLine(h RouterHealth) string {
	promotion := "off"
	if h.PromotionEnabled {
		promotion = "on"
	}
	return fmt.Sprintf("tier0=%s inactive=%d learned_skipped=%d promotion=%s",
		h.Tier0.State, len(h.InactiveIntents), len(h.LearnedSkipped), promotion)
}

func (a *App) statusCmd() *cobra.Command {
	var noProbe bool
	c := &cobra.Command{
		Use:   "status",
		Short: "The CEO twin's manifest, backend resolution (and why), and subscription budget",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			// Read-only: never the audit log's writer lock, which the
			// running daemon holds (see loadTwinManifest).
			m, err := loadTwinManifest(water.TwinsFS(), a.twinID(), cfg.Agent.MailAddress, cfg.Agent.SignatureName, cfg.GitHub.Repo)
			if err != nil {
				return err
			}

			selected, reason := cfg.Backend.Preferred, "configured (not probed)"
			if !noProbe {
				if sel, err := a.selectBackend(context.Background()); err == nil {
					selected, reason = sel.Backend.Name(), sel.Reason
				} else {
					selected, reason = "(none)", err.Error()
				}
			}

			router := routerStatusLine(func() (RouterHealth, error) {
				dc, err := newDaemonClient()
				if err != nil {
					return RouterHealth{}, err
				}
				ctx, cancel := context.WithTimeout(context.Background(), routerStatusTimeout)
				defer cancel()
				return dc.RouterHealth(ctx)
			})

			if a.jsonMode() {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{
					"twin": m.ID, "functions": m.FunctionIDs(),
					"backend": selected, "backend_reason": reason,
					"config": cfg.FilePath, "config_exists": cfg.FileExists,
					"leaked_keys": backend.LeakedKeys(), "budget": backend.LoadRateLimit(config.Home()),
					"router": router,
				})
			}
			fmt.Printf("%s %s (%s)\n", styleDim.Render("twin    "), styleBold.Render(m.ID), m.Name)
			fmt.Printf("%s %s\n", styleDim.Render("functions"), strings.Join(m.FunctionIDs(), ", "))
			fmt.Printf("%s %s %s\n", styleDim.Render("backend "), styleAccent.Render(selected), styleDim.Render(reason))
			fmt.Printf("%s %s\n", styleDim.Render("budget  "), backend.LoadRateLimit(config.Home()).Summary())
			fmt.Printf("%s %s\n", styleDim.Render("router  "), router)
			fmt.Printf("%s %s\n", styleDim.Render("config  "), cfg.FilePath)
			if lk := backend.LeakedKeys(); len(lk) > 0 {
				fmt.Printf("%s %v exported — see `water doctor`\n", styleWarn.Render("warning "), lk)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&noProbe, "no-probe", false, "do not probe backend availability")
	return c
}
