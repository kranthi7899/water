package decisions

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"water/internal/voice"
)

// Card is a prepared decision. Code fills every field except the prose
// (Lead, Question, Options, Gaps, Recommendation), which a Phraser may
// reword; no model ever writes a number, a source or the readiness label.
type Card struct {
	ID             string
	TypeID         string // registered id, or "generic"
	Severity       int    // from severity_weight, or a generic default
	Lead           string // one line
	Question       string // what's actually being decided
	Deadline       *time.Time
	Evidence       []Evidence
	Options        []Option
	Gaps           []string // stated openly, never hidden
	Recommendation string   // "" when evidence doesn't support one
	Defaults       map[string]any
	// DefaultSources names, per Defaults key, the code or tool result the
	// value came from. Validate rejects a default without one.
	DefaultSources map[string]string
	Parameters     map[string]any // editable before staging
	StagedActions  []StagedAction
	Readiness      Readiness
	SourceItemIDs  []string // "source:source_id" of the item(s) decided on
	Untrusted      bool

	// TeamSignal is what named people are reported to have said or asked
	// for about this decision (docs/slices/UI.md Phase 1c). It has no
	// computed source today: a computed card leaves it empty, and a
	// persisted decisions_records row (U13) is the only way it's populated,
	// which is why every entry naming a fictional person must set
	// Simulated (U8) -- nothing here enforces that at the type level.
	TeamSignal []Signal

	// ActionSuggestions is the UI-facing view of what can be done about
	// this card: one entry per proposable action, with a short, fixed
	// sentence built by code (never the model), never containing a
	// recipient, address or other payload value. For a computed card these
	// derive 1:1 from StagedActions (see build.go's buildActionSuggestions);
	// for a record-sourced card they come from whatever the persisted
	// record itself specifies. Options (below) is unchanged by this field:
	// ActionSuggestions is additive, a different view of what can be done,
	// not a replacement for the underlying decision-options data.
	ActionSuggestions []Suggestion
}

// Signal is one team member's reported status on a decision
// (docs/slices/UI.md Phase 1c, "Team signal"). Simulated is U8's rule: true
// for anything a fictional person is reported to have said or asked for.
type Signal struct {
	Person    string
	Status    string
	Simulated bool
}

// Suggestion is one card action as the UI shows it: an icon, a short
// code-built sentence, and (once "Review" is chosen) the function and
// payload that action would run. Sentence is never model-written and never
// interpolates a recipient, address or other payload value -- see
// suggestionSentence in build.go for the per-function template table.
type Suggestion struct {
	ID         string
	Icon       string
	Sentence   string
	Function   string
	Payload    map[string]any
	Actionable bool
}

// EvidenceKind is Evidence's closed icon-selection enum (docs/slices/UI.md
// Phase 1c). It is not free text: ValidEvidenceKind is the single place
// that decides membership, so anywhere Evidence.Kind is set can validate
// against it.
const (
	EvidenceKindMoney    = "money"
	EvidenceKindCustomer = "customer"
	EvidenceKindIssue    = "issue"
	EvidenceKindMail     = "mail"
	EvidenceKindCalendar = "calendar"
	EvidenceKindResearch = "research"
)

var validEvidenceKinds = map[string]bool{
	"":                true, // no icon -- most evidence today, unclassified
	EvidenceKindMoney: true, EvidenceKindCustomer: true, EvidenceKindIssue: true,
	EvidenceKindMail: true, EvidenceKindCalendar: true, EvidenceKindResearch: true,
}

// ValidEvidenceKind reports whether k is "" or one of Evidence's closed set
// of icon kinds.
func ValidEvidenceKind(k string) bool { return validEvidenceKinds[k] }

type Evidence struct {
	Text   string
	Source string // e.g. "gmail:msg-abc123", "code:runway_calc"

	// Kind selects which icon the UI shows next to this evidence line; ""
	// means no icon. Must be one of ValidEvidenceKind's closed set.
	Kind string

	// Untrusted marks this specific evidence line as attacker-reachable
	// content. It is distinct from Card.Untrusted (the card-level
	// aggregate, set from Meta.External/NeedResult.External since Slice C):
	// decisions.Merge is what actually populates this field today, when it
	// appends a card_evidence_extra row that was itself marked untrusted
	// (docs/slices/UI.md Phase 1c). Evidence build.go produces directly
	// (the item itself, a fetched record) leaves this at its zero value;
	// Card.Untrusted remains the authoritative aggregate either way.
	Untrusted bool
}

type Option struct {
	Label        string
	Consequences string
}

// StagedAction names an action a card may propose. Actionable is false
// when the manifest does not (yet) grant Function at level A: the card can
// still name it, but nothing can be staged from it.
type StagedAction struct {
	Function   string
	Payload    map[string]any
	Actionable bool
}

type Readiness string

const (
	Ready       Readiness = "ready"
	MissingInfo Readiness = "missing_info"
	Blocked     Readiness = "blocked"
)

// ErrUnsourced is returned by Validate for a figure or evidence item that
// does not say where it came from.
var ErrUnsourced = errors.New("decisions: unsourced figure")

// ErrInvalidEvidenceKind is returned by Validate for an Evidence.Kind value
// outside ValidEvidenceKind's closed set.
var ErrInvalidEvidenceKind = errors.New("decisions: invalid evidence kind")

// Validate enforces the card's hard invariant: every Evidence entry and
// every Defaults value names a non-empty source, and every Parameters key
// starts from a sourced default. It also enforces Evidence.Kind's closed
// set. It reports every violation at once.
func (c *Card) Validate() error {
	var errs []error
	for i, e := range c.Evidence {
		if strings.TrimSpace(e.Source) == "" {
			errs = append(errs, fmt.Errorf("%w: evidence[%d] %q has no source", ErrUnsourced, i, clip(e.Text, 40)))
		}
		if !ValidEvidenceKind(e.Kind) {
			errs = append(errs, fmt.Errorf("%w: evidence[%d] has kind %q", ErrInvalidEvidenceKind, i, e.Kind))
		}
	}
	for _, k := range sortedKeys(c.Defaults) {
		if strings.TrimSpace(c.DefaultSources[k]) == "" {
			errs = append(errs, fmt.Errorf("%w: default %q has no source", ErrUnsourced, k))
		}
	}
	for _, k := range sortedKeys(c.Parameters) {
		if _, ok := c.Defaults[k]; !ok {
			errs = append(errs, fmt.Errorf("%w: parameter %q has no sourced default", ErrUnsourced, k))
		}
	}
	return errors.Join(errs...)
}

// quarantine removes every unsourced value from c, states each removal as
// a gap, and downgrades a ready card to missing_info. It is what the
// builder does when Validate fails: the card still exists, without the
// figures nobody can trace.
func (c *Card) quarantine() {
	var kept []Evidence
	dropped, badKind := 0, 0
	for _, e := range c.Evidence {
		if strings.TrimSpace(e.Source) == "" {
			dropped++
			continue
		}
		if !ValidEvidenceKind(e.Kind) {
			e.Kind = ""
			badKind++
		}
		kept = append(kept, e)
	}
	c.Evidence = kept
	if dropped > 0 {
		c.Gaps = append(c.Gaps, fmt.Sprintf("%d evidence item(s) were dropped because they had no source.", dropped))
	}
	if badKind > 0 {
		c.Gaps = append(c.Gaps, fmt.Sprintf("%d evidence item(s) had an unrecognized icon and were shown without one.", badKind))
	}
	for _, k := range sortedKeys(c.Defaults) {
		if strings.TrimSpace(c.DefaultSources[k]) == "" {
			delete(c.Defaults, k)
			delete(c.DefaultSources, k)
			c.Gaps = append(c.Gaps, fmt.Sprintf("The figure %q was dropped because nothing says where it came from.", k))
		}
	}
	for k := range c.Parameters {
		if _, ok := c.Defaults[k]; !ok {
			delete(c.Parameters, k)
		}
	}
	if c.Readiness == Ready {
		c.Readiness = MissingInfo
	}
}

// Render is the full CLI view, built by code from the card's fields.
func (c *Card) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s\n", c.TypeID, clip(c.Lead, 160))
	flags := []string{"readiness " + string(c.Readiness), fmt.Sprintf("severity %d", c.Severity)}
	if c.Untrusted {
		flags = append(flags, "contains external content")
	}
	fmt.Fprintf(&b, "(%s)\n", strings.Join(flags, ", "))
	fmt.Fprintf(&b, "Question: %s\n", clip(c.Question, 200))
	fmt.Fprintf(&b, "Deadline: %s\n", deadline(c.Deadline))

	b.WriteString("Evidence:\n")
	if len(c.Evidence) == 0 {
		b.WriteString("  - none\n")
	}
	for _, e := range c.Evidence {
		fmt.Fprintf(&b, "  - %s [%s]\n", clip(e.Text, 160), e.Source)
	}
	if len(c.Defaults) > 0 {
		b.WriteString("Figures:\n")
		for _, k := range sortedKeys(c.Defaults) {
			fmt.Fprintf(&b, "  - %s: %s [%s]\n", k, clip(fmt.Sprint(c.Defaults[k]), 60), c.DefaultSources[k])
		}
	}
	if len(c.Options) > 0 {
		b.WriteString("Options:\n")
		for i, o := range c.Options {
			fmt.Fprintf(&b, "  %d. %s", i+1, clip(o.Label, 80))
			if o.Consequences != "" {
				fmt.Fprintf(&b, " — %s", clip(o.Consequences, 160))
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("Gaps:\n")
	if len(c.Gaps) == 0 {
		b.WriteString("  - none\n")
	}
	for _, g := range c.Gaps {
		fmt.Fprintf(&b, "  - %s\n", clip(g, 200))
	}
	if c.Recommendation != "" {
		fmt.Fprintf(&b, "Recommendation: %s\n", clip(c.Recommendation, 240))
	}
	if len(c.StagedActions) > 0 {
		b.WriteString("Could prepare:\n")
		for _, a := range c.StagedActions {
			state := "not yet available"
			if a.Actionable {
				state = "needs your approval"
			}
			fmt.Fprintf(&b, "  - %s (%s)\n", a.Function, state)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// Speak is the short spoken summary: lead, question, deadline, readiness.
func (c *Card) Speak() string {
	parts := []string{clip(c.Lead, 120), clip(c.Question, 120)}
	if c.Deadline != nil {
		parts = append(parts, "Due "+c.Deadline.Local().Format("Monday, January 2"))
	}
	switch c.Readiness {
	case Ready:
		parts = append(parts, "Everything needed is in the packet")
	case MissingInfo:
		parts = append(parts, fmt.Sprintf("Some information is still missing, %d open question(s)", len(c.Gaps)))
	case Blocked:
		parts = append(parts, "I couldn't gather everything, one of the lookups failed")
	}
	var lines []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			lines = append(lines, p)
		}
	}
	return strings.ReplaceAll(voice.Speakable(strings.Join(lines, "\n")), "\n", " ")
}

func deadline(t *time.Time) string {
	if t == nil {
		return "none found"
	}
	return t.Local().Format("Mon 2006-01-02 15:04")
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return "an unknown time"
	}
	return t.Local().Format("2006-01-02 15:04")
}

// Rank sorts cards by Severity (higher first), then Deadline (earlier first;
// a card with no deadline sorts after one with a deadline). It sorts cards
// in place and returns it, the order the morning brief and the CLI's
// `water decisions list` both present.
func Rank(cards []*Card) []*Card {
	sort.SliceStable(cards, func(i, j int) bool {
		if cards[i].Severity != cards[j].Severity {
			return cards[i].Severity > cards[j].Severity
		}
		di, dj := cards[i].Deadline, cards[j].Deadline
		switch {
		case di == nil:
			return false
		case dj == nil:
			return true
		default:
			return di.Before(*dj)
		}
	})
	return cards
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// clip collapses whitespace and control characters, then cuts to n runes
// (approvals.clip's rule, so cards and read-backs read the same way).
func clip(s string, n int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := string(r[:n])
	if r[n] != ' ' {
		if i := strings.LastIndex(cut, " "); i > len(cut)/2 {
			cut = cut[:i]
		}
	}
	return strings.TrimSpace(cut) + "…"
}
