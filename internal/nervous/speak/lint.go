package speak

import (
	"regexp"
	"strings"

	"water/internal/nervous/render"
)

// markdownHintRe matches the same constructs FlattenMarkdown strips, used
// only to detect (never to remove) markdown in Lint's input.
var markdownHintRe = regexp.MustCompile("(?m)```|^\\s{0,3}#{1,6}\\s|^\\s*[-*+]\\s|^\\s*>\\s|\\*\\*|__|~~|\\[[^\\]]*\\]\\([^)]*\\)")

var emojiHintRe = regexp.MustCompile(`[\x{1F300}-\x{1FAFF}\x{2600}-\x{27BF}]`)

// Lint reports what still looks wrong in raw voice-channel text, without
// touching it: a model's reply may contain a banned phrase, markdown, an
// emoji, a URL, an over-length list, or simply run long. Every warning is a
// short machine-readable tag (recorded by a later task's route_log row,
// Design §14); Lint never rewrites prose — only Speakable does that, and
// only for the quick tiers' own rendered output.
func Lint(raw string, v render.VoiceStyle, maxChars int) []string {
	var warnings []string
	lower := strings.ToLower(raw)

	for _, phrase := range v.BannedPhrases {
		if phrase == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(phrase)) {
			warnings = append(warnings, "banned:"+phrase)
		}
	}

	if markdownHintRe.MatchString(raw) {
		warnings = append(warnings, "markdown")
	}
	if emojiHintRe.MatchString(raw) {
		warnings = append(warnings, "emoji")
	}
	if bareURLRe.MatchString(raw) {
		warnings = append(warnings, "url")
	}
	if maxChars > 0 && len(raw) > int(float64(maxChars)*1.5) {
		warnings = append(warnings, "overlength")
	}
	if listOvercap(raw) {
		warnings = append(warnings, "list_overcap")
	}

	return warnings
}

// listOvercap reports whether raw contains more than 3 consecutive
// list-shaped lines (a bullet or a numbered item) — an unbounded list read
// aloud is the thing MaxListItems exists to prevent on the quick tiers'
// own output; Lint flags the same shape on the main path's free-form reply,
// since nothing there enforces a cap.
func listOvercap(raw string) bool {
	listLineRe := regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+`)
	run := 0
	for _, line := range strings.Split(raw, "\n") {
		if listLineRe.MatchString(line) {
			run++
			if run > 3 {
				return true
			}
			continue
		}
		if strings.TrimSpace(line) != "" {
			run = 0
		}
	}
	return false
}
