package slots

import (
	"strings"

	"water/internal/nervous/tmpl"
)

const maxTextLen = 1000

// resolveText always resolves a non-empty capture: it uses the raw span
// (case and punctuation intact), trimmed and capped at 1000 characters.
func resolveText(c tmpl.Capture) (Value, Outcome, []string) {
	t := strings.TrimSpace(c.Raw)
	if t == "" {
		return Value{Type: TypeText}, Unresolved, nil
	}
	if len(t) > maxTextLen {
		t = t[:maxTextLen]
	}
	return Value{Type: TypeText, Label: t, Spoken: t, Text: t}, Resolved, nil
}
