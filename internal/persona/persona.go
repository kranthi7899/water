package persona

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"water/internal/identity"
)

// Persona is a role's always-on context plus its discovered skills.
type Persona struct {
	Slug       string
	Soul       Doc
	Experience Doc
	Skills     []Skill
	Index      *Index
}

// Identity carries the Part 3A verification inputs for a role's files.
type Identity struct {
	RoleID     string // from role.yaml; "" = unstamped role
	Key        []byte // machine keyring; nil = no signature checks
	RequireSig bool   // true for a real local agents dir once a keyring exists
}

// Load reads soul.md, experience.md, skills and the hidden index for roleDir.
// Missing soul/experience files are tolerated (treated as blank) so a role can
// be added as a bare folder with only role.yaml. Every file found is verified
// against id (role_id, content_hash, signature) and fails loudly on mismatch.
func Load(fsys fs.FS, roleDir, slug string, id Identity) (*Persona, error) {
	p := &Persona{Slug: slug}
	var err error
	if p.Soul, err = loadDoc(fsys, path.Join(roleDir, "soul.md"), id); err != nil {
		return nil, err
	}
	if p.Experience, err = loadDoc(fsys, path.Join(roleDir, "experience.md"), id); err != nil {
		return nil, err
	}
	if p.Skills, err = DiscoverSkills(fsys, roleDir); err != nil {
		return nil, err
	}
	for _, sk := range p.Skills {
		if _, err := loadDoc(fsys, path.Join(sk.Dir, "SKILL.md"), id); err != nil {
			return nil, err
		}
	}
	if p.Index, err = LoadIndex(fsys, roleDir); err != nil {
		return nil, err
	}
	return p, nil
}

func loadDoc(fsys fs.FS, name string, id Identity) (Doc, error) {
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
	if err := identity.Verify(d.Front, d.Body, id.RoleID, name, id.Key, id.RequireSig); err != nil {
		return Doc{}, err
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
