package persona

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Skill is a contextually loaded reasoning procedure (skills/<slug>/SKILL.md).
//
// The file format matches Anthropic's SKILL.md schema exactly (Part 4.4):
// required frontmatter `name` and `description`; optional progressive
// disclosure via references/, scripts/, assets/. Water may extend the
// frontmatter with optional keys (role_id, content_hash, keywords) but every
// file stays parseable by a standard reader.
type Skill struct {
	Slug        string
	Name        string
	Description string
	Keywords    []string
	Body        string
	Dir         string   // skills/<slug>
	Resources   []string // references/, scripts/, assets/ entries present
}

// Schema limits from the SKILL.md specification.
const (
	SkillNameMax        = 64
	SkillDescriptionMax = 1024
	SkillBodyMaxLines   = 500
)

var (
	skillNameRe  = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	angleTagRe   = regexp.MustCompile(`<[A-Za-z/!?][^<>]*>`)
	reservedName = []string{"anthropic", "claude"}
)

// ErrSkillSchema is the base error for every SKILL.md validation failure.
var ErrSkillSchema = errors.New("SKILL.md schema violation")

// ValidateSkill enforces the schema: name and description present and within
// caps, name lowercase/numbers/hyphens with reserved words refused, body
// under the line cap, no XML/angle-bracket tags anywhere in the file.
func ValidateSkill(sk Skill, rawFront map[string]any) error {
	var problems []string
	name, _ := rawFront["name"].(string)
	desc, _ := rawFront["description"].(string)
	name, desc = strings.TrimSpace(name), strings.TrimSpace(desc)
	switch {
	case name == "":
		problems = append(problems, "name is required")
	case len(name) > SkillNameMax:
		problems = append(problems, fmt.Sprintf("name exceeds %d characters", SkillNameMax))
	case !skillNameRe.MatchString(name):
		problems = append(problems, "name must be lowercase letters, numbers and single hyphens")
	}
	for _, r := range reservedName {
		if name == r || strings.HasPrefix(name, r+"-") || strings.HasSuffix(name, "-"+r) || strings.Contains(name, "-"+r+"-") {
			problems = append(problems, fmt.Sprintf("name may not contain reserved word %q", r))
		}
	}
	if name != "" && sk.Slug != "" && name != sk.Slug {
		problems = append(problems, fmt.Sprintf("name %q must equal its directory name %q", name, sk.Slug))
	}
	switch {
	case desc == "":
		problems = append(problems, "description is required")
	case len(desc) > SkillDescriptionMax:
		problems = append(problems, fmt.Sprintf("description exceeds %d characters", SkillDescriptionMax))
	}
	if strings.HasPrefix(strings.ToLower(desc), "i ") || strings.HasPrefix(strings.ToLower(desc), "you ") {
		problems = append(problems, "description must be written in the third person")
	}
	if n := strings.Count(strings.TrimRight(sk.Body, "\n"), "\n") + 1; sk.Body != "" && n > SkillBodyMaxLines {
		problems = append(problems, fmt.Sprintf("body is %d lines; keep it under %d and move detail to references/", n, SkillBodyMaxLines))
	}
	if m := angleTagRe.FindString(sk.Body + "\n" + desc + "\n" + name); m != "" {
		problems = append(problems, fmt.Sprintf("angle-bracket tag %q is not allowed", m))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w in %s: %s", ErrSkillSchema, sk.Slug, strings.Join(problems, "; "))
	}
	return nil
}

// DiscoverSkills lists skills under <roleDir>/skills. A missing directory is
// not an error. Every SKILL.md found must validate; a malformed skill fails
// the role loudly rather than silently never triggering.
func DiscoverSkills(fsys fs.FS, roleDir string) ([]Skill, error) {
	dir := path.Join(roleDir, "skills")
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sdir := path.Join(dir, e.Name())
		b, err := fs.ReadFile(fsys, path.Join(sdir, "SKILL.md"))
		if err != nil {
			continue
		}
		doc, err := ParseDoc(b)
		if err != nil {
			return nil, fmt.Errorf("%s/SKILL.md: %w", sdir, err)
		}
		sk := Skill{Slug: e.Name(), Name: e.Name(), Body: doc.Prose(), Dir: sdir}
		if v, ok := doc.Front["name"].(string); ok {
			sk.Name = strings.TrimSpace(v)
		}
		if v, ok := doc.Front["description"].(string); ok {
			sk.Description = strings.TrimSpace(v)
		}
		switch kw := doc.Front["keywords"].(type) {
		case []any:
			for _, k := range kw {
				if s, ok := k.(string); ok {
					sk.Keywords = append(sk.Keywords, strings.ToLower(s))
				}
			}
		case string:
			for _, k := range strings.Split(kw, ",") {
				sk.Keywords = append(sk.Keywords, strings.ToLower(strings.TrimSpace(k)))
			}
		}
		for _, res := range []string{"references", "scripts", "assets"} {
			if _, err := fs.Stat(fsys, path.Join(sdir, res)); err == nil {
				sk.Resources = append(sk.Resources, res)
			}
		}
		if err := ValidateSkill(sk, doc.Front); err != nil {
			return nil, err
		}
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

// SkillSelector decides which discovered skills a given task calls for.
type SkillSelector interface {
	Name() string
	Select(task string, skills []Skill) []Skill
}

// KeywordSelector picks a skill when any of its keywords, its slug, or its
// name appears in the task text. Kept for tests and as the simplest baseline.
type KeywordSelector struct{}

func (KeywordSelector) Name() string { return "keyword" }

func (KeywordSelector) Select(task string, skills []Skill) []Skill {
	t := strings.ToLower(task)
	var out []Skill
	for _, s := range skills {
		terms := append([]string{strings.ToLower(s.Slug), strings.ToLower(s.Name)}, s.Keywords...)
		for _, term := range terms {
			if term != "" && strings.Contains(t, term) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// DescriptionSelector scores each skill by overlap between the task text and
// the skill's description, name, and keywords (the "trigger" vocabulary the
// SKILL.md schema asks authors to invest in). Keywords and name hits weigh
// more than description words; a skill is selected when its score clears
// MinScore, and at most MaxSkills are returned, best first. This is the
// default once real skills exist (Part 4.4: revisit the naive selector).
type DescriptionSelector struct {
	MinScore  float64 // default 2
	MaxSkills int     // default 3
}

func (DescriptionSelector) Name() string { return "description" }

var stopwords = map[string]bool{"the": true, "a": true, "an": true, "and": true, "or": true, "of": true, "to": true, "in": true, "on": true, "for": true, "is": true, "are": true, "be": true, "it": true, "this": true, "that": true, "with": true, "as": true, "by": true, "at": true, "from": true, "when": true, "use": true, "used": true, "into": true, "than": true, "not": true, "its": true, "any": true, "one": true, "who": true, "what": true, "how": true, "which": true, "about": true, "before": true, "after": true, "should": true, "would": true, "can": true, "has": true, "have": true, "will": true, "their": true, "they": true, "them": true, "you": true, "your": true, "our": true, "we": true, "so": true, "if": true, "but": true, "more": true, "most": true, "such": true, "same": true, "other": true, "some": true, "only": true, "each": true, "every": true, "all": true, "no": true, "may": true, "just": true, "then": true, "there": true, "here": true, "also": true, "been": true, "being": true, "was": true, "were": true, "do": true, "does": true, "did": true, "done": true, "over": true, "under": true, "up": true, "out": true, "off": true}

func tokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '\'')
	}) {
		f = strings.Trim(f, "'")
		if len(f) < 3 || stopwords[f] {
			continue
		}
		out[stem(f)] = true
	}
	return out
}

// stem is a deliberately tiny suffix stripper: enough to match "estimates"
// with "estimate" and "forecasting" with "forecast", nothing more.
func stem(w string) string {
	for _, suf := range []string{"ations", "ation", "ings", "ing", "ies", "ers", "er", "ed", "es", "s", "ly"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 3 {
			return w[:len(w)-len(suf)]
		}
	}
	return w
}

func (d DescriptionSelector) Select(task string, skills []Skill) []Skill {
	min := d.MinScore
	if min <= 0 {
		min = 2
	}
	max := d.MaxSkills
	if max <= 0 {
		max = 3
	}
	tt := tokens(task)
	type scored struct {
		sk    Skill
		score float64
	}
	var all []scored
	for _, s := range skills {
		score := 0.0
		for w := range tokens(s.Description) {
			if tt[w] {
				score += 1
			}
		}
		for w := range tokens(strings.ReplaceAll(s.Name, "-", " ")) {
			if tt[w] {
				score += 1.5
			}
		}
		lt := strings.ToLower(task)
		for _, k := range s.Keywords {
			if k != "" && strings.Contains(lt, k) {
				score += 2
			}
		}
		if strings.Contains(lt, strings.ToLower(s.Slug)) || strings.Contains(lt, strings.ToLower(strings.ReplaceAll(s.Slug, "-", " "))) {
			score += 3
		}
		if score >= min {
			all = append(all, scored{s, score})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })
	var out []Skill
	for i, s := range all {
		if i >= max {
			break
		}
		out = append(out, s.sk)
	}
	return out
}

// Selectors lists available selector implementations by name.
func Selectors() map[string]SkillSelector {
	return map[string]SkillSelector{"keyword": KeywordSelector{}, "description": DescriptionSelector{}}
}
