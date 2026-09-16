package voice

import (
	"regexp"
	"strings"
)

var (
	fenceRe   = regexp.MustCompile("(?s)```.*?(```|$)")
	linkRe    = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	headingRe = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+`)
	bulletRe  = regexp.MustCompile(`(?m)^\s*(?:[-*+]|\d+[.)])\s+`)
	quoteRe   = regexp.MustCompile(`(?m)^\s*>\s?`)
	ruleRe    = regexp.MustCompile(`(?m)^\s*(?:[-*_]\s*){3,}$`)
	tableSep  = regexp.MustCompile(`(?m)^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$`)
	emphRe    = regexp.MustCompile(`(\*\*|__|\*|~~)`)
	blankRe   = regexp.MustCompile(`\n{3,}`)
)

// Speakable flattens markdown into prose a TTS engine can read naturally:
// code blocks become a short spoken note, markup characters are dropped, and
// each list item or table row ends as its own sentence so the engine pauses.
func Speakable(text string) string {
	s := fenceRe.ReplaceAllString(text, "\n(code block omitted)\n")
	s = linkRe.ReplaceAllString(s, "$1")
	s = tableSep.ReplaceAllString(s, "")
	s = ruleRe.ReplaceAllString(s, "")
	s = headingRe.ReplaceAllString(s, "")
	s = bulletRe.ReplaceAllString(s, "")
	s = quoteRe.ReplaceAllString(s, "")
	s = emphRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "`", "")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		l = strings.TrimSpace(l)
		if strings.Contains(l, "|") {
			cells := strings.FieldsFunc(l, func(r rune) bool { return r == '|' })
			for j := range cells {
				cells[j] = strings.TrimSpace(cells[j])
			}
			l = strings.Join(cells, ", ")
		}
		if r := []rune(l); len(r) > 0 && !strings.ContainsRune(".?!:;,)", r[len(r)-1]) {
			l += "."
		}
		lines[i] = l
	}
	return strings.TrimSpace(blankRe.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}
