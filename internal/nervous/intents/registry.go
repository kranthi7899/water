// Package intents is the sous chef's single source of truth: a registry of
// intents loaded from twins/<id>/intents/*.yaml (plus an optional learned
// overlay), validated against the twin's manifest and the declared reflex
// and proposer handlers before anything may ever match against them. It
// mirrors internal/decisions's LoadRegistry pattern deliberately: fail
// loudly on a bad embedded file, tolerate a missing intents directory, and
// never let owner-written overlay data block the daemon.
package intents

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"water/internal/nervous/slots"
	"water/internal/nervous/tmpl"
	"water/internal/twins"
)

// Kind says whether an intent answers from the store (read) or proposes an
// outward action for the gate and approval queue to decide (write).
type Kind string

const (
	KindRead  Kind = "read"
	KindWrite Kind = "write"
)

// escalateIfValues is the enum EscalateIf entries must come from.
var escalateIfValues = map[string]bool{"slot_unresolved": true, "ambiguous_match": true}

// reservedConnectorNames may never be declared in a twin's manifest: each
// name is a namespace this slice's sous-chef machinery owns (a read
// intent's function id, or a quick-tool prefix), so a real connector by
// that name would collide with it. See docs/slices/R.md Design §1(c).
var reservedConnectorNames = map[string]bool{
	"store": true, "approvals": true, "control": true, "status": true,
	"help": true, "quick": true, "brief": true, "learned": true,
}

// Provenance records how a learned intent came to exist: which candidate
// signature it was drafted from, which turns supported it, and when it was
// promoted. Code fills every field; a promoted file is never accepted with
// owner- or model-supplied provenance. Populated starting in a later task
// (the promotion loop); the type exists now so the schema is stable.
type Provenance struct {
	Candidate    string    `yaml:"candidate"`
	TurnIDs      []string  `yaml:"turn_ids"`
	Repeats      int       `yaml:"repeats"`
	DraftedAt    time.Time `yaml:"drafted_at"`
	DraftModel   string    `yaml:"draft_model"`
	PromotedAt   time.Time `yaml:"promoted_at"`
	RegistryHash string    `yaml:"registry_hash"`
}

// SlotSpec declares one template slot: its type, how it resolves when the
// utterance doesn't state it, and whether it must be present.
type SlotSpec struct {
	Type     slots.Type `yaml:"type"`
	Default  string     `yaml:"default"`
	Min      *int       `yaml:"min"`
	Max      *int       `yaml:"max"`
	Required bool       `yaml:"required"`
}

// IntentTest is one fixture case an intent file ships with: an utterance
// that must (or, for "none", must not) match it. A later task runs every
// file's tests through Tier 0 as a regression suite.
type IntentTest struct {
	Utterance string            `yaml:"utterance"`
	Intent    string            `yaml:"intent"`
	Slots     map[string]string `yaml:"slots"`
	Pending   int               `yaml:"pending"`
}

// Intent is one file in twins/<id>/intents/. The YAML header plus an
// optional free-text notes body after a literal "---" line, exactly like
// internal/decisions's Type.
type Intent struct {
	ID              string              `yaml:"id"`
	Description     string              `yaml:"description"`
	Kind            Kind                `yaml:"kind"`
	Function        string              `yaml:"function"`
	Action          string              `yaml:"action"`
	Proposer        string              `yaml:"proposer"`
	Slots           map[string]SlotSpec `yaml:"slots"`
	Templates       []string            `yaml:"templates"`
	Examples        []string            `yaml:"examples"`
	EscalateIf      []string            `yaml:"escalate_if"`
	ReflexEligible  bool                `yaml:"reflex_eligible"`
	RequiresPending bool                `yaml:"requires_pending_approval"`
	Tests           []IntentTest        `yaml:"tests"`
	Origin          string              `yaml:"origin"`
	Provenance      *Provenance         `yaml:"provenance"`

	Notes string `yaml:"-"`
	File  string `yaml:"-"`

	// Active, RequiresApproval, InactiveReason and Disabled are computed at
	// load time, never decoded from YAML.
	Active         bool   `yaml:"-"`
	InactiveReason string `yaml:"-"`
	Disabled       string `yaml:"-"`
	// RequiresApproval is set for an Active write intent whose action is
	// granted at level A: the sous chef must queue an approval envelope
	// (nervous.ActionSink) and nothing executes until the CEO decides it.
	// An Active write intent granted at level D leaves this false: "nothing
	// leaves" by construction, so its proposal is delivered directly, with
	// no envelope and no gate call (see checkAction). Always false for a
	// read intent.
	RequiresApproval bool `yaml:"-"`

	compiled []*tmpl.Template // one per Templates entry, same order
}

// Skipped records one learned overlay file LoadRegistry declined to load.
// Owner data drift must never block the daemon, so this is reporting, not
// an error.
type Skipped struct {
	File   string
	Reason string
}

// Registry holds every validated intent for one twin.
type Registry struct {
	intents map[string]Intent
	order   []string
	shared  Shared
	hash    string
	skipped []Skipped

	// manifest, fns and schemaFn are the exact inputs this registry was
	// built from, kept so a later single-file dry-run check
	// (promote.ValidateLearned, R-23's ValidateAsOverlay/WithOverlay below)
	// can re-run the identical cross-reference checks LoadRegistry itself
	// applies, without its caller needing to thread fsys/manifest/fns
	// through a second time.
	manifest *twins.Manifest
	fns      Functions
	schemaFn func(action string) (SchemaInfo, bool)
}

// LoadOptions carries the pieces LoadRegistry needs that don't come from
// fsys or the manifest directly.
type LoadOptions struct {
	// Schema reports a granted connector function's input schema, so a
	// write intent's proposer can be checked against what the connector
	// actually accepts. The gateway supplies this from connectors.Registry;
	// intents itself never imports connectors. nil skips the check (unit
	// tests that don't care about payload shape).
	Schema func(action string) (SchemaInfo, bool)
	// Learned is the overlay filesystem (nil when the promotion loop is
	// disabled, or there is nothing learned yet).
	Learned fs.FS
	// Disabled maps a learned intent id to why it is currently disabled
	// (manual or an auto-demotion), from the store's intent_state table.
	Disabled map[string]string
}

var idRe = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)
var learnedIDRe = regexp.MustCompile(`^learned\.[a-z][a-z0-9_]*$`)

// LoadRegistry reads twins/<m.ID>/intents/*.yaml from fsys and validates
// every file against m, fns and opts. Any failure in an embedded file
// fails the whole load: the daemon must not start with an intents registry
// it cannot trust, the same posture as twins.Load and decisions.LoadRegistry.
// A missing intents directory is an empty registry, not an error.
func LoadRegistry(fsys fs.FS, m *twins.Manifest, fns Functions, opts LoadOptions) (*Registry, error) {
	if m == nil {
		return nil, fmt.Errorf("intents: a manifest is required")
	}
	for _, c := range m.Connectors {
		if reservedConnectorNames[c.Name] {
			return nil, fmt.Errorf("intents: manifest connector name %q is reserved", c.Name)
		}
	}

	dir := path.Join("twins", m.ID, "intents")
	if _, err := fs.Stat(fsys, dir); err != nil {
		// No intents directory at all: every turn goes to the main path.
		return &Registry{intents: map[string]Intent{}, shared: Shared{}, manifest: m, fns: fns, schemaFn: opts.Schema}, nil
	}

	files, err := fs.Glob(fsys, path.Join(dir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("intents: %w", err)
	}
	sort.Strings(files)

	var sharedFile string
	var intentFiles []string
	for _, f := range files {
		if path.Base(f) == "_shared.yaml" {
			sharedFile = f
			continue
		}
		intentFiles = append(intentFiles, f)
	}
	if sharedFile == "" {
		return nil, fmt.Errorf("intents: %s: _shared.yaml is required when the intents directory exists", dir)
	}
	sharedBytes, err := fs.ReadFile(fsys, sharedFile)
	if err != nil {
		return nil, fmt.Errorf("intents: %w", err)
	}
	shared, err := parseShared(sharedBytes)
	if err != nil {
		return nil, fmt.Errorf("intents: %s: %w", sharedFile, err)
	}

	r := &Registry{intents: map[string]Intent{}, shared: shared, manifest: m, fns: fns, schemaFn: opts.Schema}
	hashInputs := []hashEntry{{path: sharedFile, data: sharedBytes}}

	for _, f := range intentFiles {
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, fmt.Errorf("intents: %w", err)
		}
		it, err := parseIntent(b)
		if err != nil {
			return nil, fmt.Errorf("intents: %s: %w", f, err)
		}
		it.File = f
		if it.Origin != "" {
			return nil, fmt.Errorf("intents: %s: origin must be empty in an embedded intent file", f)
		}
		if it.Provenance != nil {
			return nil, fmt.Errorf("intents: %s: provenance must be absent in an embedded intent file", f)
		}
		if strings.HasPrefix(it.ID, "learned.") {
			return nil, fmt.Errorf("intents: %s: id %q: the \"learned.\" prefix is reserved for the overlay", f, it.ID)
		}
		if err := validateAndCompile(&it, m, fns, shared, opts.Schema); err != nil {
			return nil, fmt.Errorf("intents: %s: %w", f, err)
		}
		if prev, dup := r.intents[it.ID]; dup {
			return nil, fmt.Errorf("intents: duplicate id %q in %s and %s", it.ID, prev.File, f)
		}
		if d, ok := opts.Disabled[it.ID]; ok {
			it.Disabled = d
		}
		r.intents[it.ID] = it
		r.order = append(r.order, it.ID)
		hashInputs = append(hashInputs, hashEntry{path: f, data: b})
	}

	if opts.Learned != nil {
		learnedEntries := r.loadLearned(opts.Learned, opts)
		hashInputs = append(hashInputs, learnedEntries...)
	}

	r.hash = computeHash(hashInputs)
	return r, nil
}

type hashEntry struct {
	path string
	data []byte
}

func computeHash(entries []hashEntry) string {
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	h := sha256.New()
	for _, e := range entries {
		h.Write([]byte(e.path))
		h.Write([]byte{0})
		h.Write(e.data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// loadLearned parses every *.yaml file in learnedFS's root, applying the
// same schema and cross-reference validation as an embedded file, plus the
// learned-only rules (origin, id prefix, no collision with an embedded
// id). An invalid file is skipped and reported, never fatal: owner data
// must never block the daemon, and the embedded registry stays trustworthy
// regardless of what an overlay file contains. Full learned-intent
// eligibility (Learnable-only, no ambiguity, no false accepts against the
// eval negatives) is the promotion loop's job in a later task; this is
// only the loader.
func (r *Registry) loadLearned(learnedFS fs.FS, opts LoadOptions) []hashEntry {
	files, err := fs.Glob(learnedFS, "*.yaml")
	if err != nil {
		r.skipped = append(r.skipped, Skipped{File: "*", Reason: err.Error()})
		return nil
	}
	sort.Strings(files)
	var loaded []hashEntry
	for _, f := range files {
		b, err := fs.ReadFile(learnedFS, f)
		if err != nil {
			r.skipped = append(r.skipped, Skipped{File: f, Reason: err.Error()})
			continue
		}
		it, err := r.addLearnedFile(b, f)
		if err != nil {
			r.skipped = append(r.skipped, Skipped{File: f, Reason: err.Error()})
			continue
		}
		if d, ok := opts.Disabled[it.ID]; ok {
			it.Disabled = d
		}
		r.intents[it.ID] = it
		r.order = append(r.order, it.ID)
		loaded = append(loaded, hashEntry{path: "learned/" + f, data: b})
	}
	return loaded
}

// addLearnedFile parses and validates b as one learned-overlay intent file
// against r's own manifest, function tables and shared config — the exact
// per-file rules loadLearned applies to every real overlay file (origin,
// id prefix, embedded/learned id collision, then the full LoadRegistry
// cross-reference via validateAndCompile) — but never adds it to r.intents
// or r.order, and never mutates r. name is used only for the returned
// Intent's File field and in error messages. Shared by loadLearned (which
// commits the result on success) and ValidateAsOverlay (a pure dry run for
// promote.ValidateLearned, R-23).
func (r *Registry) addLearnedFile(b []byte, name string) (Intent, error) {
	it, err := parseIntent(b)
	if err != nil {
		return Intent{}, err
	}
	it.File = name
	if it.Origin != "learned" {
		return Intent{}, fmt.Errorf("origin must be \"learned\" in an overlay file")
	}
	if !learnedIDRe.MatchString(it.ID) {
		return Intent{}, fmt.Errorf("id %q must start with \"learned.\"", it.ID)
	}
	if _, collide := r.intents[it.ID]; collide {
		return Intent{}, fmt.Errorf("id %q collides with an embedded intent; embedded wins", it.ID)
	}
	if err := validateAndCompile(&it, r.manifest, r.fns, r.shared, r.schemaFn); err != nil {
		return Intent{}, err
	}
	return it, nil
}

// ValidateAsOverlay is addLearnedFile exposed for promote.ValidateLearned
// (R-23): it runs the exact same per-file rules a real learned-overlay load
// applies — YAML schema, origin/id-prefix, embedded/learned id collision,
// and the full manifest/function/schema cross-reference — as a pure read.
// Nothing is added to r, and r is never mutated.
func (r *Registry) ValidateAsOverlay(fileBytes []byte) (Intent, error) {
	return r.addLearnedFile(fileBytes, "draft.yaml")
}

// WithOverlay returns a shallow clone of r with it added as if it were one
// more already-loaded learned intent. promote.ValidateLearned (R-23) uses
// this to run the real Tier 0 matcher (nervous.DryMatch) against a registry
// that includes a draft, to check its ambiguity and false-accept risk,
// without ever mutating or being confused with the live registry Nervous is
// currently matching real turns against.
func (r *Registry) WithOverlay(it Intent) *Registry {
	clone := &Registry{
		intents:  make(map[string]Intent, len(r.intents)+1),
		order:    append(append([]string{}, r.order...), it.ID),
		shared:   r.shared,
		manifest: r.manifest,
		fns:      r.fns,
		schemaFn: r.schemaFn,
	}
	for k, v := range r.intents {
		clone.intents[k] = v
	}
	clone.intents[it.ID] = it
	return clone
}

// WithDisabled returns a shallow clone of r with id's Disabled field set to
// reason ("" clears it), everything else shared unchanged — no re-parsing,
// no re-compiling of templates. The automatic-demotion hook
// (internal/nervous/routelog.go, R-23) uses this: it already knows the
// outcome of a store.SetIntentState write and wants Candidates() to reflect
// it on the very next turn, without waiting for a full LoadRegistry rebuild
// from disk (that happens separately, on demand, via POST
// /v1/intents/reload).
func (r *Registry) WithDisabled(id, reason string) *Registry {
	clone := &Registry{
		intents:  make(map[string]Intent, len(r.intents)),
		order:    r.order,
		shared:   r.shared,
		hash:     r.hash,
		skipped:  r.skipped,
		manifest: r.manifest,
		fns:      r.fns,
		schemaFn: r.schemaFn,
	}
	for k, v := range r.intents {
		clone.intents[k] = v
	}
	if it, ok := clone.intents[id]; ok {
		it.Disabled = reason
		clone.intents[id] = it
	}
	return clone
}

// ReadSpec returns the FunctionSpec a read intent's function id resolves
// to, from the same fns.Read this registry was loaded against.
func (r *Registry) ReadSpec(function string) (FunctionSpec, bool) {
	s, ok := r.fns.Read[function]
	return s, ok
}

// splitNotes separates the YAML header from the notes body after a line
// that is exactly "---", mirroring internal/decisions's splitNotes exactly.
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

func parseIntent(b []byte) (Intent, error) {
	header, notes := splitNotes(b)
	dec := yaml.NewDecoder(bytes.NewReader(header))
	dec.KnownFields(true)
	var it Intent
	if err := dec.Decode(&it); err != nil {
		return Intent{}, fmt.Errorf("yaml: %w", err)
	}
	it.Notes = notes
	if it.Kind == "" {
		it.Kind = KindRead
	}
	return it, nil
}

// validateAndCompile runs every structural and cross-reference check on it,
// compiles its templates, and sets Active/InactiveReason for a write
// intent. It mutates it in place.
func validateAndCompile(it *Intent, m *twins.Manifest, fns Functions, shared Shared, schema func(string) (SchemaInfo, bool)) error {
	if it.ID == "" {
		return fmt.Errorf("id is required")
	}
	if !idRe.MatchString(it.ID) && !learnedIDRe.MatchString(it.ID) {
		return fmt.Errorf("id %q must be two lowercase.snake_case segments separated by a dot", it.ID)
	}
	if strings.TrimSpace(it.Description) == "" {
		return fmt.Errorf("%s: description is required", it.ID)
	}
	if it.Kind != KindRead && it.Kind != KindWrite {
		return fmt.Errorf("%s: kind must be \"read\" or \"write\", not %q", it.ID, it.Kind)
	}
	if it.RequiresPending && it.ID != "approvals.respond" {
		return fmt.Errorf("%s: requires_pending_approval may only be set on approvals.respond", it.ID)
	}
	for _, e := range it.EscalateIf {
		if !escalateIfValues[e] {
			return fmt.Errorf("%s: escalate_if %q must be slot_unresolved or ambiguous_match", it.ID, e)
		}
	}
	if it.ReflexEligible {
		if !hasEscalateIf(it.EscalateIf, "slot_unresolved") || !hasEscalateIf(it.EscalateIf, "ambiguous_match") {
			return fmt.Errorf("%s: reflex_eligible requires escalate_if to list both slot_unresolved and ambiguous_match", it.ID)
		}
	}
	if len(it.Templates) == 0 {
		return fmt.Errorf("%s: at least one template is required", it.ID)
	}
	if len(it.Tests) < 2 {
		return fmt.Errorf("%s: at least two tests are required", it.ID)
	}
	hasPositive := false
	for _, t := range it.Tests {
		if t.Intent == it.ID {
			hasPositive = true
		}
	}
	if !hasPositive {
		return fmt.Errorf("%s: at least one test must be a positive case (intent == %s)", it.ID, it.ID)
	}

	var spec FunctionSpec
	switch it.Kind {
	case KindRead:
		if it.Function == "" {
			return fmt.Errorf("%s: function is required for a read intent", it.ID)
		}
		if it.Action != "" || it.Proposer != "" {
			return fmt.Errorf("%s: a read intent must not set action or proposer", it.ID)
		}
		s, ok := fns.Read[it.Function]
		if !ok {
			return fmt.Errorf("%s: function %q is not a registered reflex handler", it.ID, it.Function)
		}
		if _, isManifestFn := m.Function(it.Function); isManifestFn {
			return fmt.Errorf("%s: function %q must not also be a manifest function", it.ID, it.Function)
		}
		if it.ReflexEligible && !s.ReadOnly {
			return fmt.Errorf("%s: function %q is not read-only, so it cannot be reflex_eligible", it.ID, it.Function)
		}
		for name, ss := range it.Slots {
			if ss.Type == slots.TypeText {
				return fmt.Errorf("%s: read intents may not use a text slot (%s)", it.ID, name)
			}
		}
		it.Active = true
		spec = s
	case KindWrite:
		if it.Function != "" {
			return fmt.Errorf("%s: a write intent must not set function", it.ID)
		}
		if it.Action == "" || it.Proposer == "" {
			return fmt.Errorf("%s: a write intent requires both action and proposer", it.ID)
		}
		s, ok := fns.Write[it.Proposer]
		if !ok {
			return fmt.Errorf("%s: proposer %q is not registered", it.ID, it.Proposer)
		}
		active, requiresApproval, reason, err := checkAction(it.Action, m, schema, s)
		if err != nil {
			return fmt.Errorf("%s: %w", it.ID, err)
		}
		it.Active = active
		it.RequiresApproval = requiresApproval
		it.InactiveReason = reason
		spec = s
	}

	if err := validateSlots(it.ID, it.Slots, spec); err != nil {
		return err
	}

	slotTypes := make(map[string]string, len(it.Slots))
	for name, ss := range it.Slots {
		slotTypes[name] = string(ss.Type)
	}
	it.compiled = make([]*tmpl.Template, len(it.Templates))
	for i, src := range it.Templates {
		tp, err := tmpl.Compile(src, shared.Rules, slotTypes)
		if err != nil {
			return fmt.Errorf("%s: templates[%d]: %w", it.ID, i, err)
		}
		if err := checkTemplateVocabulary(it.ID, i, src, shared); err != nil {
			return err
		}
		it.compiled[i] = tp
	}
	return nil
}

func hasEscalateIf(list []string, v string) bool { return containsString(list, v) }

// validateSlots checks that every declared slot names a real argument of
// spec, with a matching type, and that every argument spec requires either
// has a declared, required slot or a default.
func validateSlots(intentID string, declared map[string]SlotSpec, spec FunctionSpec) error {
	for name, ss := range declared {
		argType, ok := spec.Args[name]
		if !ok {
			return fmt.Errorf("%s: slot %q is not an argument of %s", intentID, name, specName(spec))
		}
		if argType != ss.Type {
			return fmt.Errorf("%s: slot %q is type %s, but the function declares it as %s", intentID, name, ss.Type, argType)
		}
	}
	for _, req := range spec.Required {
		ss, ok := declared[req]
		if !ok {
			return fmt.Errorf("%s: required argument %q has no declared slot", intentID, req)
		}
		if !ss.Required && ss.Default == "" {
			return fmt.Errorf("%s: required argument %q's slot must be required or have a default", intentID, req)
		}
	}
	return nil
}

func specName(spec FunctionSpec) string {
	if spec.ID != "" {
		return spec.ID
	}
	return "the target function"
}

// checkTemplateVocabulary is a best-effort, defense-in-depth compile-time
// lint: a template's literal wording must never itself contain an
// escalate_words entry or a clause_joiners phrase, since a template that
// requires one of those words would always be pre-empted by the
// eligibility check (a later task) before Tier 0 or Tier 1 could ever
// answer it. It does not expand <rule> references (a rule's own content is
// exempt from this check), so it cannot catch every case; the eligibility
// check on the live utterance is the actual run-time enforcement.
func checkTemplateVocabulary(intentID string, idx int, src string, shared Shared) error {
	literal := literalWords(src)
	u := tmpl.Normalize(literal, shared.skipSet())
	joined := " " + strings.Join(u.Tokens, " ") + " "
	for _, w := range shared.EscalateWords {
		pw := tmpl.Normalize(w, nil)
		phrase := " " + strings.Join(pw.Tokens, " ") + " "
		if strings.TrimSpace(phrase) != "" && strings.Contains(joined, phrase) {
			return fmt.Errorf("%s: templates[%d]: literal wording contains the escalate word %q", intentID, idx, w)
		}
	}
	for _, w := range shared.ClauseJoiners {
		pw := tmpl.Normalize(w, nil)
		phrase := " " + strings.Join(pw.Tokens, " ") + " "
		if strings.TrimSpace(phrase) != "" && strings.Contains(joined, phrase) {
			return fmt.Errorf("%s: templates[%d]: literal wording contains the clause joiner %q", intentID, idx, w)
		}
	}
	return nil
}

var slotOrRuleRe = regexp.MustCompile(`\{[^}]*\}|<[^>]*>`)
var grammarPunctRe = regexp.MustCompile(`[()\[\]|]`)

// literalWords strips {slot}/<rule> references and grouping punctuation
// out of a template source, leaving only its literal words.
func literalWords(src string) string {
	s := slotOrRuleRe.ReplaceAllString(src, " ")
	s = grammarPunctRe.ReplaceAllString(s, " ")
	return s
}

// Intents returns every loaded intent, embedded and learned, active,
// inactive and disabled alike, in file order (embedded files first, in
// glob order, then learned files in glob order).
func (r *Registry) Intents() []Intent {
	out := make([]Intent, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.intents[id])
	}
	return out
}

// Candidates returns only the intents any tier may actually match: active
// and not disabled.
func (r *Registry) Candidates() []Intent {
	var out []Intent
	for _, id := range r.order {
		it := r.intents[id]
		if it.Active && it.Disabled == "" {
			out = append(out, it)
		}
	}
	return out
}

// Shadow returns the inactive or disabled intents: never answered, but
// still matched so an escalation can name why ("intent_inactive:<id>").
func (r *Registry) Shadow() []Intent {
	var out []Intent
	for _, id := range r.order {
		it := r.intents[id]
		if !it.Active || it.Disabled != "" {
			out = append(out, it)
		}
	}
	return out
}

// Lookup returns one intent by id, active or not.
func (r *Registry) Lookup(id string) (Intent, bool) {
	it, ok := r.intents[id]
	return it, ok
}

// Templates returns id's compiled templates, in file order.
func (r *Registry) Templates(id string) []*tmpl.Template {
	it, ok := r.intents[id]
	if !ok {
		return nil
	}
	return it.compiled
}

// Shared returns the twin's _shared.yaml content.
func (r *Registry) Shared() Shared { return r.shared }

// Hash is a sha256 over every file this registry was built from (embedded
// and learned), sorted by path: a stable fingerprint used to invalidate a
// stale Tier 1 eval or a stale recorded promotion.
func (r *Registry) Hash() string { return r.hash }

// LearnedSkipped lists every learned overlay file LoadRegistry declined to
// load, and why.
func (r *Registry) LearnedSkipped() []Skipped { return r.skipped }
