package roles

import (
	"water/internal/memory"
	"water/internal/persona"
)

// Role is a discovered role with its persona and a memory handle scoped to
// itself. Nodes receive a *Role and can reach nothing outside it.
type Role struct {
	Manifest
	Dir     string // path within the agents fs
	Persona *persona.Persona
	mem     memory.Scoped
}

// Memory returns this role's memory, scoped to r.Slug. No role argument is
// exposed anywhere on the returned value.
func (r *Role) Memory() memory.Scoped { return r.mem }

// Blank reports whether the persona is still Phase 1 scaffolding.
func (r *Role) Blank() bool { return r.Persona == nil || r.Persona.Blank() }
