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
			// Only split on a sentence terminator followed by whitespace,
			// quote, or end of the buffered text so far, not mid-abbreviation
			// or mid-number ("3.5", "Inc.") which have no trailing space yet.
			atBoundary := end >= len(text) || isBoundaryByte(text[end])
			if atBoundary && end < len(text) {
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

func isBoundaryByte(b byte) bool {
	return b == ' ' || b == '\n' || b == '\t' || b == '"' || b == '\''
}
