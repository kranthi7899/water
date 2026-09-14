package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"water/internal/memory"
	"water/internal/roles"
	"water/internal/surface"
)

func (a *App) memoryCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "memory <role> <list|add|remove|prune>",
		Short: "Manage a role's bounded session memory",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := a.roleRegistry()
			if err != nil {
				return err
			}
			role, ok := reg.Get(args[0])
			if !ok {
				return exitWith(ExitUsage, fmt.Errorf("unknown role %q (known: %s)", args[0], strings.Join(reg.Slugs(), ", ")))
			}
			ctx := context.Background()
			tags, _ := cmd.Flags().GetStringSlice("tag")
			switch args[1] {
			case "list":
				return a.memoryList(ctx, role)
			case "add":
				text := strings.TrimSpace(strings.Join(args[2:], " "))
				if text == "" {
					if text, err = readStdin(); err != nil {
						return exitWith(ExitUsage, err)
					}
				}
				if text == "" {
					return exitWith(ExitUsage, errors.New("nothing to add"))
				}
				e := memory.Entry{ID: memory.NewID(), Text: text, CreatedAt: time.Now().UTC(), Tags: tags}
				if err := role.Memory().Add(ctx, e); err != nil {
					return err
				}
				fmt.Println(e.ID)
				return nil
			case "remove":
				if len(args) < 3 {
					return exitWith(ExitUsage, errors.New("usage: water memory <role> remove <id>"))
				}
				return role.Memory().Remove(ctx, args[2])
			case "prune":
				mp, _ := a.memoryProvider()
				pr, ok := mp.(memory.Pruner)
				if !ok {
					return errors.New("memory provider does not support prune")
				}
				removed, err := pr.Prune(ctx, role.Slug)
				if err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "pruned %d entr%s\n", len(removed), plural(len(removed), "y", "ies"))
				return nil
			default:
				return exitWith(ExitUsage, fmt.Errorf("unknown memory action %q", args[1]))
			}
		},
	}
	c.Flags().StringSlice("tag", nil, "tag(s) for add")
	return c
}

func (a *App) memoryList(ctx context.Context, role *roles.Role) error {
	entries, err := role.Memory().Snapshot(ctx)
	if a.jsonMode() {
		out := map[string]any{"role": role.Slug, "entries": entries}
		if err != nil {
			out["error"] = err.Error()
		}
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	for _, e := range entries {
		tags := ""
		if len(e.Tags) > 0 {
			tags = " #" + strings.Join(e.Tags, " #")
		}
		fmt.Printf("%s %s%s\n  %s\n", surface.StyleAccent.Render(e.ID), surface.StyleDim.Render(e.CreatedAt.Format(time.RFC3339)), surface.StyleDim.Render(tags),
			strings.ReplaceAll(strings.TrimSpace(e.Text), "\n", "\n  "))
	}
	if len(entries) == 0 && err == nil {
		fmt.Fprintln(os.Stderr, surface.StyleDim.Render("(empty)"))
	}
	return err
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
