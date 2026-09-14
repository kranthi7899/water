package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"water/internal/agent"
	"water/internal/backend"
	"water/internal/chat"
	"water/internal/config"
	"water/internal/orchestrator"
	"water/internal/trace"
	"water/internal/voice"
)

func (a *App) runCmd() *cobra.Command {
	var attach []string
	c := &cobra.Command{
		Use:   "run <role> [prompt]",
		Short: "Single-role turn, bypasses the graph (use `water chat` for a session)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			runID := orchestrator.NewRunID()
			rec, err := trace.New(cfg.Telemetry.TraceDir, runID)
			if err != nil {
				return err
			}
			sf := a.surfaces()
			env.Surface, env.Trace = sf, rec
			sf.RunStarted(runID, prompt)
			rec.RunStarted(prompt)
			sf.NodeStarted(role.Slug)
			rec.NodeStarted(role.Slug)
			var atts []backend.Attachment
			for _, p := range attach {
				at, err := chat.LoadAttachment(p)
				if err != nil {
					return exitWith(ExitUsage, err)
				}
				atts = append(atts, at)
			}
			resp, _, err := agent.RunTurn(ctx, role, env, nil, prompt, atts)
			rec.NodeFinished(role.Slug, resp.Duration, err)
			_ = backend.SaveRateLimit(config.Home(), resp.RateLimit)
			if err != nil {
				// agent.call already reported the failure to the surface.
				rec.Error(err)
				sf.RunFinished("", rec.Finish())
				if errors.Is(err, backend.ErrRateLimited) {
					return exitWith(ExitRateLimited, fmt.Errorf("subscription rate limit reached, not a failure: %w", err))
				}
				return exitWith(ExitBackend, err)
			}
			sf.RunFinished(resp.Text, rec.Finish())
			if a.flags.voice {
				if vp, ok := voice.Open(cfg.Voice.Provider); ok && vp.Available() {
					_ = vp.Speak(ctx, resp.Text)
				} else if ok {
					fmt.Fprintln(os.Stderr, "voice:", voice.Absence(vp))
				}
			}
			return nil
		},
	}
	c.Flags().StringArrayVar(&attach, "attach", nil, "attach a file (image, PDF, or text) to this turn; repeatable")
	return c
}
