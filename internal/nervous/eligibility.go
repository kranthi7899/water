package nervous

import (
	"strings"

	"water/internal/nervous/intents"
	"water/internal/nervous/tmpl"
)

// sentenceMarks are the characters that can end a clause. A turn containing
// two or more of them, with real content after the first, reads as more
// than one instruction and always escalates rather than risk answering
// only the first half.
const sentenceMarks = ".?!;"

// Eligible decides whether a turn may even be tried against Tier 0/Tier 1,
// before any template match is attempted. u must already be normalized
// with the same skip-word set the caller will match templates against
// (Design §6). ok=false means escalate straight to the main path; reason
// is one of "empty", "too_long", "escalate_word:<entry>" or "multi_clause".
func Eligible(u tmpl.Utterance, sh intents.Shared) (ok bool, reason string) {
	if len(u.Tokens) == 0 {
		return false, "empty"
	}
	if len(u.Tokens) > 24 {
		return false, "too_long"
	}
	if w, hit := matchEscalateWord(u.Tokens, sh.EscalateWords); hit {
		return false, "escalate_word:" + w
	}
	if multiClause(u.Raw, sh.ClauseJoiners) {
		return false, "multi_clause"
	}
	return true, ""
}

// matchEscalateWord reports the first entry of words (single tokens or 2-3
// token phrases) that appears anywhere in tokens as a contiguous run. Each
// entry is normalized the same way an utterance is, so configured phrasing
// like "which is better" matches regardless of case or curly apostrophes.
func matchEscalateWord(tokens []string, words []string) (string, bool) {
	for _, w := range words {
		wTokens := tmpl.Normalize(w, nil).Tokens
		if containsSubsequence(tokens, wTokens) {
			return w, true
		}
	}
	return "", false
}

func containsSubsequence(tokens, sub []string) bool {
	if len(sub) == 0 || len(sub) > len(tokens) {
		return false
	}
	for i := 0; i+len(sub) <= len(tokens); i++ {
		match := true
		for j, s := range sub {
			if tokens[i+j] != s {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// multiClause reports whether raw looks like more than one instruction: a
// configured clause_joiners phrase, or two or more sentence-final marks
// with real content (2+ tokens) following the first one.
func multiClause(raw string, joiners []string) bool {
	lower := strings.ToLower(raw)
	for _, j := range joiners {
		if strings.Contains(lower, j) {
			return true
		}
	}

	first := strings.IndexAny(raw, sentenceMarks)
	if first == -1 {
		return false
	}
	rest := raw[first+1:]
	if !strings.ContainsAny(rest, sentenceMarks) {
		// Only one sentence-final mark in the whole utterance: a single
		// trailing "?" is completely normal and must not itself escalate.
		return false
	}
	restTokens := tmpl.Normalize(rest, nil).Tokens
	return len(restTokens) >= 2
}
