// Package tmpl implements the Home-Assistant-style deterministic template
// grammar Tier 0 uses to match an utterance against a fixed set of intent
// templates: literal words, (a|b) alternatives, [x] optionals, {slot}
// captures and <rule> expansions. Matching is anchored (the whole
// normalized utterance must be consumed) and bounded by a step budget, so a
// pathological template can never hang or blow up matching time.
package tmpl

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Capture is one slot's captured span from a single match.
type Capture struct {
	Slot string
	// Tokens are the normalized tokens the slot consumed.
	Tokens []string
	// Raw is the original substring (case and punctuation intact) covering
	// the captured span. For a capture that reaches the last token of the
	// utterance, Raw extends to the end of the raw string, so trailing
	// punctuation ("Sounds good, see you at 3!") survives even though
	// nothing after the last token was itself captured as a token.
	Raw string
}

// Match is one full, anchored match of a Template against an Utterance.
type Match struct {
	Captures []Capture
	// Literals is the count of literal tokens this match consumed, used by
	// callers to prefer the more specific of two competing matches.
	Literals int
	// LiteralSet is the deduplicated set of literal tokens this match
	// consumed, used by callers to exempt a matched literal from a
	// deny-word check.
	LiteralSet []string
}

// Utterance is a normalized utterance ready for matching. Tokens and the
// token->raw-byte-range mapping reflect skip-word filtering; Raw is the
// original, unfiltered text.
type Utterance struct {
	Raw    string
	Tokens []string
	spans  [][2]int // parallel to Tokens: each token's [start,end) byte range in Raw
}

// isTokenRune reports whether r (already passed through mapRune) belongs to
// a token: letters, digits, apostrophe, colon, slash and hyphen. Everything
// else is a separator.
func isTokenRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) ||
		r == '\'' || r == ':' || r == '/' || r == '-'
}

// mapRune normalizes a single rune for matching: curly apostrophes become a
// straight apostrophe, and everything else is lowercased.
func mapRune(r rune) rune {
	if r == '‘' || r == '’' {
		return '\''
	}
	return unicode.ToLower(r)
}

// Normalize lowercases raw, maps curly apostrophes to straight ones, strips
// everything that isn't a token rune, tokenizes on the resulting runs, and
// drops any token present in skip. The returned Utterance keeps Raw
// unmodified so Capture.Raw can recover the original text of a match.
func Normalize(raw string, skip map[string]bool) Utterance {
	var tokens []string
	var spans [][2]int
	curStart := -1
	var cur strings.Builder

	i := 0
	for i < len(raw) {
		r, size := utf8.DecodeRuneInString(raw[i:])
		mapped := mapRune(r)
		if isTokenRune(mapped) {
			if curStart == -1 {
				curStart = i
			}
			cur.WriteRune(mapped)
		} else if curStart != -1 {
			tokens = append(tokens, cur.String())
			spans = append(spans, [2]int{curStart, i})
			cur.Reset()
			curStart = -1
		}
		i += size
	}
	if curStart != -1 {
		tokens = append(tokens, cur.String())
		spans = append(spans, [2]int{curStart, len(raw)})
	}

	if len(skip) == 0 {
		return Utterance{Raw: raw, Tokens: tokens, spans: spans}
	}
	outTokens := make([]string, 0, len(tokens))
	outSpans := make([][2]int, 0, len(spans))
	for idx, t := range tokens {
		if skip[t] {
			continue
		}
		outTokens = append(outTokens, t)
		outSpans = append(outSpans, spans[idx])
	}
	return Utterance{Raw: raw, Tokens: outTokens, spans: outSpans}
}

// Template is a compiled grammar, ready to match against utterances.
type Template struct {
	Source string
	root   node
	vocab  map[string]bool
}

// LiteralVocabulary returns every literal token this template could ever
// consume, across all of its alternative branches.
func (t *Template) LiteralVocabulary() map[string]bool {
	out := make(map[string]bool, len(t.vocab))
	for k := range t.vocab {
		out[k] = true
	}
	return out
}

// MatchAll enumerates every anchored full match of t against u, calling try
// for each one in the order the backtracking search finds them. try returns
// true to stop the search early, false to keep looking for further matches
// (used by callers that want to compare several candidates' specificity).
func (t *Template) MatchAll(u Utterance, try func(Match) bool) {
	if t == nil || t.root == nil {
		return
	}
	budget := 0
	matchNode(t.root, u.Tokens, 0, &budget, matchState{}, func(pos int, st matchState) bool {
		if pos != len(u.Tokens) {
			return false
		}
		return try(buildMatch(st, u))
	})
}
