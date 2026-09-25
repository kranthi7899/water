package promote

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"water/internal/nervous"
	"water/internal/nervous/eval"
	"water/internal/nervous/intents"
	"water/internal/nervous/slots"
	"water/internal/nervous/tmpl"
)

// DefaultMaxLearned is the growth loop's cap on simultaneously active
// learned intents (Design §16 item 3: 20).
const DefaultMaxLearned = 20

// ValidateLearned decides whether fileBytes may ever be written into a
// twin's learned-overlay directory (Design §16 item 3). reg is the twin's
// CURRENT registry (embedded intents plus whatever learned intents are
// already active); negatives is the held-out eval set's non-positive cases
// (eval.Negatives()), used for the false-accept check. maxLearned bounds how
// many learned intents may be simultaneously active; <= 0 uses
// DefaultMaxLearned.
//
// Every check below reuses the real machinery a live Tier 0 turn would use
// — intents.Registry's own LoadRegistry-style per-file validation
// (ValidateAsOverlay) and the real specificity/tie matcher
// (nervous.DryMatch) — rather than a second, parallel reimplementation of
// either.
func ValidateLearned(fileBytes []byte, reg *intents.Registry, negatives []eval.Case, maxLearned int) error {
	if reg == nil {
		return fmt.Errorf("promote: validate: a registry is required")
	}
	if maxLearned <= 0 {
		maxLearned = DefaultMaxLearned
	}

	// 1. The normal LoadRegistry-style parse/validate/cross-reference pass
	// (schema, manifest, slot/type checks, template compilation, the
	// escalate_words/clause_joiners vocabulary check, the origin/id-prefix
	// rule and the embedded-id collision rule) — reusing the exact same
	// code loadLearned itself calls, never a reimplementation of it.
	it, err := reg.ValidateAsOverlay(fileBytes)
	if err != nil {
		return fmt.Errorf("promote: fails registry validation: %w", err)
	}

	// 2. Read-only, reflex-eligible, Learnable() only: write/action intents,
	// and functions with side effects or non-lookup/compute/format classes,
	// may never be learned.
	if it.Kind != intents.KindRead || !it.ReflexEligible {
		return fmt.Errorf("promote: %s: learned intents must be kind: read and reflex_eligible", it.ID)
	}
	spec, ok := reg.ReadSpec(it.Function)
	if !ok || !spec.Learnable() {
		return fmt.Errorf("promote: %s: function %q may not be learned (not Learnable())", it.ID, it.Function)
	}

	// 3. No text slot (belt and braces: a read intent can never declare one
	// anyway, per rule 1 above, but the rule is restated here explicitly
	// per Design §16 item 3).
	for name, ss := range it.Slots {
		if ss.Type == slots.TypeText {
			return fmt.Errorf("promote: %s: learned intents may not use a text slot (%s)", it.ID, name)
		}
	}

	// 4. deny_words as a template literal (escalate_words/clause_joiners
	// overlap is already checked by ValidateAsOverlay above, for every
	// intent file regardless of origin).
	for i, src := range it.Templates {
		if w, bad := literalDenyWord(src, reg.Shared()); bad {
			return fmt.Errorf("promote: %s: templates[%d]: literal wording contains deny word %q", it.ID, i, w)
		}
	}

	// 5. The active-learned-intent cap.
	if n := countActiveLearned(reg); n >= maxLearned {
		return fmt.Errorf("promote: %s: learned intent cap reached (%d/%d active)", it.ID, n, maxLearned)
	}

	// 6. Ambiguity, false-accept risk and the file's own tests: all three
	// need the real Tier 0 matcher run against a registry that includes
	// this draft, which WithOverlay builds without ever mutating reg.
	draftReg := reg.WithOverlay(it)
	fx := eval.DefaultFixture()
	ents, err := fixtureEntitiesFromSenders(fx.Senders)
	if err != nil {
		return fmt.Errorf("promote: %s: fixture senders: %w", it.ID, err)
	}
	if err := checkAmbiguity(draftReg, it, fx.Now, ents); err != nil {
		return err
	}
	if err := checkOwnTestsPass(draftReg, it, fx.Now, ents); err != nil {
		return err
	}
	if err := checkFalseAccepts(draftReg, it, negatives, fx.Now, ents); err != nil {
		return err
	}
	return nil
}

// countActiveLearned counts reg.Candidates() (active, not disabled) whose
// Origin is "learned" — the population ValidateLearned's cap bounds.
func countActiveLearned(reg *intents.Registry) int {
	n := 0
	for _, it := range reg.Candidates() {
		if it.Origin == "learned" {
			n++
		}
	}
	return n
}

// fixtureEntitiesFromSenders parses the eval fixture's "Name <email>"
// sender strings into slots.Entities, the same conversion
// internal/nervous/intents' embedded_test.go does for its own fixture.
func fixtureEntitiesFromSenders(senders []string) (slots.Entities, error) {
	people := make([]slots.Person, 0, len(senders))
	for _, s := range senders {
		addr, err := mail.ParseAddress(s)
		if err != nil {
			return slots.Entities{}, err
		}
		people = append(people, slots.Person{Name: addr.Name, Email: addr.Address})
	}
	return slots.Entities{People: people}, nil
}

// ambiguityUtterance is one utterance to run through the real matcher, and
// the pending count it should be evaluated under.
type ambiguityUtterance struct {
	utterance string
	pending   int
}

// ambiguityUtterances is the draft's own tests: utterances (with their
// declared pending count) plus its examples: (T1 declaration text, always
// evaluated at pending=0) — the "candidate utterances" Design §16 item 3's
// ambiguity rule refers to are exactly these: Draft (draft.go) is expected
// to fold the candidate's real sample utterances into the drafted file's
// own examples: field, so there is no separate, out-of-band utterance list
// for ValidateLearned to receive.
func ambiguityUtterances(it intents.Intent) []ambiguityUtterance {
	out := make([]ambiguityUtterance, 0, len(it.Tests)+len(it.Examples))
	for _, tc := range it.Tests {
		out = append(out, ambiguityUtterance{tc.Utterance, tc.Pending})
	}
	for _, ex := range it.Examples {
		out = append(out, ambiguityUtterance{ex, 0})
	}
	return out
}

// checkAmbiguity rejects it if any of its own tests/examples utterances
// matches (via the real Tier 0 matcher, draftReg) any OTHER intent at
// equal-or-higher specificity, or ties with it.
func checkAmbiguity(draftReg *intents.Registry, it intents.Intent, now time.Time, ents slots.Entities) error {
	for _, u := range ambiguityUtterances(it) {
		id, _, reason, ok := nervous.DryMatch(draftReg, u.utterance, u.pending, now, ents)
		switch {
		case ok && id != it.ID:
			return fmt.Errorf("promote: %s: utterance %q is ambiguous: matches %s, not the new intent", it.ID, u.utterance, id)
		case !ok && reason == "ambiguous_match":
			return fmt.Errorf("promote: %s: utterance %q ties with another intent's match", it.ID, u.utterance)
		}
	}
	return nil
}

// checkOwnTestsPass re-runs every declared tests: entry through the real
// Tier 0 matcher against draftReg, and rejects it if any fails to match (or
// wrongly matches) as declared.
func checkOwnTestsPass(draftReg *intents.Registry, it intents.Intent, now time.Time, ents slots.Entities) error {
	for _, tc := range it.Tests {
		id, labels, _, ok := nervous.DryMatch(draftReg, tc.Utterance, tc.Pending, now, ents)
		if tc.Intent == "none" {
			if ok {
				return fmt.Errorf("promote: %s: test %q: want no match, got %s", it.ID, tc.Utterance, id)
			}
			continue
		}
		if !ok || id != tc.Intent {
			return fmt.Errorf("promote: %s: test %q: want %s, got %s (matched=%v)", it.ID, tc.Utterance, tc.Intent, id, ok)
		}
		if !slotsMatchLenient(labels, tc.Slots) {
			return fmt.Errorf("promote: %s: test %q: slots = %v, want %v", it.ID, tc.Utterance, labels, tc.Slots)
		}
	}
	return nil
}

// checkFalseAccepts rejects it if it would answer (via the real Tier 0
// matcher, draftReg) any of the held-out eval negatives — a "none"-classed
// case this draft would wrongly accept.
func checkFalseAccepts(draftReg *intents.Registry, it intents.Intent, negatives []eval.Case, now time.Time, ents slots.Entities) error {
	for _, neg := range negatives {
		id, _, _, ok := nervous.DryMatch(draftReg, neg.Utterance, neg.Pending, now, ents)
		if ok && id == it.ID {
			return fmt.Errorf("promote: %s: would false-accept a %s negative: %q", it.ID, neg.Class, neg.Utterance)
		}
	}
	return nil
}

func slotsMatchLenient(got, want map[string]string) bool {
	for k, v := range want {
		gv, ok := got[k]
		if !ok {
			continue
		}
		if strings.TrimSpace(strings.ToLower(gv)) != strings.TrimSpace(strings.ToLower(v)) {
			return false
		}
	}
	return true
}

var (
	slotOrRuleValidateRe   = regexp.MustCompile(`\{[^}]*\}|<[^>]*>`)
	grammarPunctValidateRe = regexp.MustCompile(`[()\[\]|]`)
)

// literalWordsOnly strips {slot}/<rule> spans and grouping punctuation out
// of a template source, leaving only its literal words — the same
// extraction intents.checkTemplateVocabulary uses for escalate_words/
// clause_joiners, applied here to deny_words instead (Design §16 item 3;
// intents' own per-file validation does not check deny_words, since an
// embedded intent legitimately may use one as a self-exempted literal —
// see denyWordHit's doc comment in internal/nervous/tier0.go. A learned
// intent has no such exemption: the growth loop should never need an
// action verb as a literal at all).
func literalWordsOnly(src string) string {
	s := slotOrRuleValidateRe.ReplaceAllString(src, " ")
	return grammarPunctValidateRe.ReplaceAllString(s, " ")
}

func literalDenyWord(src string, shared intents.Shared) (string, bool) {
	u := tmpl.Normalize(literalWordsOnly(src), skipSet(shared.SkipWords))
	joined := " " + strings.Join(u.Tokens, " ") + " "
	for _, w := range shared.DenyWords {
		pw := tmpl.Normalize(w, nil)
		phrase := " " + strings.Join(pw.Tokens, " ") + " "
		if strings.TrimSpace(phrase) != "" && strings.Contains(joined, phrase) {
			return w, true
		}
	}
	return "", false
}
