package cli

import (
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"water/internal/tools"
)

// mcpServeCmd is the hidden MCP stdio server the headless `claude` CLI spawns
// (Part 5). It enforces the policy file it is handed and appends every
// invocation to the log; the parent merges the log into the run trace.
func (a *App) mcpServeCmd() *cobra.Command {
	var policyPath, logPath string
	c := &cobra.Command{
		Use:    "mcp-serve",
		Short:  "Serve Water's tool layer over MCP stdio (spawned by the model CLI)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if policyPath == "" {
				return exitWith(ExitUsage, fmt.Errorf("--policy is required"))
			}
			pol, err := tools.LoadPolicyFile(policyPath)
			if err != nil {
				return exitWith(ExitUsage, err)
			}
			svc := tools.NewService(pol, func(ev tools.Event) {
				if logPath != "" {
					_ = tools.AppendEvents(logPath, ev)
				}
			})
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			return tools.ServeStdio(ctx, os.Stdin, os.Stdout, svc)
		},
	}
	c.Flags().StringVar(&policyPath, "policy", "", "policy file written by the parent water process")
	c.Flags().StringVar(&logPath, "log", "", "JSONL file to append tool events to")
	return c
}
