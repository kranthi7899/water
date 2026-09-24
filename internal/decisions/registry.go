// Package decisions is the twin's decision layer: a registry of decision
// types loaded from twins/<id>/decisions/*.yaml, a triage seam that says
// which type an item is, and a card builder that prepares (never makes) a
// decision from read-only research. Nothing here adds an outward action.
package decisions

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"water/internal/twins"
)

// GenericID is the always-present fallback type.
const GenericID = "generic"

// InternalPrefix marks a need computed by registered Go code, not a
// connector call.
const InternalPrefix = "internal://"

// NeedKind says what a need is for. It does not change how it resolves.
type NeedKind string

const (
	KindLookup      NeedKind = "lookup"
	KindComputation NeedKind = "computation"
	KindResearch    NeedKind = "research"
)

// Need is one piece of research a type's card requires. Fetch is either a
// manifest function id ("gmail.list_messages") or an internal:// compute
// function. Args are string templates; see placeholders.
type Need struct {
	Name  string            `yaml:"name"`
	Fetch string            `yaml:"fetch"`
	Kind  NeedKind          `yaml:"kind"`
	Args  map[string]string `yaml:"args"`
}

// Internal reports whether the need is a registered computation.
func (n Need) Internal() bool { return strings.HasPrefix(n.Fetch, InternalPrefix) }

// Type is one decision type: the YAML header plus the free-text notes that
// follow its "---" separator.
type Type struct {
	ID             string   `yaml:"id"`
	Title          string   `yaml:"title"`
	Trigger        string   `yaml:"trigger"`
	Needs          []Need   `yaml:"needs"`
	DefaultRule    string   `yaml:"default_rule"`
	StagedActions  []string `yaml:"staged_actions"`
	SeverityWeight int      `yaml:"severity_weight"`

	Notes string `yaml:"-"`
	File  string `yaml:"-"`
}

// IndexEntry is what the model sees about every type: cheap enough to keep
// in context for dozens of types.
type IndexEntry struct {
	ID      string
	Trigger string
}

// Registry holds every validated type for one twin, plus generic.
type Registry struct {
	types    map[string]Type
	order    []string
	generic  Type
	manifest *twins.Manifest
}

var idRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// placeholderRe matches an args template placeholder such as {sender}.
var placeholderRe = regexp.MustCompile(`\{([a-z_]+)\}`)

// placeholders are the item fields an args template may use.
var placeholders = map[string]bool{"sender": true, "subject": true, "keywords": true, "source_id": true, "thread": true}

// genericSeverity ranks a generic card when nothing else says how much it
// matters.
const genericSeverity = 1

// LoadRegistry reads twins/<m.ID>/decisions/*.yaml from fsys and validates
// every file against m (the already-loaded manifest) and the registered
// compute functions. Any bad file is an error: the daemon must not start
// with a registry it cannot trust, the same posture as twins.Load. A
// missing decisions directory is an empty registry, not an error.
func LoadRegistry(fsys fs.FS, m *twins.Manifest) (*Registry, error) {
	if m == nil {
		return nil, fmt.Errorf("decisions: a manifest is required")
	}
	dir := path.Join("twins", m.ID, "decisions")
	files, err := fs.Glob(fsys, path.Join(dir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("decisions: %w", err)
	}
	sort.Strings(files)
	r := &Registry{types: map[string]Type{}, manifest: m, generic: genericType(m)}
	for _, f := range files {
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, fmt.Errorf("decisions: %w", err)
		}
		t, err := ParseType(b, m)
		if err != nil {
			return nil, fmt.Errorf("decisions: %s: %w", f, err)
		}
		t.File = f
		if prev, dup := r.types[t.ID]; dup {
			return nil, fmt.Errorf("decisions: duplicate id %q in %s and %s", t.ID, prev.File, f)
		}
		r.types[t.ID] = t
		r.order = append(r.order, t.ID)
	}
	return r, nil
}

// splitNotes separates the YAML header from the notes body after a line
// that is exactly "---". A leading "---" (front-matter style) is allowed.
func splitNotes(b []byte) (header []byte, notes string) {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	if strings.HasPrefix(s, "---\n") {
		s = s[4:]
	}
	lines := strings.SplitAfter(s, "\n")
	for i, l := range lines {
		if strings.TrimRight(l, "\n") == "---" {
			return []byte(strings.Join(lines[:i], "")), strings.TrimSpace(strings.Join(lines[i+1:], ""))
		}
	}
	return []byte(s), ""
}

// ParseType decodes and strictly validates one decision-type file. Unknown
// keys are errors, as in twins.Parse: a typo must fail, not be ignored.
func ParseType(b []byte, m *twins.Manifest) (Type, error) {
	header, notes := splitNotes(b)
	dec := yaml.NewDecoder(bytes.NewReader(header))
	dec.KnownFields(true)
	var t Type
	if err := dec.Decode(&t); err != nil {
		return Type{}, fmt.Errorf("yaml: %w", err)
	}
	t.Notes = notes
	return t, t.Validate(m)
}

// Validate checks every field. m is the manifest connector fetches must
// name a function from.
func (t Type) Validate(m *twins.Manifest) error {
	switch {
	case t.ID == "":
		return fmt.Errorf("id is required")
	case !idRe.MatchString(t.ID):
		return fmt.Errorf("id %q must be lowercase letters, digits and underscores", t.ID)
	case t.ID == GenericID:
		return fmt.Errorf("id %q is reserved for the built-in type", GenericID)
	case strings.TrimSpace(t.Title) == "":
		return fmt.Errorf("%s: title is required", t.ID)
	case strings.TrimSpace(t.Trigger) == "":
		return fmt.Errorf("%s: trigger is required", t.ID)
	case strings.TrimSpace(t.DefaultRule) == "":
		return fmt.Errorf("%s: default_rule is required", t.ID)
	case len(t.Needs) == 0:
		return fmt.Errorf("%s: at least one need is required", t.ID)
	case t.SeverityWeight < 1:
		return fmt.Errorf("%s: severity_weight must be a positive integer", t.ID)
	}
	seen := map[string]bool{}
	for i, n := range t.Needs {
		if err := validateNeed(n, m); err != nil {
			return fmt.Errorf("%s: needs[%d]: %w", t.ID, i, err)
		}
		if seen[n.Name] {
			return fmt.Errorf("%s: duplicate need %q", t.ID, n.Name)
		}
		seen[n.Name] = true
	}
	for i, a := range t.StagedActions {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("%s: staged_actions[%d] is empty", t.ID, i)
		}
	}
	return nil
}

func validateNeed(n Need, m *twins.Manifest) error {
	if strings.TrimSpace(n.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if n.Fetch == "" {
		return fmt.Errorf("%s: fetch is required", n.Name)
	}
	switch n.Kind {
	case KindLookup, KindComputation, KindResearch:
	default:
		return fmt.Errorf("%s: kind must be lookup, computation or research, not %q", n.Name, n.Kind)
	}
	if n.Internal() {
		if n.Kind != KindComputation {
			return fmt.Errorf("%s: an internal:// fetch must be kind computation", n.Name)
		}
		if _, ok := lookupCompute(n.Fetch); !ok {
			return fmt.Errorf("%s: no compute function is registered for %s", n.Name, n.Fetch)
		}
		if len(n.Args) > 0 {
			return fmt.Errorf("%s: an internal:// fetch takes no args", n.Name)
		}
		return nil
	}
	if n.Kind == KindComputation {
		return fmt.Errorf("%s: kind computation needs an internal:// fetch", n.Name)
	}
	f, ok := m.Function(n.Fetch)
	if !ok {
		return fmt.Errorf("%s: fetch %s is not a function in the %s manifest", n.Name, n.Fetch, m.ID)
	}
	if f.Level != twins.R {
		return fmt.Errorf("%s: fetch %s is level %s; decision research may only read (R)", n.Name, n.Fetch, f.Level)
	}
	for k, v := range n.Args {
		if k == "" {
			return fmt.Errorf("%s: empty arg name", n.Name)
		}
		for _, p := range placeholderRe.FindAllStringSubmatch(v, -1) {
			if !placeholders[p[1]] {
				return fmt.Errorf("%s: arg %s uses unknown placeholder {%s}", n.Name, k, p[1])
			}
		}
	}
	return nil
}

// genericType is the built-in frame. Its needs are keyword searches over
// whatever read functions the manifest actually grants, so it is valid for
// any manifest and can never fail to load.
func genericType(m *twins.Manifest) Type {
	t := Type{
		ID:      GenericID,
		Title:   "Decision",
		Trigger: "Anything that needs a decision but matches no registered type.",
		// No default_rule and no staged actions: a generic card is never
		// auto-approved and recommends what to prepare, not what to send.
		SeverityWeight: genericSeverity,
		Notes:          "Built-in frame: what is being decided and by when, who asked and who is affected, what each option costs, whether it is reversible, what the connected tools already say, and what is missing.",
	}
	for _, c := range []Need{
		{Name: "related_messages", Fetch: "gmail.list_messages", Kind: KindResearch, Args: map[string]string{"query": "{keywords}"}},
		{Name: "related_documents", Fetch: "gdrive.search_files", Kind: KindResearch, Args: map[string]string{"query": "{keywords}"}},
	} {
		if f, ok := m.Function(c.Fetch); ok && f.Level == twins.R {
			t.Needs = append(t.Needs, c)
		}
	}
	return t
}

// genericQuestions are the frame's standing questions, stated as gaps
// because code cannot answer them from an item alone.
var genericQuestions = []string{
	"Who else is affected by this?",
	"What does each option cost in money, time and people?",
	"Is this reversible?",
}

// Generic returns the built-in fallback type.
func (r *Registry) Generic() Type { return r.generic }

// Lookup returns the full type for id; "generic" is always found.
func (r *Registry) Lookup(id string) (Type, bool) {
	if id == GenericID {
		return r.generic, true
	}
	t, ok := r.types[id]
	return t, ok
}

// Index lists id and trigger for every registered type in file order. It
// does not include generic, which the classifier offers on its own.
func (r *Registry) Index() []IndexEntry {
	out := make([]IndexEntry, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, IndexEntry{ID: id, Trigger: strings.TrimSpace(r.types[id].Trigger)})
	}
	return out
}

// Manifest is the manifest the registry was validated against.
func (r *Registry) Manifest() *twins.Manifest { return r.manifest }
