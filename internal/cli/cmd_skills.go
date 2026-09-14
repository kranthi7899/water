package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"water/internal/config"
	"water/internal/surface"
)

// skillUsage is one skill's load history.
type skillUsage struct {
	Role       string    `json:"role"`
	Skill      string    `json:"skill"`
	Loads      int       `json:"loads"`
	LastLoaded time.Time `json:"last_loaded,omitempty"`
}

// skillsCmd is the deterministic half of Hermes's Curator, adapted: it reports
// which skills actually load, from traces and transcripts Water already
// writes. It never archives, consolidates or rewrites a skill — skills are
// signed identity files, and the frameworks' own rule ("a tool that never
// fires is a drop candidate") is applied by a person reading this report.
func (a *App) skillsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "skills [report]",
		Short: "Which skills each role actually loads, and which have never loaded (read-only)",
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
			usage := map[string]*skillUsage{}
			add := func(role string, skills []string, at time.Time) {
				for _, sk := range skills {
					k := role + "/" + sk
					u := usage[k]
					if u == nil {
						u = &skillUsage{Role: role, Skill: sk}
						usage[k] = u
					}
					u.Loads++
					if at.After(u.LastLoaded) {
						u.LastLoaded = at
					}
				}
			}
			calls := 0
			scan := func(pattern string, handle func(line []byte)) {
				files, _ := filepath.Glob(pattern)
				for _, f := range files {
					fh, err := os.Open(f)
					if err != nil {
						continue
					}
					sc := bufio.NewScanner(fh)
					sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
					for sc.Scan() {
						handle(sc.Bytes())
					}
					fh.Close()
				}
			}
			scan(filepath.Join(cfg.Telemetry.TraceDir, "*.jsonl"), func(line []byte) {
				if !strings.Contains(string(line), `"prompt_assembled"`) {
					return
				}
				var e struct {
					At     time.Time `json:"at"`
					Type   string    `json:"type"`
					Role   string    `json:"role"`
					Skills []string  `json:"skills"`
				}
				if json.Unmarshal(line, &e) == nil && e.Type == "prompt_assembled" {
					calls++
					add(e.Role, e.Skills, e.At)
				}
			})
			// Chat turns: transcripts record the skills of each assistant reply.
			// The role is the transcript's folder name, not a field on the line.
			roleDirs, _ := filepath.Glob(filepath.Join(config.Home(), "sessions", "*"))
			for _, dir := range roleDirs {
				role := filepath.Base(dir)
				scan(filepath.Join(dir, "*.jsonl"), func(line []byte) {
					if !strings.Contains(string(line), `"kind":"assistant"`) {
						return
					}
					var e struct {
						At     time.Time `json:"at"`
						Skills []string  `json:"skills"`
					}
					if json.Unmarshal(line, &e) == nil {
						calls++
						add(role, e.Skills, e.At)
					}
				})
			}
			type roleReport struct {
				Role        string       `json:"role"`
				Used        []skillUsage `json:"used"`
				NeverLoaded []string     `json:"never_loaded"`
			}
			var out []roleReport
			for _, r := range reg.All() {
				rr := roleReport{Role: r.Slug}
				if r.Persona != nil {
					for _, sk := range r.Persona.Skills {
						if u, ok := usage[r.Slug+"/"+sk.Slug]; ok {
							rr.Used = append(rr.Used, *u)
						} else {
							rr.NeverLoaded = append(rr.NeverLoaded, sk.Slug)
						}
					}
				}
				sort.Slice(rr.Used, func(i, j int) bool { return rr.Used[i].Loads > rr.Used[j].Loads })
				out = append(out, rr)
			}
			if a.jsonMode() {
				return printJSON(map[string]any{"prompts_scanned": calls, "roles": out})
			}
			fmt.Printf("%s %d assembled prompts in %s\n", surface.StyleDim.Render("scanned"), calls, cfg.Telemetry.TraceDir)
			for _, rr := range out {
				fmt.Printf("\n%s\n", surface.StyleBold.Render(strings.ToUpper(rr.Role)))
				for _, u := range rr.Used {
					fmt.Printf("  %-32s %4d load(s)  last %s\n", u.Skill, u.Loads, u.LastLoaded.Local().Format("2006-01-02 15:04"))
				}
				if len(rr.NeverLoaded) > 0 {
					fmt.Printf("  %s %s\n", surface.StyleWarn.Render("never loaded:"), strings.Join(rr.NeverLoaded, ", "))
				}
			}
			fmt.Printf("\n%s\n", surface.StyleDim.Render("Read-only. A skill that never loads is a review candidate: check its description triggers before dropping it. Water never archives or rewrites skills on its own."))
			return nil
		},
	}
	return c
}
