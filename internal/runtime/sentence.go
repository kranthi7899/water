package runtime

import (
	"strings"
	"unicode/utf8"
)

// SentenceSplitter buffers streamed text and emits complete sentences as they
// close, for the voice channel: a reply is spoken sentence by sentence as it
// streams rather than waiting for the whole turn.
type SentenceSplitter struct {
	buf strings.Builder
}

// Feed appends a delta and returns any sentences that closed as a result, in
// order. Text that has not yet closed stays buffered.
func (s *SentenceSplitter) Feed(delta string) []string {
	s.buf.WriteString(delta)
	return s.drain(false)
}

// Flush returns whatever remains buffered, treating it as the final
// sentence (a turn does not have to end in terminal punctuation).
func (s *SentenceSplitter) Flush() []string {
	return s.drain(true)
}

func (s *SentenceSplitter) drain(final bool) []string {
	text := s.buf.String()
	var out []string
	start := 0
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == '.' || r == '!' || r == '?' || r == '\n' {
			end := i + size
			// Split only on a terminator followed by whitespace or a quote,
			// and only once that next byte has arrived: a '.' inside a number
			// ("3.5") has no space after it, and one at the very end of the
			// buffer waits for the next delta to decide. A '.' that ends an
			// abbreviation ("Mr.", "e.g.", "Inc.") or a list marker ("1.",
			// "a.") is not a sentence end even with a space after it.
			atBoundary := end < len(text) && isBoundaryByte(text[end])
			if atBoundary && r == '.' && dotIsNotSentenceEnd(text[start:i]) {
				atBoundary = false
			}
			if atBoundary {
				sentence := strings.TrimSpace(text[start:end])
				if sentence != "" {
					out = append(out, sentence)
				}
				start = end
			}
		}
		i += size
	}
	s.buf.Reset()
	s.buf.WriteString(text[start:])
	if final {
		rest := strings.TrimSpace(s.buf.String())
		s.buf.Reset()
		if rest != "" {
			out = append(out, rest)
		}
	}
	return out
}

// sentenceAbbrevs end in a '.' that does not end a sentence. A sentence that
// really does end on one ("... and so on, etc.") stays open until the next
// terminator or Flush, which only delays speech a little.
var sentenceAbbrevs = map[string]bool{
	"mr": true, "mrs": true, "ms": true, "dr": true, "st": true, "vs": true, "etc": true,
	"e.g": true, "i.e": true, "inc": true, "ltd": true, "co": true, "jr": true, "sr": true,
	"approx": true, "prof": true, "corp": true,
}

// dotIsNotSentenceEnd reports whether a '.' right after pending (the
// current, not yet emitted sentence up to that dot) ends an abbreviation or
// a list marker rather than the sentence. A list marker is a one- or
// two-digit number or a single letter that opens the sentence or follows a
// colon ("1.", "Three things: 2."); elsewhere "grew to 42." ends one.
func dotIsNotSentenceEnd(pending string) bool {
	tokStart := strings.LastIndexAny(pending, " \t\n") + 1
	tok := strings.TrimLeft(pending[tokStart:], "(\"'")
	if sentenceAbbrevs[strings.ToLower(tok)] {
		return true
	}
	if tok == "" || len(tok) > 2 {
		return false
	}
	marker := len(tok) == 1 && isASCIILetter(tok[0])
	if !marker {
		marker = true
		for j := 0; j < len(tok); j++ {
			if tok[j] < '0' || tok[j] > '9' {
				marker = false
				break
			}
		}
	}
	if !marker {
		return false
	}
	before := strings.TrimSpace(pending[:tokStart])
	return before == "" || strings.HasSuffix(before, ":")
}

func isASCIILetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }

func isBoundaryByte(b byte) bool {
	return b == ' ' || b == '\n' || b == '\t' || b == '"' || b == '\''
}
