package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// decisionsCmd lists then shows prepared decision cards, mirroring how
// `water approve` lists then acts on pending approvals: `list` runs the
// classification-trigger orchestration over today's candidate items and
// prints one line per card (code-built, ranked by severity then deadline);
// `show <id>` renders one of them in full with Card.Render().
func (a *App) decisionsCmd() *cobra.Command {
	c := &cobra.Command{Use: "decisions", Short: "Review decision cards the twin has prepared"}
	c.AddCommand(a.decisionsListCmd(), a.decisionsShowCmd(), a.decisionsEmailCmd())
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

// decisionsEmailCmd wires internal/reports into a real, reachable call
// site: it renders a decision card as a self-contained HTML report and
// stages sending it (with the report as an attachment) as an ordinary
// level-A gmail.send_message approval -- the daemon proposes the envelope,
// it does not send anything itself, so `water approve` (or the normal
// approval flow) still decides whether the mail actually goes out.
func (a *App) decisionsEmailCmd() *cobra.Command {
	var to, subject, body string
	c := &cobra.Command{
		Use:   "email <id>",
		Short: "Email a decision card as an HTML report (staged for approval, not sent)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			addrs := splitAddrs(to)
			if len(addrs) == 0 {
				return exitWith(ExitUsage, fmt.Errorf("--to is required"))
			}
			client, err := newDaemonClient()
			if err != nil {
				return exitWith(ExitError, err)
			}
			res, err := client.EmailDecisionReport(context.Background(), args[0], addrs, subject, body)
			if err != nil {
				return exitWith(ExitError, err)
			}
			if res.Status != "queued" {
				return exitWith(ExitError, fmt.Errorf("not staged: %s", res.Reason))
			}
			fmt.Printf("staged as approval %s; run `water approve` to send it\n", res.ApprovalID)
			return nil
		},
	}
	c.Flags().StringVar(&to, "to", "", "comma-separated recipient address(es)")
	c.Flags().StringVar(&subject, "subject", "", "subject line (default: the card's own summary)")
	c.Flags().StringVar(&body, "body", "", "plain-text body (default: the card's own question)")
	return c
}

func splitAddrs(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
