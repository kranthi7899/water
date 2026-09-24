package slots

import (
	"strconv"
	"strings"
	"time"

	"water/internal/nervous/tmpl"
)

var weekdayNames = map[string]time.Weekday{
	"sunday": time.Sunday, "sun": time.Sunday,
	"monday": time.Monday, "mon": time.Monday,
	"tuesday": time.Tuesday, "tue": time.Tuesday, "tues": time.Tuesday,
	"wednesday": time.Wednesday, "wed": time.Wednesday,
	"thursday": time.Thursday, "thu": time.Thursday, "thur": time.Thursday, "thurs": time.Thursday,
	"friday": time.Friday, "fri": time.Friday,
	"saturday": time.Saturday, "sat": time.Saturday,
}

var monthNames = map[string]time.Month{
	"january": time.January, "jan": time.January,
	"february": time.February, "feb": time.February,
	"march": time.March, "mar": time.March,
	"april": time.April, "apr": time.April,
	"may":  time.May,
	"june": time.June, "jun": time.June,
	"july": time.July, "jul": time.July,
	"august": time.August, "aug": time.August,
	"september": time.September, "sep": time.September, "sept": time.September,
	"october": time.October, "oct": time.October,
	"november": time.November, "nov": time.November,
	"december": time.December, "dec": time.December,
}

// dayStart truncates t to midnight in its own location.
func dayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// weekStart returns the Monday (00:00) of t's ISO week.
func weekStart(t time.Time) time.Time {
	offset := (int(t.Weekday()) + 6) % 7 // Mon=0 .. Sun=6
	return dayStart(t).AddDate(0, 0, -offset)
}

// nextWeekdayIncludingToday returns wd's next occurrence on or after today,
// so a weekday name matching today's own weekday resolves to today.
func nextWeekdayIncludingToday(today time.Time, wd time.Weekday) time.Time {
	diff := (int(wd) - int(today.Weekday()) + 7) % 7
	return today.AddDate(0, 0, diff)
}

// nextWeekOccurrence returns wd's occurrence in the ISO week following
// today's, skipping this week's occurrence even if it hasn't happened yet.
func nextWeekOccurrence(today time.Time, wd time.Weekday) time.Time {
	nextMonday := weekStart(today).AddDate(0, 0, 7)
	diff := (int(wd) - int(time.Monday) + 7) % 7
	return nextMonday.AddDate(0, 0, diff)
}

// spokenDate formats d as "Thu 25 Sep".
func spokenDate(d time.Time) string {
	return d.Format("Mon 2 Jan")
}

// spokenLabel formats d relative to today ("Today, Thu 25 Sep",
// "Tomorrow, Fri 26 Sep") or, for any other day, just spokenDate(d).
func spokenLabel(d, today time.Time) string {
	switch {
	case d.Equal(today):
		return "Today, " + spokenDate(d)
	case d.Equal(today.AddDate(0, 0, 1)):
		return "Tomorrow, " + spokenDate(d)
	case d.Equal(today.AddDate(0, 0, -1)):
		return "Yesterday, " + spokenDate(d)
	default:
		return spokenDate(d)
	}
}

func dateValue(d time.Time, label, spoken string) Value {
	return Value{Type: TypeDate, Label: label, Spoken: spoken, Start: d, End: d.AddDate(0, 0, 1)}
}

// stripOrdinal parses a leading integer off a token like "2nd" or "21st",
// tolerating a bare "2" with no suffix.
func stripOrdinal(s string) (int, bool) {
	i := len(s)
	for i > 0 && (s[i-1] < '0' || s[i-1] > '9') {
		i--
	}
	if i == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(s[:i])
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseMonthDay parses "<month> <day>" or "<day> <month>", rolling to next
// year when the resulting date has already passed this year.
func parseMonthDay(toks []string, year int, today time.Time) (time.Time, bool) {
	if len(toks) != 2 {
		return time.Time{}, false
	}
	var month time.Month
	var day int
	var haveMonth, haveDay bool
	if m, ok := monthNames[toks[0]]; ok {
		month, haveMonth = m, true
		day, haveDay = stripOrdinal(toks[1])
	} else if m, ok := monthNames[toks[1]]; ok {
		month, haveMonth = m, true
		day, haveDay = stripOrdinal(toks[0])
	}
	if !haveMonth || !haveDay || day < 1 || day > 31 {
		return time.Time{}, false
	}
	d := time.Date(year, month, day, 0, 0, 0, 0, today.Location())
	if d.Before(dayStart(today)) {
		d = time.Date(year+1, month, day, 0, 0, 0, 0, today.Location())
	}
	return d, true
}

// parseSlashDate parses "M/D" (US order), rolling to next year when the
// resulting date has already passed this year.
func parseSlashDate(tok string, year int, today time.Time) (time.Time, bool) {
	parts := strings.SplitN(tok, "/", 3)
	if len(parts) != 2 {
		return time.Time{}, false
	}
	m, err1 := strconv.Atoi(parts[0])
	d, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || m < 1 || m > 12 || d < 1 || d > 31 {
		return time.Time{}, false
	}
	dt := time.Date(year, time.Month(m), d, 0, 0, 0, 0, today.Location())
	if dt.Before(dayStart(today)) {
		dt = time.Date(year+1, time.Month(m), d, 0, 0, 0, 0, today.Location())
	}
	return dt, true
}

func resolveDate(c tmpl.Capture, now time.Time) (Value, Outcome, []string) {
	toks := c.Tokens
	today := dayStart(now)
	join := strings.Join(toks, " ")

	switch join {
	case "today":
		return dateValue(today, "today", spokenLabel(today, today)), Resolved, nil
	case "tonight":
		return dateValue(today, "tonight", "Tonight, "+spokenDate(today)), Resolved, nil
	case "tomorrow":
		d := today.AddDate(0, 0, 1)
		return dateValue(d, "tomorrow", spokenLabel(d, today)), Resolved, nil
	case "yesterday":
		d := today.AddDate(0, 0, -1)
		return dateValue(d, "yesterday", spokenLabel(d, today)), Resolved, nil
	case "day after tomorrow":
		d := today.AddDate(0, 0, 2)
		return dateValue(d, "day after tomorrow", spokenLabel(d, today)), Resolved, nil
	}

	if len(toks) == 1 {
		if wd, ok := weekdayNames[toks[0]]; ok {
			d := nextWeekdayIncludingToday(today, wd)
			return dateValue(d, toks[0], spokenLabel(d, today)), Resolved, nil
		}
		if d, err := time.ParseInLocation("2006-01-02", toks[0], now.Location()); err == nil {
			return dateValue(d, spokenDate(d), spokenLabel(d, today)), Resolved, nil
		}
		if d, ok := parseSlashDate(toks[0], now.Year(), today); ok {
			return dateValue(d, spokenDate(d), spokenLabel(d, today)), Resolved, nil
		}
	}

	if len(toks) == 2 {
		if toks[0] == "this" {
			if wd, ok := weekdayNames[toks[1]]; ok {
				d := nextWeekdayIncludingToday(today, wd)
				return dateValue(d, toks[1], spokenLabel(d, today)), Resolved, nil
			}
		}
		if toks[0] == "next" {
			if wd, ok := weekdayNames[toks[1]]; ok {
				d := nextWeekOccurrence(today, wd)
				return dateValue(d, "next "+toks[1], spokenLabel(d, today)), Resolved, nil
			}
		}
		if d, ok := parseMonthDay(toks, now.Year(), today); ok {
			return dateValue(d, spokenDate(d), spokenLabel(d, today)), Resolved, nil
		}
	}

	return Value{Type: TypeDate}, Unresolved, nil
}

// resolveDateRange accepts everything resolveDate does (as a single-day
// range) plus the week/weekend phrases resolveDate rejects.
func resolveDateRange(c tmpl.Capture, now time.Time) (Value, Outcome, []string) {
	today := dayStart(now)
	join := strings.Join(c.Tokens, " ")

	switch join {
	case "this week":
		end := weekStart(today).AddDate(0, 0, 7)
		return Value{Type: TypeDateRange, Label: "this week", Spoken: "this week", Start: today, End: end}, Resolved, nil
	case "next week":
		start := weekStart(today).AddDate(0, 0, 7)
		end := start.AddDate(0, 0, 7)
		return Value{Type: TypeDateRange, Label: "next week", Spoken: "next week", Start: start, End: end}, Resolved, nil
	case "this weekend":
		sat := weekStart(today).AddDate(0, 0, 5)
		end := weekStart(today).AddDate(0, 0, 7)
		return Value{Type: TypeDateRange, Label: "this weekend", Spoken: "this weekend", Start: sat, End: end}, Resolved, nil
	}

	v, outcome, opts := resolveDate(c, now)
	if outcome != Resolved {
		return Value{Type: TypeDateRange}, outcome, opts
	}
	v.Type = TypeDateRange
	return v, Resolved, opts
}
