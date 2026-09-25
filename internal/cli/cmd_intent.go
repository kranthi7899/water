package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"water/internal/config"
	"water/internal/nervous/promote"
)

// intentCmd is the top-level `water intent` command group: the owner-facing
// half of the promotion loop (Design §16, R-22/R-23) — list what the
// registry currently knows about, draft a candidate into a reviewable
// learned-intent file, promote a reviewed draft into the live overlay, and
// manually demote or re-enable any intent (embedded or learned).
func (a *App) intentCmd() *cobra.Command {
	c := &cobra.Command{Use: "intent", Short: "Manage embedded and learned intents (the promotion loop)"}
	c.AddCommand(a.intentListCmd(), a.intentDraftCmd(), a.intentPromoteCmd(), a.intentDemoteCmd(), a.intentEnableCmd())
	return c
}

// intentListCmd implements `water intent list [--json]`: GET /v1/intents.
func (a *App) intentListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "list",
		Short: "List every intent the registry knows about: embedded, learned, inactive, disabled",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dc, err := newDaemonClient()
			if err != nil {
				return exitWith(ExitError, err)
			}
			items, err := dc.IntentList(cmd.Context())
			if err != nil {
				return exitWith(ExitError, err)
			}
			if a.jsonMode() {
				return printJSON(items)
			}
			fmt.Print(renderIntentList(items))
			return nil
		},
	}
	return c
}

// renderIntentList formats GET /v1/intents as a table: ID, ORIGIN
// (embedded/learned), STATE (active/inactive/disabled) and, for anything
// not active, why.
func renderIntentList(items []IntentListItem) string {
	if len(items) == 0 {
		return styleDim.Render("no intents") + "\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-32s %-10s %-10s  %s\n", "ID", "ORIGIN", "STATE", "REASON")
	for _, it := range items {
		state, reason := "active", ""
		switch {
		case it.Disabled != "":
			state, reason = "disabled", it.Disabled
		case !it.Active:
			state, reason = "inactive", it.InactiveReason
		}
		fmt.Fprintf(&b, "%-32s %-10s %-10s  %s\n", it.ID, it.Origin, state, reason)
	}
	return b.String()
}

// intentDraftCmd implements `water intent draft <candidate-id> [--json]`:
// POST /v1/intents/draft, printing the drafted YAML and whether it
// currently validates.
func (a *App) intentDraftCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "draft <candidate-id>",
		Short: "Draft a learned intent from a promotion candidate (one cold model call), for review before promoting",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dc, err := newDaemonClient()
			if err != nil {
				return exitWith(ExitError, err)
			}
			out, err := dc.IntentDraft(cmd.Context(), args[0])
			if err != nil {
				return exitWith(ExitError, err)
			}
			if a.jsonMode() {
				return printJSON(out)
			}
			fmt.Print(renderIntentDraft(out))
			if !out.Valid {
				return exitWith(ExitError, fmt.Errorf("draft does not currently validate"))
			}
			return nil
		},
	}
	return c
}

// renderIntentDraft prints a drafted intent's YAML followed by its
// validation verdict, and where it was written for review.
func renderIntentDraft(out IntentDraftResult) string {
	var b strings.Builder
	fmt.Fprintln(&b, out.YAML)
	fmt.Fprintf(&b, "%s %s\n", styleDim.Render("written to"), out.Path)
	if out.Valid {
		fmt.Fprintln(&b, styleAccent.Render("valid")+" — review the YAML above, then `water intent promote "+out.ID+"` (needs the candidate id, not this intent id) to make it live")
	} else {
		fmt.Fprintln(&b, styleWarn.Render("invalid")+": "+out.ValidationError)
	}
	return b.String()
}

// intentPromoteCmd implements `water intent promote <candidate-id>`. It
// checks router.promotion.enabled first (GET /v1/router) so a disabled
// promotion loop is refused with a helpful message before ever asking for
// confirmation; shows the EXACT pending draft file `water intent draft`
// wrote and the owner already reviewed (read straight off local
// $WATER_HOME — the CLI runs on the same machine, the same convention
// `water route eval --tier1` already uses for its own model/registry
// reads), never a freshly re-drafted one (POST /v1/intents/draft makes a
// new cold model call and could silently produce different content than
// what was reviewed); asks an interactive y/N (mirroring `water approve`'s
// prompt/read pattern — a bufio.Reader over os.Stdin, one line read,
// matched case-insensitively); and only then calls POST
// /v1/intents/promote, which re-reads that same on-disk file itself,
// re-validates it against the daemon's CURRENT live registry (never trusting
// a client-supplied body), writes it into the learned overlay and reloads —
// a single source of truth for what actually gets promoted. --yes (the
// shared global flag) skips the prompt, matching every other confirmation
// in this CLI.
func (a *App) intentPromoteCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "promote <candidate-id>",
		Short: "Promote a reviewed draft into the live learned-intent overlay",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			candID := args[0]
			// Reject anything that isn't a real candidate id's shape (12
			// hex characters) before it ever reaches a filepath.Join — the
			// same check the daemon's own POST /v1/intents/promote makes,
			// kept here too so a malformed id is refused with a clear
			// message before any file I/O, not a confusing "no such file".
			if !promote.ValidCandidateID(candID) {
				return exitWith(ExitUsage, fmt.Errorf("%q is not a valid candidate id (want the 12-hex-character id `water route candidates` reports)", candID))
			}
			dc, err := newDaemonClient()
			if err != nil {
				return exitWith(ExitError, err)
			}
			ctx := cmd.Context()

			health, err := dc.RouterHealth(ctx)
			if err != nil {
				return exitWith(ExitError, err)
			}
			if !health.PromotionEnabled {
				return exitWith(ExitUsage, fmt.Errorf("the promotion loop is disabled (router.promotion.enabled=false); enable it in config before promoting %q", candID))
			}

			pendingPath := filepath.Join(promote.PendingDir(config.Home(), a.twinID()), candID+".yaml")
			yamlBytes, err := os.ReadFile(pendingPath)
			if err != nil {
				return exitWith(ExitUsage, fmt.Errorf("no pending draft for %q (run `water intent draft %s` first): %w", candID, candID, err))
			}
			fmt.Println(string(yamlBytes))
			fmt.Printf("%s %s\n", styleDim.Render("pending draft"), pendingPath)

			if !a.confirmYesNo(fmt.Sprintf("promote %s?", candID)) {
				fmt.Println("not promoted")
				return nil
			}

			out, err := dc.IntentPromote(ctx, candID)
			if err != nil {
				return exitWith(ExitError, err)
			}
			if a.jsonMode() {
				return printJSON(out)
			}
			fmt.Printf("promoted: %s (%s), reloaded=%v\n", out.ID, out.Path, out.Reloaded)
			return nil
		},
	}
	return c
}

// confirmYesNo asks an interactive y/N question on stdout/stdin: --yes (the
// shared global flag) skips the prompt and always answers yes; a
// non-interactive session with no --yes refuses rather than hanging on a
// read that will never come (the same posture root.go's bare `water` takes
// before `water onboard`). Otherwise it defers to promptYesNo, the pure
// prompt/read/match logic, kept separate so it's directly unit-testable
// without a real terminal.
func (a *App) confirmYesNo(question string) bool {
	if a.flags.yes {
		return true
	}
	if !isTTY(os.Stdin) {
		return false
	}
	return promptYesNo(os.Stdin, os.Stdout, question)
}

// promptYesNo writes question followed by " [y/N] " to out, reads exactly
// one line from in, and reports whether it was "y" or "yes"
// (case-insensitive, surrounding whitespace trimmed). Anything else —
// including a blank line, "n", or garbage — refuses: mirrors `water
// approve`'s own prompt/read shape (a bufio.Reader, ReadString('\n')), but
// default-refuse rather than auth.Confirm's default-yes [Y/n], since
// promoting an intent writes a new learned-intent file and reloads the live
// registry — silence must never mean yes here.
func promptYesNo(in io.Reader, out io.Writer, question string) bool {
	fmt.Fprintf(out, "%s [y/N] ", question)
	line, _ := bufio.NewReader(in).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

// intentDemoteCmd implements `water intent demote <id> [--reason]`: POST
// /v1/intents/demote. Not gated on router.promotion.enabled — an owner may
// want to silence a misbehaving embedded intent too, not only a learned
// one.
func (a *App) intentDemoteCmd() *cobra.Command {
	var reason string
	c := &cobra.Command{
		Use:   "demote <id>",
		Short: "Manually disable an intent (embedded or learned) so it can no longer answer",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dc, err := newDaemonClient()
			if err != nil {
				return exitWith(ExitError, err)
			}
			out, err := dc.IntentDemote(cmd.Context(), args[0], reason)
			if err != nil {
				return exitWith(ExitError, err)
			}
			if a.jsonMode() {
				return printJSON(out)
			}
			fmt.Printf("demoted: %s, reloaded=%v\n", out.ID, out.Reloaded)
			return nil
		},
	}
	c.Flags().StringVar(&reason, "reason", "manual", "why this intent is being disabled")
	return c
}

// intentEnableCmd implements `water intent enable <id>`: POST
// /v1/intents/enable.
func (a *App) intentEnableCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "enable <id>",
		Short: "Re-enable a manually or automatically demoted intent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dc, err := newDaemonClient()
			if err != nil {
				return exitWith(ExitError, err)
			}
			out, err := dc.IntentEnable(cmd.Context(), args[0])
			if err != nil {
				return exitWith(ExitError, err)
			}
			if a.jsonMode() {
				return printJSON(out)
			}
			fmt.Printf("enabled: %s, reloaded=%v\n", out.ID, out.Reloaded)
			return nil
		},
	}
	return c
}
