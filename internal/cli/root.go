package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"water/internal/backend"
	"water/internal/config"
)

// Execute runs the command tree and returns a process exit code.
func Execute(args []string) int {
	app := NewApp()
	root := app.rootCmd()
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			fmt.Fprintln(os.Stderr, styleErr.Render("error:"), ee.err)
			return ee.code
		}
		fmt.Fprintln(os.Stderr, styleErr.Render("error:"), err)
		return ExitError
	}
	return ExitOK
}

func (a *App) rootCmd() *cobra.Command {
	root := &cobra.Command{
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if a.flags.debug || os.Getenv("WATER_DEBUG") != "" {
				backend.Debug = os.Stderr
			}
		},
		Use:   "water",
		Short: "A CEO digital twin on your existing Claude subscription.",
		Long: styleDim.Render("water") + `
water is a digital twin for one role: the CEO. It understands the role's
responsibilities and environment, reads and prepares work through connectors,
and acts only through an approval-gated daemon — running on the Claude
subscription you already pay for.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// No subcommand: like `claude`, bare `water` is the chat interface.
			// Unconfigured → onboard; no terminal → help.
			if _, err := os.Stat(config.Path()); errors.Is(err, os.ErrNotExist) {
				if !isTTY(os.Stdin) && !a.flags.yes {
					fmt.Fprintln(os.Stderr, "water is not configured. Run `water onboard` (add --yes for automation).")
					return exitWith(ExitUnconfigured, errors.New("not configured"))
				}
				return a.runOnboard(cmd)
			}
			if isTTY(os.Stdin) && isTTY(os.Stdout) {
				return a.runChat(context.Background())
			}
			return cmd.Help()
		},
	}
	pf := root.PersistentFlags()
	pf.StringVar(&a.flags.traceDir, "trace", "", "trace directory override (dev)")
	_ = pf.MarkHidden("trace")
	pf.StringVarP(&a.flags.output, "output", "o", "text", "output format: text|json")
	pf.BoolVar(&a.flags.jsonOut, "json", false, "shorthand for --output json")
	pf.StringVar(&a.flags.backend, "backend", "", "backend to use (claude-subscription|codex-subscription|api|auto)")
	pf.BoolVar(&a.flags.allowMetered, "allow-metered", false, "permit a metered API backend for this invocation")
	pf.BoolVarP(&a.flags.yes, "yes", "y", false, "assume yes; never prompt")
	pf.BoolVarP(&a.flags.quiet, "quiet", "q", false, "suppress progress output")
	pf.BoolVarP(&a.flags.verbose, "verbose", "v", false, "show per-node responses as they arrive")
	pf.BoolVar(&a.flags.voice, "voice", false, "speak replies aloud (uses configured voice provider)")
	pf.BoolVar(&a.flags.debug, "debug", false, "log every model subprocess: real flags (prompt text elided), pid, duration, exit, last stderr line")

	root.AddCommand(
		a.onboardCmd(), a.doctorCmd(), a.statusCmd(), a.chatCmd(),
		a.configCmd(), a.versionCmd(), a.voiceCmd(), a.mcpServeCmd(),
		a.daemonCmd(), a.auditCmd(), a.askCmd(), a.approveCmd(),
		a.connectCmd(), a.decisionsCmd(),
	)
	root.CompletionOptions.HiddenDefaultCmd = true
	return root
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
