package approvals

import (
	"strings"
	"unicode"
)

var (
	affirmative = set("yes", "yeah", "yep", "yup", "sure", "ok", "okay", "confirm", "confirmed", "approve", "approved", "affirmative", "correct")
	negative    = set("no", "nope", "nah", "not", "dont", "don't", "never", "cancel", "stop", "wait", "hold", "abort", "negative", "deny", "reject", "wrong")
	// filler may accompany an affirmative without changing it.
	filler = set("send", "it", "go", "ahead", "do", "please", "that", "this", "now", "and", "the", "one", "thanks", "thank", "you")
)

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// Match interprets a reply deterministically. It returns Yes only when the
// reply has at least one explicit affirmative, no negation, and no word
// outside a small known vocabulary. Any negation alone is No; a mix, a hedge,
// an unknown word or silence is Ambiguous, which callers treat as No.
func Match(reply string) Answer {
	words := strings.FieldsFunc(strings.ToLower(strings.ReplaceAll(reply, "’", "'")), func(r rune) bool {
		return !unicode.IsLetter(r) && r != '\''
	})
	yes, no, other := false, false, false
	for _, w := range words {
		w = strings.Trim(w, "'")
		switch {
		case w == "":
		case affirmative[w]:
			yes = true
		case negative[w] || strings.HasSuffix(w, "n't"):
			no = true
		case filler[w]:
		default:
			other = true
		}
	}
	switch {
	case no && !yes && !other:
		return No
	case no:
		return Ambiguous
	case yes && !other:
		return Yes
	}
	return Ambiguous
}
