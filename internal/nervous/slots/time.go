package slots

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"water/internal/nervous/tmpl"
)

// timeAt builds a Value.At carrying just an hour/minute, on a zero-value
// date; callers combine it with a separately resolved date.
func timeAt(hour, minute int) time.Time {
	return time.Date(1, 1, 1, hour, minute, 0, 0, time.UTC)
}

func formatTime12(hour, minute int) string {
	h := hour % 12
	if h == 0 {
		h = 12
	}
	ampm := "am"
	if hour >= 12 {
		ampm = "pm"
	}
	if minute == 0 {
		return fmt.Sprintf("%d%s", h, ampm)
	}
	return fmt.Sprintf("%d:%02d%s", h, minute, ampm)
}

func timeValue(hour, minute int, label string) Value {
	if label == "" {
		label = formatTime12(hour, minute)
	}
	return Value{Type: TypeTime, Label: label, Spoken: label, At: timeAt(hour, minute)}
}

// parseTimeToken parses one token like "3pm", "15:00", "3:30" or "3:30pm".
// A bare hour (no am/pm suffix, no colon spelling out 24h) applies the
// project's 1-7 -> pm, 8-11 -> am heuristic; a colon form with hour <= 12
// and no suffix gets the same heuristic; hour >= 13 is unambiguous 24h.
func parseTimeToken(tok string) (Value, bool) {
	suffix := ""
	numPart := tok
	switch {
	case strings.HasSuffix(tok, "am"):
		suffix, numPart = "am", strings.TrimSuffix(tok, "am")
	case strings.HasSuffix(tok, "pm"):
		suffix, numPart = "pm", strings.TrimSuffix(tok, "pm")
	}

	var hour, minute int
	if strings.Contains(numPart, ":") {
		parts := strings.SplitN(numPart, ":", 2)
		h, err1 := strconv.Atoi(parts[0])
		m, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			return Value{}, false
		}
		hour, minute = h, m
	} else {
		h, err := strconv.Atoi(numPart)
		if err != nil {
			return Value{}, false
		}
		hour = h
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return Value{}, false
	}

	switch suffix {
	case "am":
		if hour == 12 {
			hour = 0
		} else if hour > 12 {
			return Value{}, false
		}
	case "pm":
		if hour > 12 {
			return Value{}, false
		}
		if hour != 12 {
			hour += 12
		}
	default:
		if hour >= 1 && hour <= 7 {
			hour += 12
		}
		// hour 0, 8-11, or already >=12 (24h form) is left as-is.
	}
	return timeValue(hour, minute, ""), true
}

func resolveTime(c tmpl.Capture) (Value, Outcome, []string) {
	toks := c.Tokens
	if len(toks) == 1 {
		switch toks[0] {
		case "noon":
			return timeValue(12, 0, "noon"), Resolved, nil
		case "midnight":
			return timeValue(0, 0, "midnight"), Resolved, nil
		}
		if v, ok := parseTimeToken(toks[0]); ok {
			return v, Resolved, nil
		}
	} else if len(toks) == 2 {
		if v, ok := parseTimeToken(toks[0] + toks[1]); ok {
			return v, Resolved, nil
		}
	}
	return Value{Type: TypeTime}, Unresolved, nil
}

func partOfDayValue(day time.Time, startHour, endHour int, label string) Value {
	start := day.Add(time.Duration(startHour) * time.Hour)
	end := day.Add(time.Duration(endHour) * time.Hour)
	return Value{Type: TypePartOfDay, Label: label, Spoken: "this " + label, Start: start, End: end}
}

func resolvePartOfDay(c tmpl.Capture, now time.Time) (Value, Outcome, []string) {
	if len(c.Tokens) != 1 {
		return Value{Type: TypePartOfDay}, Unresolved, nil
	}
	day := dayStart(now)
	switch c.Tokens[0] {
	case "morning":
		return partOfDayValue(day, 8, 12, "morning"), Resolved, nil
	case "afternoon":
		return partOfDayValue(day, 12, 17, "afternoon"), Resolved, nil
	case "evening":
		return partOfDayValue(day, 17, 21, "evening"), Resolved, nil
	}
	return Value{Type: TypePartOfDay}, Unresolved, nil
}
