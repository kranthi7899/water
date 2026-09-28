package tmpl

// stepBudget bounds how many match attempts a single MatchAll call may make
// against one template, so a pathological template/utterance combination
// (deep nested optionals over a long utterance) can never hang or take
// unbounded time. Exceeding it simply means "no further matches found",
// never an error or a panic.
const stepBudget = 10000

// capturePos is a slot capture in token-index space, before it's resolved
// into a Capture against a specific Utterance.
type capturePos struct {
	name       string
	start, end int // token indices [start,end)
}

// matchState is the immutable state threaded through a backtracking match
// attempt. Every mutation copies rather than aliasing a shared backing
// array, since the search explores many branches from the same state.
type matchState struct {
	caps       []capturePos
	literalPos []int // token indices consumed as literals
}

func (s matchState) withCapture(c capturePos) matchState {
	caps := make([]capturePos, len(s.caps)+1)
	copy(caps, s.caps)
	caps[len(s.caps)] = c
	return matchState{caps: caps, literalPos: s.literalPos}
}

func (s matchState) withLiteral(pos int) matchState {
	lits := make([]int, len(s.literalPos)+1)
	copy(lits, s.literalPos)
	lits[len(s.literalPos)] = pos
	return matchState{caps: s.caps, literalPos: lits}
}

// cont is the continuation invoked when a node has matched: it receives the
// resulting token position and state, and reports whether the search
// should stop (true) or keep looking for further matches (false).
type cont func(pos int, st matchState) bool

// matchNode tries to match n against tokens starting at pos, calling k for
// every way it can succeed. It returns true as soon as k (or the budget)
// says to stop searching.
func matchNode(n node, tokens []string, pos int, budget *int, st matchState, k cont) bool {
	*budget++
	if *budget > stepBudget {
		return true
	}
	switch v := n.(type) {
	case litNode:
		if pos < len(tokens) && tokens[pos] == v.word {
			return k(pos+1, st.withLiteral(pos))
		}
		return false
	case seqNode:
		return matchSeq(v.items, 0, tokens, pos, budget, st, k)
	case altNode:
		for _, b := range v.branches {
			if matchNode(b, tokens, pos, budget, st, k) {
				return true
			}
		}
		return false
	case optNode:
		for _, b := range v.branches {
			if matchNode(b, tokens, pos, budget, st, k) {
				return true
			}
		}
		return k(pos, st) // absent
	case slotNode:
		maxLen := v.max
		if pos+maxLen > len(tokens) {
			maxLen = len(tokens) - pos
		}
		for length := v.min; length <= maxLen; length++ {
			*budget++
			if *budget > stepBudget {
				return true
			}
			c := capturePos{name: v.name, start: pos, end: pos + length}
			if k(pos+length, st.withCapture(c)) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// matchSeq matches items[i:] in order, threading pos and st through each
// one via a chain of continuations.
func matchSeq(items []node, i int, tokens []string, pos int, budget *int, st matchState, k cont) bool {
	if i == len(items) {
		return k(pos, st)
	}
	return matchNode(items[i], tokens, pos, budget, st, func(pos2 int, st2 matchState) bool {
		return matchSeq(items, i+1, tokens, pos2, budget, st2, k)
	})
}

// buildMatch resolves a matchState's token-index captures into a Match
// against the utterance that produced it.
func buildMatch(st matchState, u Utterance) Match {
	captures := make([]Capture, 0, len(st.caps))
	for _, c := range st.caps {
		toks := append([]string{}, u.Tokens[c.start:c.end]...)
		captures = append(captures, Capture{
			Slot:   c.name,
			Tokens: toks,
			Raw:    rawSpan(u, c.start, c.end),
		})
	}
	litSet := map[string]bool{}
	for _, p := range st.literalPos {
		litSet[u.Tokens[p]] = true
	}
	litSlice := make([]string, 0, len(litSet))
	for w := range litSet {
		litSlice = append(litSlice, w)
	}
	return Match{Captures: captures, Literals: len(st.literalPos), LiteralSet: litSlice}
}

// rawSpan recovers the original text of tokens[startIdx:endIdx]. When the
// span reaches the last token of the utterance, it extends to the end of
// the raw string rather than stopping at the last token's own byte range,
// so trailing punctuation a template-final text slot captured ("Sounds
// good, see you at 3!") survives intact. A capture that ends earlier in the
// utterance stops exactly at its last token, so it never bleeds into
// content a later part of the template still needs to match.
func rawSpan(u Utterance, startIdx, endIdx int) string {
	lastTok := endIdx - 1
	startByte := u.spans[startIdx][0]
	endByte := u.spans[lastTok][1]
	if lastTok == len(u.Tokens)-1 {
		endByte = len(u.Raw)
	}
	return u.Raw[startByte:endByte]
}
