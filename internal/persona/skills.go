package persona

import (
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Skill is a contextually loaded reasoning procedure (skills/<slug>/SKILL.md).
type Skill struct {
	Slug        string
	Name        string
	Description string
	Keywords    []string
	Body        string
}

// DiscoverSkills lists skills under <roleDir>/skills. A missing directory is
// not an error: Phase 1 ships zero skills.
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
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		doc, err := ParseDoc(b)
		if err != nil {
			return nil, err
		}
		sk := Skill{Slug: e.Name(), Name: e.Name(), Body: doc.Prose()}
		if v, ok := doc.Front["name"].(string); ok {
			sk.Name = v
		}
		if v, ok := doc.Front["description"].(string); ok {
			sk.Description = v
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
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

// SkillSelector decides which discovered skills a given task calls for.
// The default is naive keyword/frontmatter matching; a retrieval-based
// selector can replace it later by implementing this interface.
type SkillSelector interface {
	Name() string
	Select(task string, skills []Skill) []Skill
}

// KeywordSelector picks a skill when any of its keywords, its slug, or its
// name appears in the task text.
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
