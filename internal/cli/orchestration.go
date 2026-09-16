package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"water/internal/agent"
	"water/internal/backend"
	"water/internal/config"
	"water/internal/orchestrator"
	"water/internal/roles"
	"water/internal/trace"
)

// orchOpts tunes one orchestration run for the command driving it.
type orchOpts struct {
	// Solo collapses the graph to the CEO alone (measurement mode).
	Solo bool
	// ResumeHint is the command prefix offered after an interrupted run,
	// e.g. "water orchestrate --resume". Empty suppresses the hint.
	ResumeHint string
	// AfterRun runs once the graph has produced a final output, while the
	// trace is still open so any further model call is recorded against it.
	AfterRun func(ctx context.Context, env agent.Env, st *orchestrator.State, final string) error
}

// orchRun is a completed orchestration.
type orchRun struct {
	State *orchestrator.State
	Env   agent.Env
	Final string
	Stats trace.Stats
}

// runOrchestration executes one graph run against an already-built state: it
// resolves backends and per-role tool policies, builds the router and graph,
// opens the trace, runs the executor, and maps failures onto exit codes. The
// caller owns the state (fresh or resumed) and the checkpointer.
func (a *App) runOrchestration(ctx context.Context, cfg *config.Resolved, reg *roles.Registry, st *orchestrator.State, brief string, cp orchestrator.Checkpointer, opts orchOpts) (*orchRun, error) {
	def, err := a.selectBackend(ctx)
	if err != nil {
		return nil, err
	}
	if w := meteredLeakWarning(def); w != "" && !a.jsonMode() {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	env, _, err := a.roleEnv(ctx, reg, def)
	if err != nil {
		return nil, exitWith(ExitBackend, err)
	}

	orch := reg.Orchestrator()
	var delegates []string
	for _, r := range reg.Delegates() {
		delegates = append(delegates, r.Slug)
	}
	router, ok := orchestrator.NewRouter(cfg.Orchestration.Router, orch.Slug, delegates)
	if !ok {
		return nil, exitWith(ExitUsage, fmt.Errorf("unknown router %q (known: %v)", cfg.Orchestration.Router, orchestrator.RouterNames()))
	}
	if h, ok := router.(*orchestrator.HierarchyRouter); ok {
		h.MaxRounds = cfg.Orchestration.MaxRounds
		if opts.Solo {
			// Measurement mode (Part 2 follow-up): CEO answers alone,
			// delegating nothing. Same brief, same persona, no graph.
			h.COO, h.Specialists = "", nil
		}
	}
	st.SetEdges(orchestrator.EdgesFor(router))

	rec, err := trace.New(cfg.Telemetry.TraceDir, st.RunID)
	if err != nil {
		return nil, err
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
	active := newActiveNodes()
	ex := &orchestrator.Executor{
		MaxParallel:  cfg.Orchestration.MaxParallel,
		Timeout:      runTimeout(cfg),
		MaxSteps:     cfg.Orchestration.MaxSteps,
		Checkpointer: cp,
		Hooks: orchestrator.Hooks{
			NodeStarted:  func(role string) { active.start(role); sf.NodeStarted(role); rec.NodeStarted(role) },
			NodeFinished: func(role string, d time.Duration, err error) { active.finish(role); rec.NodeFinished(role, d, err) },
			Checkpointed: func(step int) { rec.Checkpoint(step) },
		},
	}
	// Live debugging: `water debug dump <run-id>` signals this process,
	// which writes a state dump without stopping the run.
	_ = os.MkdirAll(cfg.Orchestration.CheckpointDir, 0o755)
	pf := pidFile(cfg.Orchestration.CheckpointDir, st.RunID)
	_ = os.WriteFile(pf, []byte(fmt.Sprintf(`{"pid":%d,"started":%q}`, os.Getpid(), time.Now().Format(time.RFC3339))), 0o600)
	defer os.Remove(pf)
	disarm := armStateDump(func() (string, error) { return writeStateDump(cfg.Telemetry.TraceDir, st, router, active) })
	defer disarm()
	if !a.jsonMode() && !a.flags.quiet {
		fmt.Fprintf(os.Stderr, "live dump: water debug dump %s\n", st.RunID)
	}
	sf.RunStarted(st.RunID, brief)
	rec.RunStarted(brief)
	st.Observe(func(m orchestrator.AgentMessage) { rec.Message(m); sf.MessageSent(m) })
	runErr := ex.Run(ctx, g, st)
	final, _ := st.FinalOutput()
	if runErr != nil {
		rec.Error(runErr)
	}
	// finish closes the trace exactly once, on every path out of here.
	finish := func() trace.Stats {
		stats := rec.Finish()
		sf.RunFinished(final, stats)
		_ = backend.SaveRateLimit(config.Home(), stats.LastRateLimit)
		return stats
	}
	if runErr != nil {
		stats := finish()
		if cfg.Orchestration.Checkpointer == orchestrator.FileCheckpointerName && opts.ResumeHint != "" {
			fmt.Fprintf(os.Stderr, "checkpoint saved; resume with: %s %s\n", opts.ResumeHint, st.RunID)
		}
		if errors.Is(runErr, backend.ErrRateLimited) {
			msg := "run interrupted by the subscription rate limit, not by a failure"
			if stats.LastRateLimit != nil && !stats.LastRateLimit.FiveHourResets.IsZero() {
				msg += " (window resets " + stats.LastRateLimit.FiveHourResets.Local().Format("Mon 15:04") + ")"
			}
			if opts.ResumeHint != "" {
				return nil, exitWith(ExitRateLimited, fmt.Errorf("%s; `%s %s` continues it", msg, opts.ResumeHint, st.RunID))
			}
			return nil, exitWith(ExitRateLimited, errors.New(msg))
		}
		return nil, exitWith(ExitBackend, runErr)
	}
	if final == "" {
		finish()
		return nil, exitWith(ExitError, errors.New("run completed without FinalOutput"))
	}
	if opts.AfterRun != nil {
		if err := opts.AfterRun(ctx, env, st, final); err != nil {
			finish()
			return nil, err
		}
	}
	return &orchRun{State: st, Env: env, Final: final, Stats: finish()}, nil
}
