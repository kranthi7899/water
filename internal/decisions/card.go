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
}

type Evidence struct {
	Text   string
	Source string // e.g. "gmail:msg-abc123", "code:runway_calc"
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

// Validate enforces the card's hard invariant: every Evidence entry and
// every Defaults value names a non-empty source, and every Parameters key
// starts from a sourced default. It reports every violation at once.
func (c *Card) Validate() error {
	var errs []error
	for i, e := range c.Evidence {
		if strings.TrimSpace(e.Source) == "" {
			errs = append(errs, fmt.Errorf("%w: evidence[%d] %q has no source", ErrUnsourced, i, clip(e.Text, 40)))
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
	dropped := 0
	for _, e := range c.Evidence {
		if strings.TrimSpace(e.Source) == "" {
			dropped++
			continue
		}
		kept = append(kept, e)
	}
	c.Evidence = kept
	if dropped > 0 {
		c.Gaps = append(c.Gaps, fmt.Sprintf("%d evidence item(s) were dropped because they had no source.", dropped))
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
