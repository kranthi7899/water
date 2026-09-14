package persona

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// Persona is a role's always-on context plus its discovered skills.
type Persona struct {
	Slug       string
	Soul       Doc
	Experience Doc
	Skills     []Skill
	Index      *Index
}

// Load reads soul.md, experience.md, skills and the hidden index for roleDir.
// Missing soul/experience files are tolerated (treated as blank) so a role can
// be added as a bare folder with only role.yaml.
func Load(fsys fs.FS, roleDir, slug string) (*Persona, error) {
	p := &Persona{Slug: slug}
	var err error
	if p.Soul, err = loadDoc(fsys, path.Join(roleDir, "soul.md")); err != nil {
		return nil, err
	}
	if p.Experience, err = loadDoc(fsys, path.Join(roleDir, "experience.md")); err != nil {
		return nil, err
	}
	if p.Skills, err = DiscoverSkills(fsys, roleDir); err != nil {
		return nil, err
	}
	if p.Index, err = LoadIndex(fsys, roleDir); err != nil {
		return nil, err
	}
	return p, nil
}

func loadDoc(fsys fs.FS, name string) (Doc, error) {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Doc{Front: map[string]any{}}, nil
		}
		return Doc{}, err
	}
	d, err := ParseDoc(b)
	if err != nil {
		return Doc{}, fmt.Errorf("%s: %w", name, err)
	}
	return d, nil
}

// Blank reports whether both always-on files are still scaffolds.
func (p *Persona) Blank() bool { return p.Soul.IsBlank() && p.Experience.IsBlank() }

// Render assembles the always-on persona sections plus the selected skills
// into a system-prompt fragment. Memory and inbox are appended by the node,
// not here — this function sees only the role's own content.
func (p *Persona) Render(selected []Skill) string {
	var sb strings.Builder
	if s := p.Soul.Prose(); s != "" {
		sb.WriteString("# Soul\n\n" + s + "\n\n")
	}
	if e := p.Experience.Prose(); e != "" {
		sb.WriteString("# Experience\n\n" + e + "\n\n")
	}
	for _, sk := range selected {
		sb.WriteString("# Skill: " + sk.Name + "\n\n" + sk.Body + "\n\n")
	}
	return strings.TrimSpace(sb.String())
}
