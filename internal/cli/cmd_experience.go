package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"water/internal/experience"
)

// experienceCmd is a dev-only, explicit tool: offline experience.md growth
// and promotion-candidate review. It is never invoked by `run` or
// `orchestrate` — a role never writes to its own experience.md while
// answering — and it never writes frameworks.md; promotion stays a manual,
// reviewed edit.
func (a *App) experienceCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "experience <role> <grow|candidates>",
		Short: "Offline experience.md growth and promotion-candidate review (dev-only)",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			role := args[0]
			dir := a.flags.agentsDir
			if dir == "" {
				dir = "agents"
			}
			roleDir := filepath.Join(dir, role)
			if info, err := os.Stat(roleDir); err != nil || !info.IsDir() {
				return exitWith(ExitUsage, fmt.Errorf("unknown role directory %q (pass --agents-dir if not running from the repo root)", roleDir))
			}
			switch args[1] {
			case "grow":
				return a.experienceGrow(cmd, dir, role)
			case "candidates":
				minSupport, _ := cmd.Flags().GetInt("min-confidence")
				return a.experienceCandidates(dir, role, minSupport)
			default:
				return exitWith(ExitUsage, fmt.Errorf("unknown experience action %q (want grow|candidates)", args[1]))
			}
		},
	}
	c.Flags().String("question", "", "path to a file with the original question (grow)")
	c.Flags().String("response", "", "path to a file with the role's response (grow)")
	c.Flags().String("feedback", "", "path to a file with reviewer feedback on the response (grow)")
	c.Flags().Bool("dry-run", false, "reflect but write nothing (grow)")
	c.Flags().Int("min-confidence", 2, "minimum independent-source support to list (candidates)")
	return c
}

func (a *App) experienceGrow(cmd *cobra.Command, dir, role string) error {
	qPath, _ := cmd.Flags().GetString("question")
	rPath, _ := cmd.Flags().GetString("response")
	fPath, _ := cmd.Flags().GetString("feedback")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if qPath == "" || rPath == "" || fPath == "" {
		return exitWith(ExitUsage, fmt.Errorf("grow requires --question, --response, and --feedback file paths"))
	}
	question, err := os.ReadFile(qPath)
	if err != nil {
		return exitWith(ExitUsage, err)
	}
	response, err := os.ReadFile(rPath)
	if err != nil {
		return exitWith(ExitUsage, err)
	}
	feedback, err := os.ReadFile(fPath)
	if err != nil {
		return exitWith(ExitUsage, err)
	}

	ctx := context.Background()
	sel, err := a.selectBackend(ctx)
	if err != nil {
		return err
	}
	if w := meteredLeakWarning(sel); w != "" && !a.jsonMode() {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}

	result, err := experience.Grow(ctx, sel.Backend, dir, role, string(question), string(response), string(feedback), dryRun)
	if err != nil {
		return exitWith(ExitBackend, err)
	}
	fmt.Printf("action: %s\n", result.Action)
	if result.Rationale != "" {
		fmt.Printf("rationale: %s\n", result.Rationale)
	}
	switch result.Action {
	case "new":
		fmt.Printf("new lesson: %s\n", result.Lesson)
	case "reinforce":
		fmt.Printf("reinforced: %s\n", result.Matched)
	}
	if dryRun {
		fmt.Fprintln(os.Stderr, "(dry run — nothing written)")
	}
	return nil
}

func (a *App) experienceCandidates(dir, role string, minSupport int) error {
	cands, err := experience.Candidates(dir, role, minSupport)
	if err != nil {
		return err
	}
	for _, c := range cands {
		fmt.Printf("(support %d) %s\n  drawn from: %s\n\n", c.Support, c.Sentence, strings.Join(c.Sources, ", "))
	}
	if len(cands) == 0 {
		fmt.Fprintln(os.Stderr, "(no lessons at or above --min-confidence)")
	}
	fmt.Println("Promotion into frameworks.md is a manual, reviewed edit — this command does not write it.")
	return nil
}
