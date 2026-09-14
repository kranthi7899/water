package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"water/internal/config"
	"water/internal/surface"
)

// Execute runs the command tree and returns a process exit code.
func Execute(embedded fs.FS, args []string) int {
	app := NewApp(embedded)
	root := app.rootCmd()
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			fmt.Fprintln(os.Stderr, surface.StyleErr.Render("error:"), ee.err)
			return ee.code
		}
		fmt.Fprintln(os.Stderr, surface.StyleErr.Render("error:"), err)
		return ExitError
	}
	return ExitOK
}

func (a *App) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "water",
		Short: "A council of role-agents on your existing subscription.",
		Long: surface.StyleDim.Render(surface.Banner) + `
water hosts role-agents (a singleton CEO plus delegates), each with a private
persona and private memory, orchestrated through a native state graph, running
on the Claude or ChatGPT subscription CLIs you already pay for.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// No subcommand: status-aware. Onboard if unconfigured, else help.
			if _, err := os.Stat(config.Path()); errors.Is(err, os.ErrNotExist) {
				if !isTTY(os.Stdin) && !a.flags.yes {
					fmt.Fprintln(os.Stderr, "water is not configured. Run `water onboard` (add --yes for automation).")
					return exitWith(ExitUnconfigured, errors.New("not configured"))
				}
				return a.runOnboard(cmd)
			}
			return cmd.Help()
		},
	}
	pf := root.PersistentFlags()
	pf.StringVar(&a.flags.agentsDir, "agents-dir", "", "overlay a local agents directory (dev)")
	pf.StringVar(&a.flags.traceDir, "trace", "", "trace directory override (dev)")
	_ = pf.MarkHidden("agents-dir")
	_ = pf.MarkHidden("trace")
	pf.StringVarP(&a.flags.output, "output", "o", "text", "output format: text|json")
	pf.BoolVar(&a.flags.jsonOut, "json", false, "shorthand for --output json")
	pf.StringVar(&a.flags.backend, "backend", "", "backend to use (claude-subscription|codex-subscription|api|auto)")
	pf.BoolVar(&a.flags.allowMetered, "allow-metered", false, "permit a metered API backend for this invocation")
	pf.BoolVarP(&a.flags.yes, "yes", "y", false, "assume yes; never prompt")
	pf.BoolVarP(&a.flags.quiet, "quiet", "q", false, "suppress progress output")
	pf.BoolVarP(&a.flags.verbose, "verbose", "v", false, "show per-node responses as they arrive")
	pf.BoolVar(&a.flags.voice, "voice", false, "voice I/O (reserved; not yet available)")

	root.AddCommand(
		a.onboardCmd(), a.doctorCmd(), a.statusCmd(), a.runCmd(), a.orchestrateCmd(),
		a.memoryCmd(), a.experienceCmd(), a.configCmd(), a.versionCmd(), a.voiceCmd(), a.dashboardCmd(),
	)
	root.CompletionOptions.HiddenDefaultCmd = true
	return root
}
