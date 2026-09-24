// Package render turns a quick tier's structured Result into the words a
// channel actually shows, from twins/<id>/style.yaml. Tiers never produce
// finished sentences themselves — one renderer, driven by one style file,
// keeps the twin's voice the same regardless of which tier answered.
package render

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Item is one row of a list answer, e.g. {"time":"10:00","title":"Board sync"}.
type Item map[string]string

// Clarification is a short question with a fixed set of answers, shown when
// a tier can't proceed without the CEO picking one (an ambiguous person, or
// more than one pending approval).
type Clarification struct {
	Question string
	Options  []string
}

// Kind labels what a Result represents. It has no behavior of its own; it
// exists so later tasks (write intents, approvals) can tag a Result without
// widening this package's API.
type Kind string

const (
	KindRead     Kind = "read"
	KindProposal Kind = "proposal"
	KindDecision Kind = "decision"
	KindClarify  Kind = "clarify"
)

// Result is what every quick-tier handler returns. It is data, not prose:
// the renderer, not the handler, decides the exact words.
type Result struct {
	Intent string
	Kind   string // one of the Kind constants; string so callers aren't forced to import Kind just to compare

	// Interpretation is the header echo of what the handler understood,
	// e.g. "Tomorrow, Thu 25 Sep". Non-empty for KindRead and KindProposal
	// results, so a misread slot is always visible in the answer itself.
	Interpretation string

	// Facts substitute into a response's header/empty templates, keyed by
	// name (including "<slot>_spoken" for every resolved slot).
	Facts map[string]string

	Items              []Item
	Warnings           []string
	NeedsClarification *Clarification

	// ApprovalID is set when a write intent's proposal queued an envelope;
	// the facade uses it to emit approval_required alongside this Result.
	ApprovalID string

	// Tainted is true when any record behind this Result carries External
	// content (a synced email, an attendee added by someone else, ...).
	Tainted bool

	// Text is pre-phrased prose (the cached brief, an approval read-back)
	// that bypasses header/item templating entirely. It is still subject
	// to the channel's length cap.
	Text string
}

// placeholderRe matches a single {name} substitution site. Substitution is
// a single pass over the ORIGINAL template string: only text that already
// contains "{...}" in the template is ever treated as a substitution site.
// A value being substituted in is never rescanned, so external content
// (an event title of "Standup {sync}") lands in the output literally
// instead of being read as a nested placeholder or erroring.
var placeholderRe = regexp.MustCompile(`\{[A-Za-z0-9_]+\}`)

func substitute(tmpl string, vals map[string]string) string {
	return placeholderRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		key := m[1 : len(m)-1]
		if v, ok := vals[key]; ok {
			return v
		}
		return m
	})
}

// Render writes the words for one Result on one channel ("cli", "text-bar"
// or "voice"). It looks up the intent's response templates when the style
// declares one, and otherwise falls back to a plain, deterministic
// rendering so an intent with no styled response still answers sensibly.
func (s *Style) Render(r Result, ch string) string {
	if r.NeedsClarification != nil {
		return renderClarification(*r.NeedsClarification)
	}

	entry, hasEntry := s.responses[r.Intent]

	var b strings.Builder

	header := r.Interpretation
	if hasEntry && entry.Header != "" {
		header = substitute(entry.Header, r.Facts)
	} else if header == "" {
		header = "Here's what I found."
	}
	b.WriteString(header)

	if r.Text != "" {
		// Pre-phrased prose (brief, read-back) replaces the item/empty
		// body entirely; the header above still applies when set.
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(r.Text)
	} else if len(r.Items) == 0 {
		empty := "Nothing found."
		if hasEntry && entry.Empty != "" {
			empty = substitute(entry.Empty, r.Facts)
		}
		b.WriteString("\n")
		b.WriteString(empty)
	} else {
		max := s.MaxListItems(ch)
		items := r.Items
		more := 0
		if max > 0 && len(items) > max {
			more = len(items) - max
			items = items[:max]
		}
		for _, it := range items {
			var line string
			if hasEntry && entry.Item != "" {
				line = substitute(entry.Item, it)
			} else {
				line = defaultItemLine(it)
			}
			b.WriteString("\n")
			b.WriteString(line)
		}
		if more > 0 {
			moreTmpl := "and {n} more"
			if hasEntry && entry.More != "" {
				moreTmpl = entry.More
			}
			b.WriteString("\n")
			b.WriteString(substitute(moreTmpl, map[string]string{"n": strconv.Itoa(more)}))
		}
	}

	for _, w := range r.Warnings {
		b.WriteString(fmt.Sprintf("\n(%s)", w))
	}

	return b.String()
}

func renderClarification(c Clarification) string {
	var b strings.Builder
	b.WriteString(c.Question)
	for i, opt := range c.Options {
		fmt.Fprintf(&b, "\n%d. %s", i+1, opt)
	}
	return b.String()
}

// defaultItemLine renders an Item with no styled template. Map iteration
// order is undefined in Go, so keys are sorted for a deterministic result.
func defaultItemLine(it Item) string {
	keys := make([]string, 0, len(it))
	for k := range it {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+it[k])
	}
	return "- " + strings.Join(parts, ", ")
}
