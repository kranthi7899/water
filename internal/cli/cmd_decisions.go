package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

// decisionsCmd lists then shows prepared decision cards, mirroring how
// `water approve` lists then acts on pending approvals: `list` runs the
// classification-trigger orchestration over today's candidate items and
// prints one line per card (code-built, ranked by severity then deadline);
// `show <id>` renders one of them in full with Card.Render().
func (a *App) decisionsCmd() *cobra.Command {
	c := &cobra.Command{Use: "decisions", Short: "Review decision cards the twin has prepared"}
	c.AddCommand(a.decisionsListCmd(), a.decisionsShowCmd())
	return c
}

func (a *App) decisionsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List open decision cards, ranked by severity then deadline",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newDaemonClient()
			if err != nil {
				return exitWith(ExitError, err)
			}
			cards, err := client.Decisions(context.Background())
			if err != nil {
				return exitWith(ExitError, err)
			}
			if len(cards) == 0 {
				fmt.Println("no open decision cards")
				return nil
			}
			for _, c := range cards {
				deadline := "no deadline"
				if c.Deadline != nil {
					deadline = "due " + c.Deadline.Local().Format("Mon 2006-01-02")
				}
				fmt.Printf("%s  [%s, severity %d, %s]  %s\n", c.ID, c.TypeID, c.Severity, deadline, c.Lead)
			}
			return nil
		},
	}
}

func (a *App) decisionsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Render one decision card in full",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newDaemonClient()
			if err != nil {
				return exitWith(ExitError, err)
			}
			cards, err := client.Decisions(context.Background())
			if err != nil {
				return exitWith(ExitError, err)
			}
			for _, c := range cards {
				if c.ID == args[0] {
					fmt.Println(c.Render())
					return nil
				}
			}
			return exitWith(ExitUsage, fmt.Errorf("no open decision card %q (run `water decisions list`)", args[0]))
		},
	}
}
