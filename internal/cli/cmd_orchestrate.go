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
	"water/internal/voice"
)

func (a *App) orchestrateCmd() *cobra.Command {
	var resume string
	c := &cobra.Command{
		Use:   "orchestrate \"<brief>\"",
		Short: "Full graph run: CEO decomposes → delegates → CEO synthesises",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.flags.voice {
				return exitWith(ExitUsage, voice.ErrUnavailable)
			}
			if resume != "" {
				// Checkpointer is NoopCheckpointer in Phase 1; the flag exists so
				// resumable runs are a provider swap later.
				return exitWith(ExitUsage, fmt.Errorf("--resume %s: %w (checkpointer is %q)", resume, orchestrator.ErrNoCheckpoint, orchestrator.NoopCheckpointer{}.Name()))
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			reg, err := a.roleRegistry()
			if err != nil {
				return err
			}
			brief := ""
			if len(args) == 1 {
				brief = args[0]
			} else if brief, err = readStdin(); err != nil {
				return exitWith(ExitUsage, err)
			}
			if strings.TrimSpace(brief) == "" {
				return exitWith(ExitUsage, errors.New("empty brief"))
			}
			ctx := context.Background()
			sel, err := a.selectBackend(ctx)
			if err != nil {
				return err
			}
			if w := meteredLeakWarning(sel); w != "" && !a.jsonMode() {
				fmt.Fprintln(os.Stderr, "warning:", w)
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

			st := orchestrator.NewState("", brief, reg.OrchestratorSlugs())
			rec, err := trace.New(cfg.Telemetry.TraceDir, st.RunID)
			if err != nil {
				return err
			}
			sf := a.surfaces()
			env := agent.Env{Backend: sel.Backend, Surface: sf, Trace: rec, Timeout: runTimeout(cfg)}
			g := &orchestrator.Graph{Nodes: map[string]orchestrator.Node{}, Router: router}
			for _, r := range reg.All() {
				g.Nodes[r.Slug] = agent.Node(r, env, delegates)
			}
			ex := &orchestrator.Executor{
				MaxParallel:  cfg.Orchestration.MaxParallel,
				Timeout:      runTimeout(cfg),
				Checkpointer: orchestrator.NoopCheckpointer{},
				Hooks: orchestrator.Hooks{
					NodeStarted:  func(role string) { sf.NodeStarted(role); rec.NodeStarted(role) },
					NodeFinished: func(role string, d time.Duration, err error) { rec.NodeFinished(role, d, err) },
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
				return exitWith(ExitBackend, runErr)
			}
			if final == "" {
				return exitWith(ExitError, errors.New("run completed without FinalOutput"))
			}
			return nil
		},
	}
	c.Flags().StringVar(&resume, "resume", "", "resume a checkpointed run by id (reserved)")
	return c
}
