package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"water/internal/config"
)

func (a *App) configCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "config [get <key> | set <key> <value>]",
		Short: "Resolved config with the layer each value came from",
		Args:  cobra.MaximumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) >= 1 && args[0] == "set" {
				if len(args) != 3 {
					return exitWith(ExitUsage, errors.New("usage: water config set <key> <value>"))
				}
				if err := config.Save(map[string]string{args[1]: args[2]}); err != nil {
					return exitWith(ExitUsage, err)
				}
				a.cfg = nil
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			flat := cfg.Flat()
			if len(args) >= 1 && args[0] == "get" {
				if len(args) != 2 {
					return exitWith(ExitUsage, errors.New("usage: water config get <key>"))
				}
				v, ok := flat[args[1]]
				if !ok {
					return exitWith(ExitUsage, fmt.Errorf("unknown key %q", args[1]))
				}
				fmt.Println(v)
				return nil
			}
			if a.jsonMode() {
				rows := map[string]map[string]string{}
				for _, k := range config.Keys() {
					rows[k] = map[string]string{"value": flat[k], "layer": cfg.Provenance[k]}
				}
				return json.NewEncoder(os.Stdout).Encode(map[string]any{"file": cfg.FilePath, "file_exists": cfg.FileExists, "values": rows})
			}
			fmt.Printf("%s %s\n", styleDim.Render("file"), cfg.FilePath)
			for _, k := range config.Keys() {
				layer := cfg.Provenance[k]
				ls := styleDim.Render(layer)
				if layer != config.LayerDefault {
					ls = styleAccent.Render(layer)
				}
				fmt.Printf("  %-28s %-22s %s\n", k, flat[k], ls)
			}
			return nil
		},
	}
	return c
}
