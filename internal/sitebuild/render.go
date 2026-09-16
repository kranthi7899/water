package sitebuild

import (
	"embed"
	"fmt"
	"html/template"
	"strings"
	texttemplate "text/template"
)

//go:embed templates/*.tmpl
var templates embed.FS

// Size ceilings. tools.Service writes without a cap of its own, so the
// renderer is where a runaway plan is stopped.
const (
	MaxFileBytes  = 256 << 10
	MaxTotalBytes = 1 << 20
)

// roleCard is one of the four fixed role-agents, never model-supplied.
type roleCard struct {
	Name  string
	Blurb string
}

type block struct {
	ID      string
	Heading string
	Body    string
	Bullets []string
	Roles   []roleCard // set only for the role-agents block
}

type view struct {
	Title       string
	Description string
	Theme       SiteTheme
	Hero        block
	Blocks      []block
	Footer      block
}

// defaults keep the page complete when the model omits a slot. Order matches
// Slots; hero and footer are handled separately.
var defaults = map[string]block{
	SlotHero: {
		Heading: "Water",
		Body:    "A council of role-agents on the subscription you already pay for.",
	},
	SlotWhat: {
		Heading: "What Water is",
		Body:    "Water runs a small leadership council rather than a single assistant: four role-agents, each with a private persona and its own memory, orchestrated through a native state graph.",
	},
	SlotRoles: {
		Heading: "The four role-agents",
	},
	SlotMemory: {
		Heading: "Memory isolation",
		Body:    "Every role keeps its own memory and transcripts. No role can read another's, and nothing a role learns leaks sideways into a peer's context.",
	},
	SlotFlow: {
		Heading: "How a run flows",
		Body:    "The CEO frames the brief, the COO decomposes and assigns it, the specialists answer in their own voice, the COO verifies the deliverables, and the CEO alone writes the final decision.",
	},
	SlotSafety: {
		Heading: "Approved local actions",
		Body:    "Tools are deny-by-default and scoped per role. Paths resolve against explicitly declared roots, Water's own state is never reachable, and consequential actions wait for a person to approve them.",
	},
	SlotDemo: {
		Heading: "See it run",
		Body:    "Point Water at a brief and watch the council work through it.",
	},
	SlotFooter: {},
}

var roleNames = []struct{ slug, name string }{
	{"ceo", "CEO"},
	{"coo", "COO"},
	{"cto", "CTO"},
	{"design", "Design"},
}

var roleBlurbs = map[string]string{
	"ceo":    "Frames the intent, adjudicates disagreement, and is the only role that writes the final answer.",
	"coo":    "Decomposes the brief into assignments and verifies what comes back before it reaches the CEO.",
	"cto":    "Judges technical feasibility, implementation cost, and what the constraints actually permit.",
	"design": "Owns experience, layout, copy hierarchy, and the risks a user will feel first.",
}

// Render turns a plan into the files to write. Paths are fixed here and never
// come from the model. Every model string reaches the page through a template
// action, so it is escaped for its destination.
func Render(p SitePlan, name string) ([]GeneratedFile, error) {
	v := build(p)

	idx, err := template.New("index.html.tmpl").ParseFS(templates, "templates/index.html.tmpl")
	if err != nil {
		return nil, err
	}
	css, err := texttemplate.New("styles.css.tmpl").ParseFS(templates, "templates/styles.css.tmpl")
	if err != nil {
		return nil, err
	}
	readme, err := texttemplate.New("README.md.tmpl").ParseFS(templates, "templates/README.md.tmpl")
	if err != nil {
		return nil, err
	}

	files := make([]GeneratedFile, 0, 3)
	for _, f := range []struct {
		path string
		exec func(*strings.Builder) error
	}{
		{"index.html", func(sb *strings.Builder) error { return idx.Execute(sb, v) }},
		{"styles.css", func(sb *strings.Builder) error { return css.Execute(sb, v) }},
		{"README.md", func(sb *strings.Builder) error { return readme.Execute(sb, v) }},
	} {
		var sb strings.Builder
		if err := f.exec(&sb); err != nil {
			return nil, fmt.Errorf("rendering %s: %w", f.path, err)
		}
		files = append(files, GeneratedFile{Path: f.path, Content: sb.String()})
	}

	total := 0
	for _, f := range files {
		if len(f.Content) > MaxFileBytes {
			return nil, fmt.Errorf("%s is %d bytes, over the %d byte limit", f.Path, len(f.Content), MaxFileBytes)
		}
		total += len(f.Content)
	}
	if total > MaxTotalBytes {
		return nil, fmt.Errorf("site is %d bytes, over the %d byte limit", total, MaxTotalBytes)
	}
	return files, nil
}

// build resolves the plan's sections onto the template's fixed slots, filling
// gaps with built-in copy and appending anything unrecognised after the safety
// model so no content is silently dropped.
func build(p SitePlan) view {
	slot := func(id string) block {
		b := defaults[id]
		b.ID = id
		s, ok := p.Section(id)
		if !ok {
			return b
		}
		if s.Heading != "" {
			b.Heading = s.Heading
		}
		if s.Body != "" {
			b.Body = s.Body
		}
		b.Bullets = s.Bullets
		return b
	}

	known := map[string]bool{}
	for _, id := range Slots {
		known[id] = true
	}

	roles := slot(SlotRoles)
	cards := make([]roleCard, 0, len(roleNames))
	for i, r := range roleNames {
		blurb := roleBlurbs[r.slug]
		if i < len(roles.Bullets) && roles.Bullets[i] != "" {
			blurb = roles.Bullets[i]
		}
		cards = append(cards, roleCard{Name: r.name, Blurb: blurb})
	}
	roles.Roles, roles.Bullets = cards, nil

	blocks := []block{slot(SlotWhat), roles, slot(SlotMemory), slot(SlotFlow), slot(SlotSafety)}
	for _, s := range p.Sections {
		if known[s.ID] {
			continue
		}
		heading := s.Heading
		if heading == "" {
			heading = s.ID
		}
		blocks = append(blocks, block{ID: s.ID, Heading: heading, Body: s.Body, Bullets: s.Bullets})
	}
	blocks = append(blocks, slot(SlotDemo))

	title := p.Title
	hero := slot(SlotHero)
	if hero.Heading == "" {
		hero.Heading = title
	}
	return view{
		Title:       title,
		Description: p.Description,
		Theme:       p.Theme,
		Hero:        hero,
		Blocks:      blocks,
		Footer:      slot(SlotFooter),
	}
}
