// Package roles discovers role folders (extension point 3) and enforces the
// system invariants: unique slugs, exactly one singleton, an orchestrator.
package roles

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest is agents/<slug>/role.yaml.
type Manifest struct {
	Schema        int      `yaml:"schema"`
	Name          string   `yaml:"name"`
	Slug          string   `yaml:"slug"`
	RoleID        string   `yaml:"role_id"` // stable random UUID; identity anchor (Part 3A)
	Singleton     bool     `yaml:"singleton"`
	Orchestrator  bool     `yaml:"orchestrator"`
	Description   string   `yaml:"description"`
	SkillsEnabled []string `yaml:"skills_enabled"`

	// Part 8: per-role backend resolution. "" inherits the global default.
	Backend string `yaml:"backend"`
	Model   string `yaml:"model"`

	// Part 6.2: capability manifest the router/COO reads when assigning work.
	Capability Capability `yaml:"capability"`

	// Part 5.2: tool permissions. Absent block = no tools at all.
	Tools *ToolGrant `yaml:"tools"`
}

// Capability is the per-role manifest. Content must come from the role's own
// soul.md / experience.md, and descriptions must be distinct and concrete —
// the documented failure is a manager delegating to the wrong agent because
// descriptions overlap.
type Capability struct {
	Owns          []string `yaml:"owns"`
	RouteHereWhen []string `yaml:"route_here_when"`
	EscalatesWhen []string `yaml:"escalates_when"`
	Consumes      []string `yaml:"consumes"`
	Produces      []string `yaml:"produces"`
	DoesNot       []string `yaml:"does_not"`
}

// Empty reports whether no capability text was written.
func (c Capability) Empty() bool {
	return len(c.Owns)+len(c.RouteHereWhen)+len(c.EscalatesWhen)+len(c.Consumes)+len(c.Produces)+len(c.DoesNot) == 0
}

// Render produces the manifest block other roles (COO, CEO) read.
func (c Capability) Render(slug, name string) string {
	if c.Empty() {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "### %s (%s)\n", name, slug)
	section := func(h string, items []string) {
		if len(items) == 0 {
			return
		}
		sb.WriteString(h + ":\n")
		for _, it := range items {
			sb.WriteString("- " + strings.TrimSpace(it) + "\n")
		}
	}
	section("Owns", c.Owns)
	section("Route here when", c.RouteHereWhen)
	section("Escalates when", c.EscalatesWhen)
	section("Consumes", c.Consumes)
	section("Produces", c.Produces)
	section("Does not", c.DoesNot)
	return sb.String()
}

// ToolGrant is the role-scoped tools block (Part 5.2). Everything defaults to
// deny; roots are never inherited from the working directory.
type ToolGrant struct {
	Filesystem struct {
		Mode  string   `yaml:"mode"` // none | read-only | read-write
		Roots []string `yaml:"roots"`
	} `yaml:"filesystem"`
	Shell struct {
		Mode      string   `yaml:"mode"` // none | allowlist | confirm-each | unrestricted
		Allowlist []string `yaml:"allowlist"`
	} `yaml:"shell"`
	Network string `yaml:"network"` // none | (reserved)
	// Trace: "current-run" grants read-only access to evidence references in
	// the run the role is participating in (the COO's verification capability).
	Trace string `yaml:"trace"`
}

var slugRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// RoleIDRe is the accepted role_id shape (RFC 4122 UUID, lowercase).
var RoleIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ParseManifest decodes and validates a role.yaml. dirName is the folder the
// file was found in; slug must match it so discovery and addressing agree.
func ParseManifest(b []byte, dirName string) (Manifest, error) {
	var m Manifest
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("role.yaml: %w", err)
	}
	if m.Schema != 1 {
		return m, fmt.Errorf("role.yaml: unsupported schema %d (want 1)", m.Schema)
	}
	if strings.TrimSpace(m.Name) == "" {
		return m, fmt.Errorf("role.yaml: name is required")
	}
	if m.Slug == "" {
		m.Slug = dirName
	}
	if !slugRe.MatchString(m.Slug) {
		return m, fmt.Errorf("role.yaml: slug %q must match %s", m.Slug, slugRe)
	}
	if m.Slug != dirName {
		return m, fmt.Errorf("role.yaml: slug %q does not match folder %q", m.Slug, dirName)
	}
	if m.RoleID != "" && !RoleIDRe.MatchString(m.RoleID) {
		return m, fmt.Errorf("role.yaml: role_id %q is not a lowercase UUID", m.RoleID)
	}
	if m.Tools != nil {
		if err := validateToolGrant(m.Tools); err != nil {
			return m, fmt.Errorf("role.yaml: tools: %w", err)
		}
	}
	return m, nil
}

func validateToolGrant(t *ToolGrant) error {
	switch t.Filesystem.Mode {
	case "", "none", "read-only", "read-write":
	default:
		return fmt.Errorf("filesystem.mode %q must be none|read-only|read-write", t.Filesystem.Mode)
	}
	switch t.Shell.Mode {
	case "", "none", "allowlist", "confirm-each", "unrestricted":
	default:
		return fmt.Errorf("shell.mode %q must be none|allowlist|confirm-each|unrestricted", t.Shell.Mode)
	}
	switch t.Network {
	case "", "none":
	default:
		return fmt.Errorf("network %q: only \"none\" is supported in this build", t.Network)
	}
	switch t.Trace {
	case "", "none", "current-run":
	default:
		return fmt.Errorf("trace %q must be none|current-run", t.Trace)
	}
	return nil
}

// FS, SH and NET expose the grant to the tools package without an import
// cycle (tools must not import roles).
func (t *ToolGrant) FS() (string, []string) {
	if t == nil {
		return "none", nil
	}
	return t.Filesystem.Mode, t.Filesystem.Roots
}

func (t *ToolGrant) SH() (string, []string) {
	if t == nil {
		return "none", nil
	}
	return t.Shell.Mode, t.Shell.Allowlist
}

func (t *ToolGrant) NET() string {
	if t == nil {
		return "none"
	}
	return t.Network
}

func (t *ToolGrant) TR() string {
	if t == nil || t.Trace == "none" {
		return ""
	}
	return t.Trace
}
