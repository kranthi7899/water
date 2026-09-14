package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"water/internal/agent"
	"water/internal/orchestrator"
	"water/internal/trace"
	"water/internal/voice"
)

func (a *App) runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run <role> [prompt]",
		Short: "Single-role session, bypasses the graph",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.flags.voice {
				return exitWith(ExitUsage, voice.ErrUnavailable)
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			reg, err := a.roleRegistry()
			if err != nil {
				return err
			}
			role, ok := reg.Get(args[0])
			if !ok {
				return exitWith(ExitUsage, fmt.Errorf("unknown role %q (known: %s)", args[0], strings.Join(reg.Slugs(), ", ")))
			}
			prompt := ""
			if len(args) == 2 {
				prompt = args[1]
			} else if prompt, err = readStdin(); err != nil {
				return exitWith(ExitUsage, err)
			}
			if strings.TrimSpace(prompt) == "" {
				return exitWith(ExitUsage, errors.New("empty prompt"))
			}
			ctx := context.Background()
			sel, err := a.selectBackend(ctx)
			if err != nil {
				return err
			}
			if w := meteredLeakWarning(sel); w != "" && !a.jsonMode() {
				fmt.Fprintln(os.Stderr, "warning:", w)
			}
			runID := orchestrator.NewRunID()
			rec, err := trace.New(cfg.Telemetry.TraceDir, runID)
			if err != nil {
				return err
			}
			sf := a.surfaces()
			sf.RunStarted(runID, prompt)
			rec.RunStarted(prompt)
			sf.NodeStarted(role.Slug)
			rec.NodeStarted(role.Slug)
			env := agent.Env{Backend: sel.Backend, Surface: sf, Trace: rec, Timeout: runTimeout(cfg)}
			resp, _, err := agent.RunSingle(ctx, role, env, prompt)
			rec.NodeFinished(role.Slug, resp.Duration, err)
			if err != nil {
				sf.NodeFailed(role.Slug, err)
				rec.Error(err)
				sf.RunFinished("", rec.Finish())
				return exitWith(ExitBackend, err)
			}
			sf.NodeFinished(role.Slug, resp)
			sf.RunFinished(resp.Text, rec.Finish())
			return nil
		},
	}
}
