package gmail

import "strings"

// closerWords is the set of trailing sign-off words stripCloser recognizes
// (docs/slices/BRAND.md task 6), matched case-insensitively against a whole
// trimmed line (an optional single trailing comma is also trimmed first),
// never as a substring of a longer line -- see stripCloser's doc comment
// for why that anchoring matters.
var closerWords = map[string]bool{
	"thanks":    true,
	"thank you": true,
	"best":      true,
	"regards":   true,
}

// isCloserLine reports whether line, once trimmed of surrounding
// whitespace and a single optional trailing comma, is exactly one of
// closerWords (case-insensitive) -- nothing more, nothing less. A line like
// "Thanks a lot," or "...I owe you a huge thanks." is not a match: only the
// bare word (or phrase) on its own line counts, so a body that legitimately
// ends mid-sentence with one of these words as part of a real sentence is
// never mistaken for a closer.
func isCloserLine(line string) bool {
	s := strings.TrimSpace(line)
	s = strings.TrimSuffix(s, ",")
	s = strings.TrimSpace(s)
	return closerWords[strings.ToLower(s)]
}

// stripCloser removes a trailing model-written closer -- "Thanks,",
// "Thank you,", "Best,", "Regards," (case-insensitive, with or without the
// trailing comma) -- plus, when present, one following name line, from the
// end of body. It exists so the brand email template's own signoff/
// signature block (internal/brand.RenderEmail) can never double up with one
// the model already wrote (docs/slices/BRAND.md task 6).
//
// Both the closer line and the optional name line after it are matched by
// position (the last, or last-and-second-last, non-blank line of body),
// never by scanning for the word anywhere in the text -- so a body that
// ends mid-sentence with "thanks" as part of a real sentence (rare, but
// possible) is left untouched: isCloserLine only matches a line that is,
// after trimming, nothing but the closer word itself.
func stripCloser(body string) string {
	lines := strings.Split(body, "\n")

	// end is one past the last non-blank line; trailing blank lines are
	// never part of the closer itself.
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	if end == 0 {
		return body
	}

	// Case 1: the last non-blank line is itself the closer (no name line
	// after it, e.g. a lone trailing "Thanks,").
	if isCloserLine(lines[end-1]) {
		return strings.TrimRight(strings.Join(lines[:end-1], "\n"), " \t\n")
	}

	// Case 2: the last non-blank line is a name, and the closer is the
	// next non-blank line before it (skipping any blank line in between,
	// e.g. "Thanks,\n\nKranthi" as well as the brief's own
	// "Thank you,\nKranthi").
	name := strings.TrimSpace(lines[end-1])
	if name == "" {
		return body
	}
	closerIdx := end - 2
	for closerIdx >= 0 && strings.TrimSpace(lines[closerIdx]) == "" {
		closerIdx--
	}
	if closerIdx < 0 || !isCloserLine(lines[closerIdx]) {
		return body
	}
	return strings.TrimRight(strings.Join(lines[:closerIdx], "\n"), " \t\n")
}
