package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"water/internal/audit"
	"water/internal/store"
)

// auditCmd inspects and repairs the hash-chained audit log. It opens the
// store read-write only for `repair` (to re-anchor after truncating a torn
// final line); `verify` reads only the log file.
func (a *App) auditCmd() *cobra.Command {
	c := &cobra.Command{Use: "audit", Short: "Inspect and repair the audit log"}
	c.AddCommand(
		&cobra.Command{
			Use:   "verify",
			Short: "Verify the audit log's hash chain",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				n, err := audit.Verify(twinAuditPath(a.twinID()))
				if err != nil {
					return exitWith(ExitError, err)
				}
				fmt.Printf("audit log verifies: %d entries\n", n)
				return nil
			},
		},
		&cobra.Command{
			Use:   "repair",
			Short: "Drop a single torn final line, if that is the only break, and record the repair",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				id := a.twinID()
				st, err := store.Open(twinStorePath(id))
				if err != nil {
					return err
				}
				defer st.Close()
				if err := audit.Repair(twinAuditPath(id), audit.WithAnchor(st)); err != nil {
					return exitWith(ExitError, err)
				}
				fmt.Println("repaired")
				return nil
			},
		},
	)
	return c
}
