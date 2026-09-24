package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"water/internal/approvals"
)

// approveCmd lists pending approvals as a numbered menu (the A1 code-generated
// read-back — never a model's paraphrase) and lets the CEO decide one.
func (a *App) approveCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "approve",
		Short: "Review and decide pending approvals",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newDaemonClient()
			if err != nil {
				return exitWith(ExitError, err)
			}
			ctx := context.Background()
			envs, err := client.Pending(ctx)
			if err != nil {
				return exitWith(ExitError, err)
			}
			fmt.Println(approvals.Menu(envs))
			if len(envs) == 0 {
				return nil
			}
			if !isTTY(os.Stdin) {
				return nil
			}
			in := bufio.NewReader(os.Stdin)
			for {
				fmt.Print("> ")
				line, _ := in.ReadString('\n')
				line = strings.TrimSpace(line)
				if line == "" || strings.EqualFold(line, "q") || strings.EqualFold(line, "quit") {
					return nil
				}
				n, err := strconv.Atoi(line)
				if err != nil || n < 1 || n > len(envs) {
					fmt.Println("enter a number from the list, or q to quit")
					continue
				}
				e := envs[n-1]
				fmt.Println(approvals.ReadBack(e))
				fmt.Print("yes or no> ")
				reply, _ := in.ReadString('\n')
				result, err := client.Decide(ctx, e.ID, e.PayloadHash, reply)
				if err != nil {
					return exitWith(ExitError, err)
				}
				fmt.Printf("%s: %s\n", e.ID, result.Envelope.Status)
				if line := decisionOutcome(result); line != "" {
					fmt.Println(line)
				}
				return nil
			}
		},
	}
	return c
}

// decisionOutcome says what happened to an approved action. An unknown
// outcome or a ran-but-errored one must never read as "not executed": that
// would invite asking for the same send again.
func decisionOutcome(r DecisionResult) string {
	switch {
	case r.OutcomeUnknown:
		return "outcome unknown, it may have gone through: check (e.g. Sent mail, the calendar) before asking for it again: " + r.Error
	case r.Executed && r.Error != "":
		return fmt.Sprintf("executed, but: %s (output: %s)", r.Error, string(r.Output))
	case r.Executed:
		return "executed: " + string(r.Output)
	case r.Error != "":
		return "not executed: " + r.Error
	}
	return ""
}
