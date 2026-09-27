// Package workspaces loads twins/<id>/workspaces/*.yaml (docs/slices/UI.md
// Phase 1a, "data infrastructure"): one file per UI workspace, each naming
// a template, a primary data source and deterministic membership rules —
// literal lists of ids/keys a record must match one of, never a model's
// guess. internal/recordlinks' InWorkspace evaluates those rules against a
// record and writes the in_workspace edge (store.LinkInWorkspace) for
// every match.
//
// Loading is fail-loudly, exactly like twins.Load and
// decisions.LoadRegistry: a bad id, template or source stops daemon
// startup, since a workspace the UI cannot trust is worse than none at
// all. A missing twins/<id>/workspaces directory is an empty registry, not
// an error — most twins (ceo-demo, a counterparty twin) have none of their
// own.
package workspaces

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"water/internal/store"
)

// SpecSource is the store.Meta.Source a spec-loaded workspace row carries
// (Sync), distinguishing it from a hand-created, "ui"-sourced workspace
// (Slice V, before this package existed).
const SpecSource = "workspace_spec"

// idRe is a workspace id's shape (docs/slices/UI.md Phase 1a).
var idRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// validTemplates are the closed set of workspace kinds the UI knows how to
// render.
var validTemplates = map[string]bool{
	"project":   true,
	"finance":   true,
	"clients":   true,
	"people":    true,
	"ideas":     true,
	"research":  true,
	"marketing": true,
}

// sourceRe is the closed set of source kinds a workspace may declare:
// linear_team:<KEY>, company_finance, company_customers, roster, research,
// or github:<owner/repo>. internal/dashboards' own source check reuses
// ValidSource for the source kinds it shares with workspaces.
var sourceRe = regexp.MustCompile(`^(linear_team:[A-Z][A-Z0-9]*|company_finance|company_customers|roster|research|github:[\w.-]+/[\w.-]+)$`)

// ValidSource reports whether s is one of the closed workspace source
// kinds.
func ValidSource(s string) bool { return sourceRe.MatchString(s) }

// ClientsRule is the one membership field with two YAML shapes: `clients:
// all` (every roster client) or `clients: [id, ...]` (only these).
type ClientsRule struct {
	All  bool
	List []string
}

// UnmarshalYAML accepts either the scalar "all" or a sequence of client
// ids; anything else is a validation error at load time, not a silently
// empty rule.
func (c *ClientsRule) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var s string
		if err := n.Decode(&s); err != nil {
			return err
		}
		if s != "all" {
			return fmt.Errorf("clients: %q must be \"all\" or a list of client ids", s)
		}
		c.All = true
		return nil
	}
	var list []string
	if err := n.Decode(&list); err != nil {
		return fmt.Errorf("clients: must be \"all\" or a list of client ids: %w", err)
	}
	c.List = list
	return nil
}

// Matches reports whether clientID belongs under this rule.
func (c ClientsRule) Matches(clientID string) bool {
	if clientID == "" {
		return false
	}
	if c.All {
		return true
	}
	return contains(c.List, clientID)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Candidate is the subset of a record's identity that membership rules
// match against. A caller (internal/recordlinks) fills in only the field
// that applies to the record kind it's checking (an issue names its team
// key; a roster client names its own id; ...); the zero value of a field
// never matches anything.
type Candidate struct {
	TeamKey      string // a Linear issue's team key, e.g. "WAT"
	ProjectID    string // a roster project id
	DecisionType string // a decision card's type id
	ClientID     string // a roster client id
	VendorID     string // a roster vendor id
}

// MembershipRules are the deterministic rules — never a guess — that
// decide whether a record belongs to a workspace. Every field is a literal
// list of ids/keys the record must match one of. An empty rule set matches
// nothing: a domain workspace that has no rules yet (Phase 1a's `people`
// and `ideas` placeholders) simply gets no automatic edges until a later
// phase adds one.
type MembershipRules struct {
	Projects      []string    `yaml:"projects,omitempty"`
	Teams         []string    `yaml:"teams,omitempty"`
	DecisionTypes []string    `yaml:"decision_types,omitempty"`
	Clients       ClientsRule `yaml:"clients,omitempty"`
	Vendors       []string    `yaml:"vendors,omitempty"`
}

// Matches reports whether c belongs under these rules.
func (m MembershipRules) Matches(c Candidate) bool {
	switch {
	case c.TeamKey != "" && contains(m.Teams, c.TeamKey):
		return true
	case c.ProjectID != "" && contains(m.Projects, c.ProjectID):
		return true
	case c.DecisionType != "" && contains(m.DecisionTypes, c.DecisionType):
		return true
	case c.ClientID != "" && m.Clients.Matches(c.ClientID):
		return true
	case c.VendorID != "" && contains(m.Vendors, c.VendorID):
		return true
	default:
		return false
	}
}

// Spec is one twins/<id>/workspaces/<id>.yaml file, fully parsed and
// validated.
type Spec struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Template    string `yaml:"template"`
	Source      string `yaml:"source"`
	Description string `yaml:"description,omitempty"`

	Projects      []string    `yaml:"projects,omitempty"`
	Teams         []string    `yaml:"teams,omitempty"`
	DecisionTypes []string    `yaml:"decision_types,omitempty"`
	Clients       ClientsRule `yaml:"clients,omitempty"`
	Vendors       []string    `yaml:"vendors,omitempty"`

	File string `yaml:"-"`
	// SpecHash is a sha256 of the raw YAML file's bytes, filled in by
	// ParseSpec.
	SpecHash string `yaml:"-"`
}

// Rules is s's own membership rules, as a MembershipRules value a caller
// can check a Candidate against directly (Registry.MatchingSpecs already
// does this for every loaded spec; exported for a caller that has one Spec
// in hand instead of a whole Registry).
func (s Spec) Rules() MembershipRules {
	return MembershipRules{Projects: s.Projects, Teams: s.Teams, DecisionTypes: s.DecisionTypes, Clients: s.Clients, Vendors: s.Vendors}
}

// Validate checks id, name, template and source against the closed rules
// Phase 1a defines. Unknown YAML keys are already rejected by ParseSpec's
// strict decode before Validate ever runs.
func (s Spec) Validate() error {
	switch {
	case s.ID == "":
		return fmt.Errorf("id is required")
	case !idRe.MatchString(s.ID):
		return fmt.Errorf("id %q must match %s", s.ID, idRe.String())
	case strings.TrimSpace(s.Name) == "":
		return fmt.Errorf("%s: name is required", s.ID)
	case !validTemplates[s.Template]:
		return fmt.Errorf("%s: template %q must be one of project, finance, clients, people, ideas, research, marketing", s.ID, s.Template)
	case !ValidSource(s.Source):
		return fmt.Errorf("%s: source %q must be linear_team:<KEY>, company_finance, company_customers, roster, research or github:<owner/repo>", s.ID, s.Source)
	}
	return nil
}

// ParseSpec decodes and validates one workspace file. Unknown keys are
// errors, as in twins.Parse and decisions.ParseType: a typo in a rule key
// must fail loudly, never be silently ignored.
func ParseSpec(b []byte) (Spec, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var s Spec
	if err := dec.Decode(&s); err != nil {
		return Spec{}, fmt.Errorf("yaml: %w", err)
	}
	if err := s.Validate(); err != nil {
		return Spec{}, err
	}
	s.SpecHash = hashBytes(b)
	return s, nil
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Registry holds every validated Spec for one twin, in id order.
type Registry struct {
	specs []Spec
	byID  map[string]Spec
}

// Specs returns every loaded spec, sorted by id.
func (r *Registry) Specs() []Spec {
	if r == nil {
		return nil
	}
	return append([]Spec(nil), r.specs...)
}

// Spec returns the loaded spec named id, if any.
func (r *Registry) Spec(id string) (Spec, bool) {
	if r == nil {
		return Spec{}, false
	}
	s, ok := r.byID[id]
	return s, ok
}

// MatchingSpecs returns every spec whose membership rules match c, in id
// order — a record may belong to more than one workspace (e.g. a project
// workspace and a domain workspace both), so this returns all matches, not
// just the first.
func (r *Registry) MatchingSpecs(c Candidate) []Spec {
	if r == nil {
		return nil
	}
	var out []Spec
	for _, s := range r.specs {
		if s.Rules().Matches(c) {
			out = append(out, s)
		}
	}
	return out
}

// LoadRegistry reads twins/<twinID>/workspaces/*.yaml from fsys and
// validates every file. A missing directory is an empty registry, not an
// error; a bad file (duplicate id, or ParseSpec's own validation) always
// fails, the same fail-loudly posture decisions.LoadRegistry and
// twins.Load already have for their own embedded directories.
func LoadRegistry(fsys fs.FS, twinID string) (*Registry, error) {
	dir := path.Join("twins", twinID, "workspaces")
	files, err := fs.Glob(fsys, path.Join(dir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("workspaces: %w", err)
	}
	sort.Strings(files)
	r := &Registry{byID: map[string]Spec{}}
	for _, f := range files {
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, fmt.Errorf("workspaces: %w", err)
		}
		s, err := ParseSpec(b)
		if err != nil {
			return nil, fmt.Errorf("workspaces: %s: %w", f, err)
		}
		s.File = f
		if prev, dup := r.byID[s.ID]; dup {
			return nil, fmt.Errorf("workspaces: duplicate id %q in %s and %s", s.ID, prev.File, f)
		}
		r.byID[s.ID] = s
		r.specs = append(r.specs, s)
	}
	sort.Slice(r.specs, func(i, j int) bool { return r.specs[i].ID < r.specs[j].ID })
	return r, nil
}

// Sync upserts one workspaces row per loaded spec (id, name, description,
// template, primary_source, spec_hash). It is idempotent and safe to call
// on every daemon startup, exactly like roster.Load's own Write: re-running
// it after a spec's YAML content changed simply overwrites the row with
// the new spec_hash.
func (r *Registry) Sync(ctx context.Context, st *store.Store) error {
	if r == nil {
		return nil
	}
	for _, s := range r.specs {
		if err := st.Upsert(ctx, &store.Workspace{
			Meta:          store.Meta{Source: SpecSource, SourceID: s.ID},
			Name:          s.Name,
			Description:   s.Description,
			Template:      s.Template,
			PrimarySource: s.Source,
			SpecHash:      s.SpecHash,
		}); err != nil {
			return fmt.Errorf("workspaces: sync %q: %w", s.ID, err)
		}
	}
	return nil
}
