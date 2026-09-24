package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"water/internal/backend"
	"water/internal/config"
)

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
			deps, err := buildTwinDeps()
			if err != nil {
				return err
			}
			defer deps.Close()

			selected, reason := cfg.Backend.Preferred, "configured (not probed)"
			if !noProbe {
				if sel, err := a.selectBackend(context.Background()); err == nil {
					selected, reason = sel.Backend.Name(), sel.Reason
				} else {
					selected, reason = "(none)", err.Error()
				}
			}

			if a.jsonMode() {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{
					"twin": deps.manifest.ID, "functions": deps.manifest.FunctionIDs(),
					"backend": selected, "backend_reason": reason,
					"config": cfg.FilePath, "config_exists": cfg.FileExists,
					"leaked_keys": backend.LeakedKeys(), "budget": backend.LoadRateLimit(config.Home()),
				})
			}
			fmt.Printf("%s %s (%s)\n", styleDim.Render("twin    "), styleBold.Render(deps.manifest.ID), deps.manifest.Name)
			fmt.Printf("%s %s\n", styleDim.Render("functions"), strings.Join(deps.manifest.FunctionIDs(), ", "))
			fmt.Printf("%s %s %s\n", styleDim.Render("backend "), styleAccent.Render(selected), styleDim.Render(reason))
			fmt.Printf("%s %s\n", styleDim.Render("budget  "), backend.LoadRateLimit(config.Home()).Summary())
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
