package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"water/internal/orchestrator"
)

func (a *App) orchestrateCmd() *cobra.Command {
	var resume string
	var list, solo bool
	c := &cobra.Command{
		Use:   "orchestrate \"<brief>\"",
		Short: "Full graph run: CEO frames → COO assigns → specialists → COO verifies → CEO decides",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			reg, err := a.roleRegistry()
			if err != nil {
				return err
			}
			cp := a.checkpointer(reg)
			if list {
				return a.listCheckpoints(cp)
			}
			ctx := context.Background()
			var st *orchestrator.State
			brief := ""
			if resume != "" {
				fc, ok := cp.(*orchestrator.FileCheckpointer)
				if !ok {
					return exitWith(ExitUsage, fmt.Errorf("--resume needs orchestration.checkpointer=file (is %q)", cfg.Orchestration.Checkpointer))
				}
				st, err = fc.Load(ctx, resume)
				if err != nil {
					return exitWith(ExitUsage, err)
				}
				brief = st.Brief
				if f, done := st.FinalOutput(); done {
					fmt.Fprintln(os.Stderr, "run", resume, "already completed; final output follows")
					fmt.Println(f)
					return nil
				}
			} else {
				if len(args) == 1 {
					brief = args[0]
				} else if brief, err = readStdin(); err != nil {
					return exitWith(ExitUsage, err)
				}
				if strings.TrimSpace(brief) == "" {
					return exitWith(ExitUsage, errors.New("empty brief"))
				}
				st = orchestrator.NewState("", brief, reg.OrchestratorSlugs())
			}
			return a.runOrchestration(ctx, cfg, reg, st, brief, cp, orchOpts{
				Solo:       solo,
				ResumeHint: "water orchestrate --resume",
			})
		},
	}
	c.Flags().StringVar(&resume, "resume", "", "resume a checkpointed run by id (the id printed when the run started)")
	c.Flags().BoolVar(&list, "list", false, "list resumable runs")
	c.Flags().BoolVar(&solo, "solo", false, "CEO answers alone, delegating nothing (single-agent comparison)")
	return c
}

func (a *App) checkpointer(reg interface{ OrchestratorSlugs() []string }) orchestrator.Checkpointer {
	cfg, _ := a.config()
	if cfg != nil && cfg.Orchestration.Checkpointer == orchestrator.FileCheckpointerName {
		return &orchestrator.FileCheckpointer{Dir: cfg.Orchestration.CheckpointDir, Orchestrators: reg.OrchestratorSlugs()}
	}
	return orchestrator.NoopCheckpointer{}
}

func (a *App) listCheckpoints(cp orchestrator.Checkpointer) error {
	fc, ok := cp.(*orchestrator.FileCheckpointer)
	if !ok {
		return exitWith(ExitUsage, errors.New("checkpointer is noop; nothing to list"))
	}
	infos, err := fc.List()
	if err != nil {
		return err
	}
	if a.jsonMode() {
		return printJSON(infos)
	}
	for _, in := range infos {
		state := "resumable"
		if in.Complete {
			state = "complete"
		}
		fmt.Printf("%s  step %-3d %-10s %s\n", in.RunID, in.StepCount, state, truncateLine(in.Brief, 70))
	}
	if len(infos) == 0 {
		fmt.Fprintln(os.Stderr, "(no checkpoints)")
	}
	return nil
}

func truncateLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
