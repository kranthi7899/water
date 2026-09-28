package approvals

import (
	"strings"
	"unicode"
)

var (
	// "correct" is deliberately absent: "correct that" / "please correct it"
	// is a request to fix the draft, and it must never read as approval.
	affirmative = set("yes", "yeah", "yep", "yup", "sure", "ok", "okay", "confirm", "confirmed", "approve", "approved", "affirmative")
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

// pendingPhrases are the affirmative phrasings MatchPending accepts when
// exactly one envelope is pending (Slice W, D5a). Each is matched as a whole
// word sequence; none of them contains an affirmative word on its own
// ("go ahead", "do it", "send it" are filler to Match), which is exactly why
// Match alone reads them as Ambiguous — and why, on the voice path where
// anything but Yes is applied as a denial, widening the intent templates
// without this would have turned "go ahead" into a rejection.
var pendingPhrases = [][]string{
	{"go", "ahead"},
	{"do", "it"},
	{"send", "it"},
	{"i", "approve"},
	{"approve", "the", "message"},
	{"approve", "the", "email"},
	{"approve", "the", "mail"},
	{"approve", "the", "draft"},
	{"approve", "the", "request"},
	{"approve", "the", "invite"},
	// "yes that's fine" has been an approvals.respond template since R-21,
	// but "that's" and "fine" are outside Match's vocabulary, so on voice it
	// was a denial.
	{"that's", "fine"},
}

// pendingNeutral are words MatchPending tolerates alongside an affirmative
// when exactly one envelope is pending: they name the thing being approved
// or soften the request, and never change its direction.
var pendingNeutral = set("i", "message", "email", "mail", "draft", "request", "invite", "we", "can", "just")

// MatchPending is Match widened for natural phrasings ("I approve the
// message", "go ahead", "yes send it", "do it"), but only when pending == 1:
// with none or several envelopes waiting, a loose phrase could bind to the
// wrong thing, so it is exactly Match. Negation still wins: a phrase directly
// preceded by a negation ("don't send it") never counts as a yes, and any
// mix of a yes with a negation ("go ahead, no wait") is Ambiguous. Match
// itself is unchanged, so its other callers keep their exact behaviour.
func MatchPending(reply string, pending int) Answer {
	if pending != 1 {
		return Match(reply)
	}
	words := replyWords(reply)
	yes, no, other := false, false, false
	isNeg := func(w string) bool { return negative[w] || strings.HasSuffix(w, "n't") }
	for i := 0; i < len(words); {
		if n := phraseAt(words, i); n > 0 {
			if !(i > 0 && isNeg(words[i-1])) {
				yes = true
			}
			i += n
			continue
		}
		w := words[i]
		i++
		switch {
		case affirmative[w]:
			yes = true
		case isNeg(w):
			no = true
		case filler[w], pendingNeutral[w]:
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

// replyWords is Match's own tokenization: lowercase letter runs (keeping
// apostrophes), curly apostrophes folded, surrounding quotes trimmed.
func replyWords(reply string) []string {
	raw := strings.FieldsFunc(strings.ToLower(strings.ReplaceAll(reply, "’", "'")), func(r rune) bool {
		return !unicode.IsLetter(r) && r != '\''
	})
	out := raw[:0]
	for _, w := range raw {
		if w = strings.Trim(w, "'"); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// phraseAt reports the length of the longest pendingPhrases entry starting
// at words[i], or 0.
func phraseAt(words []string, i int) int {
	best := 0
	for _, p := range pendingPhrases {
		if len(p) <= best || i+len(p) > len(words) {
			continue
		}
		ok := true
		for j, w := range p {
			if words[i+j] != w {
				ok = false
				break
			}
		}
		if ok {
			best = len(p)
		}
	}
	return best
}
