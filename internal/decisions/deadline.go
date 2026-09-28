package decisions

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"water/internal/store"
)

// This file is the deterministic deadline extractor docs/slice-c-planning.md
// section 5 asks for: a card's Deadline is parsed from the item in code,
// never guessed by a model. It recognizes a deliberately narrow set of
// phrasings, a cue word ("by", "before", "due", "no later than",
// "deadline:") followed by a day, optionally with a time of day:
//
//	today, tomorrow, EOD / end of day / COB / close of business
//	a weekday name (the next one after the item arrived)
//	an ISO date (2026-10-01)
//	a month and day, with or without a year (October 3rd, Jan 5, 2027)
//	then optionally "5pm", "2:30 pm", "at 17:00"
//
// A day with no time means the end of the business day, 17:00. It returns
// nil, and the card says so as a gap, whenever it is unsure: no match,
// two matches naming different times, "next Friday" (this week's or the
// week after?), the item's own weekday ("by Wednesday" sent on a
// Wednesday), a relative day with no known arrival time, or a date or time
// that does not exist.

// endOfDayHour is the hour a deadline with no stated time resolves to.
const endOfDayHour = 17

var weekdays = map[string]time.Weekday{
	"sunday": time.Sunday, "sun": time.Sunday,
	"monday": time.Monday, "mon": time.Monday,
	"tuesday": time.Tuesday, "tues": time.Tuesday, "tue": time.Tuesday,
	"wednesday": time.Wednesday, "wed": time.Wednesday,
	"thursday": time.Thursday, "thurs": time.Thursday, "thur": time.Thursday, "thu": time.Thursday,
	"friday": time.Friday, "fri": time.Friday,
	"saturday": time.Saturday, "sat": time.Saturday,
}

var months = map[string]time.Month{
	"january": time.January, "jan": time.January, "february": time.February, "feb": time.February,
	"march": time.March, "mar": time.March, "april": time.April, "apr": time.April, "may": time.May,
	"june": time.June, "jun": time.June, "july": time.July, "jul": time.July,
	"august": time.August, "aug": time.August, "september": time.September, "sept": time.September, "sep": time.September,
	"october": time.October, "oct": time.October, "november": time.November, "nov": time.November,
	"december": time.December, "dec": time.December,
}

const (
	weekdayAlt = `sunday|sun|monday|mon|tuesday|tues|tue|wednesday|wed|thursday|thurs|thur|thu|friday|fri|saturday|sat`
	monthAlt   = `january|jan|february|feb|march|mar|april|apr|may|june|jun|july|jul|august|aug|september|sept|sep|october|oct|november|nov|december|dec`
)

// deadlineRe groups: 1 this/next, 2 relative day, 3 weekday, 4 ISO date,
// 5 month, 6 day of month, 7 year, 8-10 12-hour time, 11-12 24-hour time.
var deadlineRe = regexp.MustCompile(`\b(?:by|before|due(?:\s+(?:by|on|before))?|no later than|deadline(?:\s+is)?\s*:?)\s+` +
	`(?:(this|next)\s+)?` +
	`(?:(today|tomorrow|eod|cob|end of (?:the )?day|close of business)|(` + weekdayAlt + `)|(\d{4}-\d{2}-\d{2})|(` + monthAlt + `)\.?\s+(\d{1,2})(?:st|nd|rd|th)?(?:,?\s+(\d{4}))?)\b` +
	`(?:,?\s*(?:at\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)\b|,?\s*(?:at\s+)?(\d{1,2}):(\d{2})\b)?`)

// extractDeadline returns the single deadline text states, resolved
// against received (when the item arrived) in loc, or nil.
func extractDeadline(text string, received time.Time, loc *time.Location) *time.Time {
	if loc == nil {
		loc = time.Local
	}
	var found *time.Time
	for _, m := range deadlineRe.FindAllStringSubmatch(strings.ToLower(text), -1) {
		t, ok := resolveDeadline(m, received, loc)
		if !ok {
			return nil
		}
		if found != nil && !found.Equal(t) {
			return nil
		}
		found = &t
	}
	return found
}

func resolveDeadline(m []string, received time.Time, loc *time.Location) (time.Time, bool) {
	if m[1] == "next" {
		return time.Time{}, false
	}
	var recv time.Time
	if !received.IsZero() {
		recv = received.In(loc)
	}
	var y, d int
	var mo time.Month
	switch {
	case m[2] != "":
		if recv.IsZero() {
			return time.Time{}, false
		}
		day := recv
		if m[2] == "tomorrow" {
			day = recv.AddDate(0, 0, 1)
		}
		y, mo, d = day.Date()
	case m[3] != "":
		if recv.IsZero() {
			return time.Time{}, false
		}
		ahead := (int(weekdays[m[3]]) - int(recv.Weekday()) + 7) % 7
		if ahead == 0 {
			return time.Time{}, false
		}
		y, mo, d = recv.AddDate(0, 0, ahead).Date()
	case m[4] != "":
		t, err := time.ParseInLocation("2006-01-02", m[4], loc)
		if err != nil {
			return time.Time{}, false
		}
		y, mo, d = t.Date()
	case m[5] != "":
		mo = months[m[5]]
		d, _ = strconv.Atoi(m[6])
		if m[7] != "" {
			y, _ = strconv.Atoi(m[7])
		} else {
			if recv.IsZero() {
				return time.Time{}, false
			}
			y = recv.Year()
			if validDate(y, mo, d) && time.Date(y, mo, d, 23, 59, 0, 0, loc).Before(recv) {
				y++
			}
		}
		if !validDate(y, mo, d) {
			return time.Time{}, false
		}
	default:
		return time.Time{}, false
	}
	h, minute := endOfDayHour, 0
	switch {
	case m[8] != "":
		h, _ = strconv.Atoi(m[8])
		if m[9] != "" {
			minute, _ = strconv.Atoi(m[9])
		}
		if h < 1 || h > 12 || minute > 59 {
			return time.Time{}, false
		}
		h %= 12
		if m[10] == "pm" {
			h += 12
		}
	case m[11] != "":
		h, _ = strconv.Atoi(m[11])
		minute, _ = strconv.Atoi(m[12])
		if h > 23 || minute > 59 {
			return time.Time{}, false
		}
	}
	return time.Date(y, mo, d, h, minute, 0, 0, loc), true
}

func validDate(y int, mo time.Month, d int) bool {
	t := time.Date(y, mo, d, 12, 0, 0, 0, time.UTC)
	return d >= 1 && t.Month() == mo && t.Day() == d
}

// deadlineSource is the text a deadline is extracted from and the time the
// item arrived, which relative days ("Friday", "tomorrow") count from.
func deadlineSource(r store.Record) (string, time.Time) {
	switch v := r.(type) {
	case *store.Message:
		at := v.SentAt
		if at.IsZero() {
			at = v.CreatedAt
		}
		return v.Subject + "\n" + v.Body, at
	case *store.Document:
		at := v.ModifiedAt
		if at.IsZero() {
			at = v.CreatedAt
		}
		return v.Title + "\n" + v.Excerpt, at
	case *store.Issue:
		return v.Title, v.CreatedAt
	}
	return "", time.Time{}
}
