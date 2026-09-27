// Package dashboards loads twins/<id>/dashboards/*.yaml (docs/slices/UI.md
// Phase 1a): a dashboard's data source plus the ids of the three metrics,
// one breakdown and one callout function a later phase (Phase 4,
// internal/dashboards/compute.go) will implement. This phase is spec-only:
// no compute logic and no endpoint. The YAML carries no numbers and no
// expressions, only ids — every metric/breakdown/callout id must appear in
// this package's closed registry of known compute-function ids, and a
// value that merely looks like a number or an expression is rejected on
// shape alone before the registry is even consulted, so a hand-edited
// numeric literal can never leak in ahead of Phase 4.
package dashboards

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"water/internal/workspaces"
)

// idRe is the shape a metric/breakdown/callout id must have: a snake_case
// identifier starting with a letter. This alone rejects a numeric literal
// ("12", "3.5") or an expression ("revenue-cost", "a+b") in a field that
// must carry only an id.
var idRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// crossTeamSource is the one source kind dashboards need beyond
// internal/workspaces' closed set: delivery's dashboard is a cross-team
// view (docs/slices/UI.md Phase 4 "Delivery"), since no single workspace's
// linear_team:<KEY> scopes "every Linear team".
const crossTeamSource = "linear_all"

// ValidSource reports whether s is a recognized dashboard source kind:
// every source kind internal/workspaces.ValidSource accepts, plus
// "linear_all".
func ValidSource(s string) bool { return s == crossTeamSource || workspaces.ValidSource(s) }

// The closed compute-function registries. docs/slices/UI.md's Phase 4
// section spells out each dashboard's three metrics, one breakdown and one
// callout in prose (e.g. finance: "cash position, monthly burn, and
// runway", "spend by application", "the application losing the most
// relative to its revenue"); these ids are this task's own literal
// encoding of that prose into snake_case, fixed now so Phase 4 has a
// contract to implement against and a bad or invented id fails at load
// time, long before there is any compute code to run.
var validMetricIDs = map[string]bool{
	// finance
	"cash_position": true,
	"monthly_burn":  true,
	"runway_months": true,
	// delivery
	"open_issues":    true,
	"blocked_issues": true,
	"urgent_issues":  true,
	// clients
	"accounts_at_risk": true,
	"open_tickets":     true,
	"nps_score":        true,
}

var validBreakdownIDs = map[string]bool{
	"spend_by_application": true, // finance
	"issues_by_team":       true, // delivery
	"accounts_by_health":   true, // clients
}

var validCalloutIDs = map[string]bool{
	"worst_app_margin":                    true, // finance
	"top_blocker_issue":                   true, // delivery
	"longest_silent_account_with_balance": true, // clients
}

// Spec is one twins/<id>/dashboards/<id>.yaml file. ID is the filename
// stem, not a YAML field (docs/slices/UI.md never gives dashboards their
// own id key — there are exactly three, one per file).
type Spec struct {
	ID        string   `yaml:"-"`
	Name      string   `yaml:"name"`
	Source    string   `yaml:"source"`
	Metrics   []string `yaml:"metrics"`
	Breakdown string   `yaml:"breakdown"`
	Callout   string   `yaml:"callout"`

	File string `yaml:"-"`
}

// validateID checks that v has an id's shape: this is the check that
// rejects a numeric-literal- or expression-shaped value, independent of
// whether v also happens to be a known compute-function id.
func validateID(field, v string) error {
	if v == "" {
		return fmt.Errorf("%s is required", field)
	}
	if !idRe.MatchString(v) {
		return fmt.Errorf("%s %q must be a snake_case id, not a number or an expression", field, v)
	}
	return nil
}

// Validate checks name, source, and that metrics/breakdown/callout are
// each a well-shaped id in this package's closed registry.
func (s Spec) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("%s: name is required", s.ID)
	}
	if !ValidSource(s.Source) {
		return fmt.Errorf("%s: source %q is not a recognized source kind", s.ID, s.Source)
	}
	if len(s.Metrics) != 3 {
		return fmt.Errorf("%s: metrics must list exactly 3 ids, got %d", s.ID, len(s.Metrics))
	}
	for i, m := range s.Metrics {
		if err := validateID("metrics", m); err != nil {
			return fmt.Errorf("%s: metrics[%d]: %w", s.ID, i, err)
		}
		if !validMetricIDs[m] {
			return fmt.Errorf("%s: metrics[%d]: %q is not a known metric id", s.ID, i, m)
		}
	}
	if err := validateID("breakdown", s.Breakdown); err != nil {
		return fmt.Errorf("%s: %w", s.ID, err)
	}
	if !validBreakdownIDs[s.Breakdown] {
		return fmt.Errorf("%s: breakdown: %q is not a known breakdown id", s.ID, s.Breakdown)
	}
	if err := validateID("callout", s.Callout); err != nil {
		return fmt.Errorf("%s: %w", s.ID, err)
	}
	if !validCalloutIDs[s.Callout] {
		return fmt.Errorf("%s: callout: %q is not a known callout id", s.ID, s.Callout)
	}
	return nil
}

// ParseSpec decodes and validates one dashboard file. Unknown keys are
// errors, as in twins.Parse, decisions.ParseType and workspaces.ParseSpec:
// a typo must fail loudly, never be silently ignored. id is the filename
// stem (LoadRegistry supplies it; there is no YAML id field).
func ParseSpec(b []byte, id string) (Spec, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var s Spec
	if err := dec.Decode(&s); err != nil {
		return Spec{}, fmt.Errorf("yaml: %w", err)
	}
	s.ID = id
	if err := s.Validate(); err != nil {
		return Spec{}, err
	}
	return s, nil
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

// LoadRegistry reads twins/<twinID>/dashboards/*.yaml from fsys and
// validates every file. A missing directory is an empty registry, not an
// error; a bad file always fails, the same fail-loudly posture
// workspaces.LoadRegistry, decisions.LoadRegistry and twins.Load already
// have for their own embedded directories.
func LoadRegistry(fsys fs.FS, twinID string) (*Registry, error) {
	dir := path.Join("twins", twinID, "dashboards")
	files, err := fs.Glob(fsys, path.Join(dir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("dashboards: %w", err)
	}
	sort.Strings(files)
	r := &Registry{byID: map[string]Spec{}}
	for _, f := range files {
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, fmt.Errorf("dashboards: %w", err)
		}
		id := strings.TrimSuffix(path.Base(f), ".yaml")
		s, err := ParseSpec(b, id)
		if err != nil {
			return nil, fmt.Errorf("dashboards: %s: %w", f, err)
		}
		s.File = f
		if prev, dup := r.byID[s.ID]; dup {
			return nil, fmt.Errorf("dashboards: duplicate id %q in %s and %s", s.ID, prev.File, f)
		}
		r.byID[s.ID] = s
		r.specs = append(r.specs, s)
	}
	sort.Slice(r.specs, func(i, j int) bool { return r.specs[i].ID < r.specs[j].ID })
	return r, nil
}
