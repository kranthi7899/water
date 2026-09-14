package roles

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"water/internal/memory"
	"water/internal/persona"
)

// Registry is the validated set of discovered roles.
type Registry struct {
	roles  []*Role
	bySlug map[string]*Role
	source string
}

// LoadError aggregates every validation failure so the user sees all of them.
type LoadError struct{ Problems []string }

func (e *LoadError) Error() string {
	return "role loading failed:\n  - " + strings.Join(e.Problems, "\n  - ")
}

// ErrNoRoles is returned when the agents tree contains no valid role.
var ErrNoRoles = errors.New("no roles discovered")

// LoadOptions carry the identity-verification inputs (Part 3A).
type LoadOptions struct {
	// Key is the machine keyring (nil = no signature verification).
	Key []byte
	// RequireSignatures makes unsigned stamped files fail. Set when roles come
	// from a real local agents directory and a keyring exists.
	RequireSignatures bool
}

// Load scans every top-level directory of src for a role.yaml, validates the
// set, and binds each role's memory. It fails LOUDLY at startup if two roles
// declare singleton, a slug collides, a manifest is invalid, a persona file's
// identity does not match its folder, or no orchestrator exists.
func Load(src persona.Source, mem memory.Provider) (*Registry, error) {
	return LoadWith(src, mem, LoadOptions{})
}

// LoadWith is Load with identity options.
func LoadWith(src persona.Source, mem memory.Provider, opts LoadOptions) (*Registry, error) {
	fsys := src.FS()
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("reading agents tree (%s): %w", src.Name(), err)
	}
	reg := &Registry{bySlug: map[string]*Role{}, source: src.Name()}
	var problems []string
	var singletons, orchestrators []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		raw, err := fs.ReadFile(fsys, e.Name()+"/role.yaml")
		if err != nil {
			continue // a folder without role.yaml is not a role
		}
		m, err := ParseManifest(raw, e.Name())
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", e.Name(), err))
			continue
		}
		if _, dup := reg.bySlug[m.Slug]; dup {
			problems = append(problems, fmt.Sprintf("slug %q declared by more than one folder", m.Slug))
			continue
		}
		p, err := persona.Load(fsys, e.Name(), m.Slug, persona.Identity{RoleID: m.RoleID, Key: opts.Key, RequireSig: opts.RequireSignatures})
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: persona: %v", e.Name(), err))
			continue
		}
		r := &Role{Manifest: m, Dir: e.Name(), Persona: p}
		if mem != nil {
			r.mem = memory.Bind(mem, m.Slug)
		}
		if m.Singleton {
			singletons = append(singletons, m.Slug)
		}
		if m.Orchestrator {
			orchestrators = append(orchestrators, m.Slug)
		}
		reg.roles = append(reg.roles, r)
		reg.bySlug[m.Slug] = r
	}
	// role_ids must be unique across roles: a duplicated id means a copied folder.
	seenID := map[string]string{}
	for _, r := range reg.roles {
		if r.RoleID == "" {
			continue
		}
		if other, dup := seenID[r.RoleID]; dup {
			problems = append(problems, fmt.Sprintf("roles %s and %s share role_id %s — one folder was copied from the other", other, r.Slug, r.RoleID))
		}
		seenID[r.RoleID] = r.Slug
	}
	if len(singletons) > 1 {
		problems = append(problems, fmt.Sprintf("exactly one role may declare singleton: true; found %v", singletons))
	}
	if len(reg.roles) > 0 && len(orchestrators) == 0 {
		problems = append(problems, "no role declares orchestrator: true; the graph has no entry point")
	}
	if len(problems) > 0 {
		return nil, &LoadError{Problems: problems}
	}
	if len(reg.roles) == 0 {
		return nil, fmt.Errorf("%w in %s", ErrNoRoles, src.Name())
	}
	sort.SliceStable(reg.roles, func(i, j int) bool {
		// orchestrator first, then alphabetical: stable, predictable listings
		if reg.roles[i].Orchestrator != reg.roles[j].Orchestrator {
			return reg.roles[i].Orchestrator
		}
		return reg.roles[i].Slug < reg.roles[j].Slug
	})
	return reg, nil
}

// Source names where the roles were loaded from.
func (r *Registry) Source() string { return r.source }

// All returns roles, orchestrator first.
func (r *Registry) All() []*Role { return append([]*Role(nil), r.roles...) }

// Get looks up a role by slug.
func (r *Registry) Get(slug string) (*Role, bool) {
	x, ok := r.bySlug[strings.ToLower(strings.TrimSpace(slug))]
	return x, ok
}

// Slugs returns slugs in listing order.
func (r *Registry) Slugs() []string {
	out := make([]string, 0, len(r.roles))
	for _, x := range r.roles {
		out = append(out, x.Slug)
	}
	return out
}

// Orchestrator returns the singleton orchestrator role (the CEO).
func (r *Registry) Orchestrator() *Role {
	for _, x := range r.roles {
		if x.Orchestrator && x.Singleton {
			return x
		}
	}
	for _, x := range r.roles {
		if x.Orchestrator {
			return x
		}
	}
	return nil
}

// Delegates returns every non-orchestrator role, in listing order.
func (r *Registry) Delegates() []*Role {
	var out []*Role
	for _, x := range r.roles {
		if !x.Orchestrator {
			out = append(out, x)
		}
	}
	return out
}

// OrchestratorSlugs is the set handed to orchestrator.NewState so FinalOutput
// writes can be authorised at runtime.
func (r *Registry) OrchestratorSlugs() []string {
	var out []string
	for _, x := range r.roles {
		if x.Orchestrator {
			out = append(out, x.Slug)
		}
	}
	return out
}
