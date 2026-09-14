package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"water/internal/backend"
	"water/internal/config"
	"water/internal/memory"
	"water/internal/surface"
)

type roleStatus struct {
	Slug          string   `json:"slug"`
	Name          string   `json:"name"`
	RoleID        string   `json:"role_id"`
	Singleton     bool     `json:"singleton"`
	Orchestrator  bool     `json:"orchestrator"`
	PersonaBlank  bool     `json:"persona_blank"`
	Skills        int      `json:"skills"`
	MemEntries    int      `json:"memory_entries"`
	MemBytes      int      `json:"memory_bytes"`
	Backend       string   `json:"backend"`
	BackendReason string   `json:"backend_reason"`
	Model         string   `json:"model,omitempty"`
	Tools         []string `json:"tools,omitempty"`
}

func (a *App) statusCmd() *cobra.Command {
	var noProbe bool
	c := &cobra.Command{
		Use:   "status",
		Short: "Discovered roles, per-role backend resolution (and why), memory sizes",
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
			ctx := context.Background()
			selected, reason := cfg.Backend.Preferred, "configured (not probed)"
			res := map[string]roleResolution{}
			if !noProbe {
				if sel, err := a.selectBackend(ctx); err == nil {
					selected, reason = sel.Backend.Name(), sel.Reason
					if env, r, err := a.roleEnv(ctx, reg, sel); err == nil {
						_ = env
						res = r
					} else {
						reason += "; per-role: " + err.Error()
					}
				} else {
					selected, reason = "(none)", err.Error()
				}
			}
			var rows []roleStatus
			for _, r := range reg.All() {
				rs := roleStatus{Slug: r.Slug, Name: r.Name, RoleID: r.RoleID, Singleton: r.Singleton, Orchestrator: r.Orchestrator, PersonaBlank: r.Blank(), Model: r.Model}
				if r.Persona != nil {
					rs.Skills = len(r.Persona.Skills)
				}
				if sz, ok := mp.(memory.Sizer); ok {
					rs.MemEntries, rs.MemBytes, _ = sz.Size(r.Slug)
				}
				if rr, ok := res[r.Slug]; ok {
					rs.Backend, rs.BackendReason, rs.Tools = rr.Backend, rr.Reason, rr.Tools
				} else {
					rs.Backend, rs.BackendReason = selected, reason
					if r.Backend != "" {
						rs.Backend, rs.BackendReason = r.Backend, "role.yaml backend: (not probed)"
					}
				}
				rows = append(rows, rs)
			}
			if a.jsonMode() {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{
					"source": reg.Source(), "roles": rows, "backend": selected, "backend_reason": reason,
					"config": cfg.FilePath, "config_exists": cfg.FileExists, "leaked_keys": backend.LeakedKeys(),
					"router": cfg.Orchestration.Router, "checkpointer": cfg.Orchestration.Checkpointer, "keyring": a.keys() != nil,
					"budget": backend.LoadRateLimit(config.Home()),
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
				tools := ""
				if len(r.Tools) > 0 {
					tools = " · tools " + strings.Join(r.Tools, ",")
				}
				fmt.Printf("  %-8s %-8s %d skill(s) · memory %d/%d B%s%s\n", surface.StyleBold.Render(r.Slug), r.Name, r.Skills, r.MemEntries, r.MemBytes, tools, tags)
				fmt.Printf("           %s %s %s\n", surface.StyleDim.Render("backend"), surface.StyleAccent.Render(r.Backend), surface.StyleDim.Render(r.BackendReason))
			}
			fmt.Printf("%s %s %s\n", surface.StyleDim.Render("backend "), surface.StyleAccent.Render(selected), surface.StyleDim.Render(reason))
			fmt.Printf("%s %s · checkpointer %s · rounds %d · steps %d\n", surface.StyleDim.Render("router  "), cfg.Orchestration.Router, cfg.Orchestration.Checkpointer, cfg.Orchestration.MaxRounds, cfg.Orchestration.MaxSteps)
			fmt.Printf("%s %s\n", surface.StyleDim.Render("budget  "), backend.LoadRateLimit(config.Home()).Summary())
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
