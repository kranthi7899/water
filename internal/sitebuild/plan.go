// Package sitebuild turns a council's final synthesis into a small static
// website. The model decides the words; Go decides the files. Nothing the
// model writes ever becomes a path, a command, or unescaped markup.
package sitebuild

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// SitePlan is the structured site the CEO's synthesis is reduced to. It
// carries no file paths: the renderer owns those, so a model can never name a
// destination.
type SitePlan struct {
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Sections    []Section `json:"sections"`
	Theme       SiteTheme `json:"theme"`
}

// Section is one block of page copy.
type Section struct {
	ID      string   `json:"id"` // slug; matches a template slot when known
	Heading string   `json:"heading"`
	Body    string   `json:"body"`
	Bullets []string `json:"bullets"`
}

// SiteTheme is the colour scheme. Every colour is validated before it reaches
// the stylesheet.
type SiteTheme struct {
	Name       string `json:"name"`
	Background string `json:"background"`
	Foreground string `json:"foreground"`
	Accent     string `json:"accent"`
}

// GeneratedFile is one rendered file. Path is always relative and always
// produced by the renderer, never by the model.
type GeneratedFile struct {
	Path    string
	Content string
}

// Slot IDs the renderer knows how to place. Unknown sections still render,
// appended after the safety model.
const (
	SlotHero        = "hero"
	SlotWhat        = "what-is-water"
	SlotRoles       = "role-agents"
	SlotMemory      = "memory-isolation"
	SlotFlow        = "orchestration-flow"
	SlotSafety      = "safety-model"
	SlotDemo        = "demo"
	SlotFooter      = "footer"
	maxSections     = 24
	maxBullets      = 12
	maxTextBytes    = 4000
	maxHeadingBytes = 200
)

// Slots lists the template's fixed blocks in page order.
var Slots = []string{SlotHero, SlotWhat, SlotRoles, SlotMemory, SlotFlow, SlotSafety, SlotDemo, SlotFooter}

const extractSystem = `You are converting a completed council decision into the content of a single
static web page. This is a formatting step: express the decision that was already made. Do not
introduce new claims, and do not describe your own reasoning.

Respond with ONLY a single JSON object, no surrounding prose or code fences, matching this shape:
{"title": "", "description": "", "sections": [{"id": "", "heading": "", "body": "", "bullets": []}], "theme": {"name": "", "background": "", "foreground": "", "accent": ""}}

- "title" is the page title, at most a short line. "description" is one sentence for the meta tag.
- Provide one section for each of these ids, in this order: hero, what-is-water, role-agents,
  memory-isolation, orchestration-flow, safety-model, demo, footer.
- Under "role-agents" put exactly four bullets, one per role, in the order CEO, COO, CTO, Design.
- "body" is prose, one short paragraph. "bullets" may be empty when a section needs no list.
- Every theme colour must be a hex value of the form #rrggbb. "name" is a short label for the scheme.
- Write plain text only: no HTML, no markdown syntax, no links, no file paths.`

// BuildPrompt renders the extraction prompt for one brief and its synthesis.
// The instructions travel in the prompt body because the extraction runs as an
// ordinary single turn for the orchestrator role, which assembles its own
// system prompt from its persona.
func BuildPrompt(brief, final string) string {
	var sb strings.Builder
	sb.WriteString(extractSystem)
	sb.WriteString("\n\n")
	fmt.Fprintf(&sb, "<brief>%s</brief>\n\n<final_synthesis>%s</final_synthesis>\n",
		strings.TrimSpace(brief), strings.TrimSpace(final))
	return sb.String()
}

// ParsePlan extracts the site plan from a backend response, tolerant of a
// surrounding fence or stray prose, then normalises it. A plan that cannot be
// parsed is an error: nothing is guessed from prose.
func ParsePlan(text string) (SitePlan, error) {
	text = strings.TrimSpace(text)
	if i, j := strings.IndexByte(text, '{'), strings.LastIndexByte(text, '}'); i >= 0 && j > i {
		text = text[i : j+1]
	}
	var p SitePlan
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		return SitePlan{}, err
	}
	if strings.TrimSpace(p.Title) == "" {
		return SitePlan{}, fmt.Errorf("plan has no title")
	}
	if len(p.Sections) == 0 {
		return SitePlan{}, fmt.Errorf("plan has no sections")
	}
	return Normalize(p), nil
}

var (
	hexColor  = regexp.MustCompile(`^#[0-9a-fA-F]{3,8}$`)
	nonSlug   = regexp.MustCompile(`[^a-z0-9]+`)
	defaultBg = "#050505"
	defaultFg = "#E8E4D0"
	defaultAc = "#D00000"
)

// Normalize makes a plan safe and deterministic to render: text is stripped of
// control characters and clamped, ids are slugified and de-duplicated, and any
// colour that is not a plain hex value is replaced with a built-in default.
func Normalize(p SitePlan) SitePlan {
	p.Title = clamp(clean(p.Title), maxHeadingBytes)
	p.Description = clamp(clean(p.Description), maxHeadingBytes)

	if len(p.Sections) > maxSections {
		p.Sections = p.Sections[:maxSections]
	}
	seen := map[string]int{}
	out := make([]Section, 0, len(p.Sections))
	for _, s := range p.Sections {
		id := slug(s.ID)
		if id == "" {
			id = slug(s.Heading)
		}
		if id == "" {
			id = "section"
		}
		seen[id]++
		if n := seen[id]; n > 1 {
			id = fmt.Sprintf("%s-%d", id, n)
		}
		s.ID = id
		s.Heading = clamp(clean(s.Heading), maxHeadingBytes)
		s.Body = clamp(clean(s.Body), maxTextBytes)
		if len(s.Bullets) > maxBullets {
			s.Bullets = s.Bullets[:maxBullets]
		}
		bs := make([]string, 0, len(s.Bullets))
		for _, b := range s.Bullets {
			if b = clamp(clean(b), maxTextBytes); b != "" {
				bs = append(bs, b)
			}
		}
		s.Bullets = bs
		out = append(out, s)
	}
	p.Sections = out

	p.Theme.Name = clamp(slug(p.Theme.Name), 64)
	if p.Theme.Name == "" {
		p.Theme.Name = "water-base"
	}
	p.Theme.Background = color(p.Theme.Background, defaultBg)
	p.Theme.Foreground = color(p.Theme.Foreground, defaultFg)
	p.Theme.Accent = color(p.Theme.Accent, defaultAc)
	return p
}

// Section returns the section with the given id.
func (p SitePlan) Section(id string) (Section, bool) {
	for _, s := range p.Sections {
		if s.ID == id {
			return s, true
		}
	}
	return Section{}, false
}

func color(v, fallback string) string {
	if hexColor.MatchString(strings.TrimSpace(v)) {
		return strings.TrimSpace(v)
	}
	return fallback
}

// clean drops control characters, which have no place in page copy and can
// corrupt a terminal when the same text is echoed in the approval plan.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
}

func clamp(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}

func slug(s string) string {
	s = nonSlug.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	return strings.Trim(s, "-")
}
