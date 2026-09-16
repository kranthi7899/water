package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"water/internal/agent"
	"water/internal/config"
	"water/internal/orchestrator"
	"water/internal/sitebuild"
	"water/internal/tools"
)

func (a *App) buildSiteCmd() *cobra.Command {
	var out, name string
	var open, overwrite bool
	c := &cobra.Command{
		Use:   "build-site \"<brief>\"",
		Short: "Run the council on a brief, then write a static site after you approve the files",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			reg, err := a.roleRegistry()
			if err != nil {
				return err
			}
			brief := ""
			if len(args) == 1 {
				brief = args[0]
			} else if brief, err = readStdin(); err != nil {
				return exitWith(ExitUsage, err)
			}
			if strings.TrimSpace(brief) == "" {
				return exitWith(ExitUsage, errors.New("empty brief"))
			}
			absOut, err := filepath.Abs(out)
			if err != nil {
				return exitWith(ExitUsage, err)
			}
			// The output directory is a workspace, never water's own state:
			// that tree holds every role's memory, transcripts and the keyring.
			if within(absOut, config.Home()) {
				return exitWith(ExitUsage, fmt.Errorf("--out %s is inside water's own state directory", absOut))
			}

			ctx := context.Background()
			st := orchestrator.NewState("", brief, reg.OrchestratorSlugs())
			var plan sitebuild.SitePlan
			run, err := a.runOrchestration(ctx, cfg, reg, st, brief, a.checkpointer(reg), orchOpts{
				ResumeHint: "water orchestrate --resume",
				// The site content is a re-expression of the CEO's final
				// synthesis, asked of the CEO itself: no specialist's words
				// reach the page except through the decision the CEO wrote.
				AfterRun: func(ctx context.Context, env agent.Env, st *orchestrator.State, final string) error {
					resp, _, err := agent.RunSingle(ctx, reg.Orchestrator(), env, sitebuild.BuildPrompt(brief, final))
					if err != nil {
						return exitWith(ExitBackend, fmt.Errorf("site plan turn: %w", err))
					}
					plan, err = sitebuild.ParsePlan(resp.Text)
					if err != nil {
						return exitWith(ExitError, fmt.Errorf("parsing site plan: %w\nraw: %s", err, resp.Text))
					}
					return nil
				},
			})
			if err != nil {
				return err
			}

			files, err := sitebuild.Render(plan, name)
			if err != nil {
				return exitWith(ExitError, err)
			}
			if err := os.MkdirAll(absOut, 0o755); err != nil {
				return exitWith(ExitUsage, err)
			}
			pol := &tools.Policy{
				Role:       "build-site",
				RunID:      run.State.RunID,
				Filesystem: tools.FSPolicy{Mode: "read-write", Roots: []string{absOut}},
				Network:    "none",
				Protected:  []string{config.Home()},
			}
			table, err := sitebuild.Inspect(pol, files)
			if err != nil {
				return exitWith(ExitUsage, err)
			}
			table.Intent = fmt.Sprintf("build a static site for %q", truncateLine(brief, 70))
			if n := table.Overwrites(); n > 0 && a.flags.yes && !overwrite {
				return exitWith(ExitUsage, fmt.Errorf("refusing to overwrite %d existing files; pass --overwrite", n))
			}
			if !sitebuild.Confirm(os.Stdin, os.Stderr, table, a.flags.yes) {
				fmt.Fprintln(os.Stderr, "declined; nothing written")
				return nil
			}
			if err := sitebuild.Apply(ctx, tools.NewService(pol, nil), files); err != nil {
				return exitWith(ExitError, err)
			}

			index := filepath.Join(absOut, "index.html")
			if a.jsonMode() {
				paths := make([]string, 0, len(table.Rows))
				for _, r := range table.Rows {
					paths = append(paths, r.Resolved)
				}
				return printJSON(map[string]any{"run_id": run.State.RunID, "out": absOut, "files": paths})
			}
			fmt.Fprintf(os.Stderr, "wrote %d files to %s\n", len(table.Rows), absOut)
			fmt.Println(index)
			if open {
				// Opening a file means launching something; build-site has no
				// shell, so it hands the path over and stops.
				fmt.Fprintln(os.Stderr, "open it in a browser:", index)
			}
			return nil
		},
	}
	c.Flags().StringVar(&out, "out", "./water-site", "directory to write the site into")
	c.Flags().StringVar(&name, "name", "site", "name for the generated site")
	c.Flags().BoolVar(&open, "open", false, "print the page path for opening (never launches a process)")
	c.Flags().BoolVar(&overwrite, "overwrite", false, "permit replacing existing files when --yes is set")
	return c
}

// within reports whether path sits inside dir.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
