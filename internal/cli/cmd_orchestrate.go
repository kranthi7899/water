package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"water/internal/agent"
	"water/internal/orchestrator"
	"water/internal/trace"
)

func (a *App) orchestrateCmd() *cobra.Command {
	var resume string
	var list bool
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
			def, err := a.selectBackend(ctx)
			if err != nil {
				return err
			}
			if w := meteredLeakWarning(def); w != "" && !a.jsonMode() {
				fmt.Fprintln(os.Stderr, "warning:", w)
			}
			env, _, err := a.roleEnv(ctx, reg, def)
			if err != nil {
				return exitWith(ExitBackend, err)
			}

			orch := reg.Orchestrator()
			var delegates []string
			for _, r := range reg.Delegates() {
				delegates = append(delegates, r.Slug)
			}
			router, ok := orchestrator.NewRouter(cfg.Orchestration.Router, orch.Slug, delegates)
			if !ok {
				return exitWith(ExitUsage, fmt.Errorf("unknown router %q (known: %v)", cfg.Orchestration.Router, orchestrator.RouterNames()))
			}
			if h, ok := router.(*orchestrator.HierarchyRouter); ok {
				h.MaxRounds = cfg.Orchestration.MaxRounds
			}
			st.SetEdges(orchestrator.EdgesFor(router))

			rec, err := trace.New(cfg.Telemetry.TraceDir, st.RunID)
			if err != nil {
				return err
			}
			sf := a.surfaces()
			env.Surface, env.Trace = sf, rec
			g := &orchestrator.Graph{Nodes: map[string]orchestrator.Node{}, Router: router}
			for _, r := range reg.All() {
				if h, ok := router.(*orchestrator.HierarchyRouter); ok {
					g.Nodes[r.Slug] = agent.HierarchyNode(r, env, h)
				} else {
					g.Nodes[r.Slug] = agent.Node(r, env, delegates)
				}
			}
			ex := &orchestrator.Executor{
				MaxParallel:  cfg.Orchestration.MaxParallel,
				Timeout:      runTimeout(cfg),
				MaxSteps:     cfg.Orchestration.MaxSteps,
				Checkpointer: cp,
				Hooks: orchestrator.Hooks{
					NodeStarted:  func(role string) { sf.NodeStarted(role); rec.NodeStarted(role) },
					NodeFinished: func(role string, d time.Duration, err error) { rec.NodeFinished(role, d, err) },
					Checkpointed: func(step int) { rec.Checkpoint(step) },
				},
			}
			sf.RunStarted(st.RunID, brief)
			rec.RunStarted(brief)
			st.Observe(func(m orchestrator.AgentMessage) { rec.Message(m); sf.MessageSent(m) })
			runErr := ex.Run(ctx, g, st)
			final, _ := st.FinalOutput()
			if runErr != nil {
				rec.Error(runErr)
			}
			stats := rec.Finish()
			sf.RunFinished(final, stats)
			if runErr != nil {
				if cfg.Orchestration.Checkpointer == orchestrator.FileCheckpointerName {
					fmt.Fprintf(os.Stderr, "checkpoint saved; resume with: water orchestrate --resume %s\n", st.RunID)
				}
				return exitWith(ExitBackend, runErr)
			}
			if final == "" {
				return exitWith(ExitError, errors.New("run completed without FinalOutput"))
			}
			return nil
		},
	}
	c.Flags().StringVar(&resume, "resume", "", "resume a checkpointed run by id (the id printed when the run started)")
	c.Flags().BoolVar(&list, "list", false, "list resumable runs")
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
