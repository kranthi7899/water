package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"water/internal/diagnose"
	"water/internal/orchestrator"
	"water/internal/surface"
)

func (a *App) diagnoseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diagnose <run-id>",
		Short: "Validate the orchestration topology empirically from a run's trace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			id := filepath.Base(args[0])
			events, err := diagnose.ReadTrace(filepath.Join(cfg.Telemetry.TraceDir, id+".jsonl"))
			if err != nil {
				return exitWith(ExitUsage, fmt.Errorf("trace for %s: %w", id, err))
			}
			snap, _ := diagnose.ReadSnapshot(filepath.Join(cfg.Orchestration.CheckpointDir, id+".json"))
			var edges *orchestrator.PermissionGraph
			if reg, err := a.roleRegistry(); err == nil {
				if orch := reg.Orchestrator(); orch != nil {
					var specialists []string
					coo := ""
					for _, r := range reg.Delegates() {
						if r.Slug == "coo" {
							coo = r.Slug
							continue
						}
						specialists = append(specialists, r.Slug)
					}
					if coo != "" {
						edges = orchestrator.Hierarchy(orch.Slug, coo, specialists)
					}
				}
			}
			rep := diagnose.Analyze(id, events, snap, edges)
			if a.jsonMode() {
				return printJSON(rep)
			}
			fmt.Printf("%s %s\n", surface.StyleDim.Render("run"), surface.StyleAccent.Render(id))
			for _, f := range rep.Findings {
				mark := surface.StyleOK.Render("✓")
				switch f.Severity {
				case "warn":
					mark = surface.StyleWarn.Render("!")
				case "fail":
					mark = surface.StyleErr.Render("×")
				case "info":
					mark = surface.StyleDim.Render("·")
				}
				fmt.Printf("  %s %-30s %s\n", mark, f.Check, surface.StyleDim.Render(f.Detail))
			}
			fmt.Printf("%s dissent survival %.0f%% · verified %d · unconfirmed %d · messages %d · tool calls %d (%d denied)\n",
				surface.StyleDim.Render("totals"), rep.DissentRate*100, rep.Verified, rep.Unconfirmed, rep.Messages, rep.ToolCalls, rep.ToolDenials)
			return nil
		},
	}
}
