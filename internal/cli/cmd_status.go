package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"water/internal/backend"
	"water/internal/memory"
	"water/internal/surface"
)

type roleStatus struct {
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	Singleton    bool   `json:"singleton"`
	Orchestrator bool   `json:"orchestrator"`
	PersonaBlank bool   `json:"persona_blank"`
	Skills       int    `json:"skills"`
	MemEntries   int    `json:"memory_entries"`
	MemBytes     int    `json:"memory_bytes"`
}

func (a *App) statusCmd() *cobra.Command {
	var noProbe bool
	c := &cobra.Command{
		Use:   "status",
		Short: "Quick: discovered roles, selected backend, memory sizes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			reg, err := a.roleRegistry()
			if err != nil {
				return err
			}
			mp, _ := a.memoryProvider()
			var rows []roleStatus
			for _, r := range reg.All() {
				rs := roleStatus{Slug: r.Slug, Name: r.Name, Singleton: r.Singleton, Orchestrator: r.Orchestrator, PersonaBlank: r.Blank()}
				if r.Persona != nil {
					rs.Skills = len(r.Persona.Skills)
				}
				if sz, ok := mp.(memory.Sizer); ok {
					rs.MemEntries, rs.MemBytes, _ = sz.Size(r.Slug)
				}
				rows = append(rows, rs)
			}
			selected, reason := "", ""
			if !noProbe {
				if sel, err := a.selectBackend(context.Background()); err == nil {
					selected, reason = sel.Backend.Name(), sel.Reason
				} else {
					selected, reason = "(none)", err.Error()
				}
			} else {
				selected, reason = cfg.Backend.Preferred, "configured (not probed)"
			}
			if a.jsonMode() {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{
					"source": reg.Source(), "roles": rows, "backend": selected, "backend_reason": reason,
					"config": cfg.FilePath, "config_exists": cfg.FileExists, "leaked_keys": backend.LeakedKeys(),
				})
			}
			fmt.Printf("%s %s\n", surface.StyleDim.Render("roles   "), surface.StyleDim.Render("from "+reg.Source()))
			for _, r := range rows {
				tags := ""
				if r.Orchestrator {
					tags += surface.StyleAccent.Render(" orchestrator")
				}
				if r.Singleton {
					tags += surface.StyleDim.Render(" singleton")
				}
				persona := surface.StyleDim.Render("persona unwritten")
				if !r.PersonaBlank {
					persona = "persona written"
				}
				fmt.Printf("  %-8s %-8s %s · %d skill(s) · memory %d/%d B%s\n", surface.StyleBold.Render(r.Slug), r.Name, persona, r.Skills, r.MemEntries, r.MemBytes, tags)
			}
			fmt.Printf("%s %s %s\n", surface.StyleDim.Render("backend "), surface.StyleAccent.Render(selected), surface.StyleDim.Render(reason))
			fmt.Printf("%s %s\n", surface.StyleDim.Render("config  "), cfg.FilePath)
			if lk := backend.LeakedKeys(); len(lk) > 0 {
				fmt.Printf("%s %v exported — see `water doctor`\n", surface.StyleWarn.Render("warning "), lk)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&noProbe, "no-probe", false, "do not probe backend availability")
	return c
}
