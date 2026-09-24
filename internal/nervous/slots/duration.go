package slots

import (
	"fmt"
	"strings"
	"time"

	"water/internal/nervous/tmpl"
)

const (
	minDuration = 5 * time.Minute
	maxDuration = 4 * time.Hour
)

func formatDuration(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%d minutes", int(d/time.Minute))
	}
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	if m == 0 {
		if h == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", h)
	}
	return fmt.Sprintf("%d hour %d minutes", h, m)
}

func durationValue(d time.Duration) Value {
	label := formatDuration(d)
	return Value{Type: TypeDuration, Label: label, Spoken: label, Dur: d}
}

func checkDuration(d time.Duration) (Value, Outcome, []string) {
	if d < minDuration || d > maxDuration {
		return Value{Type: TypeDuration}, Unresolved, nil
	}
	return durationValue(d), Resolved, nil
}

// resolveDuration accepts the named phrases ("half an hour", "an hour",
// "hour and a half") and general "<n> minute(s)"/"<n> hour(s)" numerals,
// bounded to [5 minutes, 4 hours]. A numeral outside that range (e.g. "2
// minutes" or "5 hours") is Unresolved, not silently clamped.
func resolveDuration(c tmpl.Capture) (Value, Outcome, []string) {
	toks := c.Tokens
	switch strings.Join(toks, " ") {
	case "half an hour":
		return durationValue(30 * time.Minute), Resolved, nil
	case "an hour":
		return durationValue(60 * time.Minute), Resolved, nil
	case "hour and a half":
		return durationValue(90 * time.Minute), Resolved, nil
	}

	if len(toks) == 2 {
		if n, ok := parseNumberWord(toks[0]); ok {
			switch toks[1] {
			case "minute", "minutes":
				return checkDuration(time.Duration(n) * time.Minute)
			case "hour", "hours":
				return checkDuration(time.Duration(n) * time.Hour)
			}
		}
	}
	return Value{Type: TypeDuration}, Unresolved, nil
}
