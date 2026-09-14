package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"water/internal/backend"
	"water/internal/config"
	"water/internal/surface"
)

func (a *App) onboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "onboard",
		Short: "Detect claude/codex, check auth, write initial config",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return a.runOnboard(cmd) },
	}
}

type onboardReport struct {
	Backends map[string]backend.Availability `json:"backends"`
	Selected string                          `json:"selected,omitempty"`
	Metered  bool                            `json:"metered"`
	Warnings []string                        `json:"warnings,omitempty"`
	Config   string                          `json:"config"`
	Written  bool                            `json:"written"`
}

func (a *App) runOnboard(cmd *cobra.Command) error {
	ctx := context.Background()
	cfg, err := a.config()
	if err != nil {
		return err
	}
	a.configureBackends(cfg)
	rep := onboardReport{Backends: map[string]backend.Availability{}, Config: config.Path()}
	for _, b := range backend.Default.All() {
		rep.Backends[b.Name()] = b.Available(ctx)
	}
	sel, selErr := backend.Select(ctx, backend.Default, backend.SelectConfig{Preferred: "auto", AllowMetered: false})
	if selErr == nil {
		rep.Selected = sel.Backend.Name()
		rep.Metered = sel.Availability.Metered
		if w := meteredLeakWarning(sel); w != "" {
			rep.Warnings = append(rep.Warnings, w)
		}
	}

	if !a.jsonMode() {
		fmt.Fprintln(os.Stderr, surface.StyleDim.Render(surface.Banner))
		for _, name := range backend.Default.Names() {
			av := rep.Backends[name]
			mark := surface.StyleDim.Render("○")
			if av.Usable() && !av.Metered {
				mark = surface.StyleAccent.Render("●")
			} else if av.Usable() {
				mark = surface.StyleWarn.Render("$")
			}
			fmt.Fprintf(os.Stderr, "  %s %-20s %s\n", mark, name, surface.StyleDim.Render(av.Detail))
		}
	}

	if selErr != nil {
		msg := "no subscription CLI is installed and logged in.\n" +
			"  install one and sign in with your plan:\n" +
			"    claude  → https://claude.com/claude-code   then run `claude` and log in\n" +
			"    codex   → npm i -g @openai/codex            then run `codex login`\n" +
			"  then run `water onboard` again."
		if a.jsonMode() {
			_ = json.NewEncoder(os.Stdout).Encode(rep)
		}
		return exitWith(ExitUsage, errors.New(msg))
	}

	set := map[string]string{"backend.preferred": rep.Selected, "backend.allow_metered": "false"}
	if err := config.Save(set); err != nil {
		return err
	}
	for _, d := range []string{config.MemoryDir(), cfg.Telemetry.TraceDir} {
		_ = os.MkdirAll(d, 0o755)
	}
	rep.Written = true

	if a.jsonMode() {
		return json.NewEncoder(os.Stdout).Encode(rep)
	}
	fmt.Fprintf(os.Stderr, "\n  %s %s\n", surface.StyleDim.Render("selected"), surface.StyleAccent.Render(rep.Selected))
	fmt.Fprintf(os.Stderr, "  %s %s\n", surface.StyleDim.Render("config  "), rep.Config)
	for _, w := range rep.Warnings {
		fmt.Fprintf(os.Stderr, "\n  %s %s\n", surface.StyleWarn.Render("warning"), w)
	}
	fmt.Fprintf(os.Stderr, "\n  %s\n", surface.StyleDim.Render("next: water status · water run ceo \"hello\" · water orchestrate \"<brief>\""))
	return nil
}
