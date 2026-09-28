// Package speak is the one-voice contract: every tier's output on the voice
// channel passes through the same normalization (Speakable) before it
// reaches a client's TTS engine, and the same lint (Lint) reports — never
// rewrites — anything that still looks wrong. Stdlib only, no dependency on
// a model, a connector or the gate: this package only reformats text it is
// handed.
package speak

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Options bounds Speakable's list and length handling. Loc defaults to UTC
// if nil (date/time normalization here is text rewriting, not timezone
// conversion, so Loc only matters if a future pass needs "today"/"now"
// context; it is accepted now so callers don't need a breaking signature
// change later).
type Options struct {
	MaxListItems int
	MaxChars     int
	Loc          *time.Location
}

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

// FlattenMarkdown flattens markdown into prose a TTS engine can read
// naturally: code blocks become a short spoken note, markup characters are
// dropped, and each list item or table row ends as its own sentence so the
// engine pauses. Moved verbatim from internal/voice/speakable.go (Slice R
// task R-13): internal/voice.Speakable now delegates here, unchanged.
func FlattenMarkdown(text string) string {
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

var (
	bareURLRe = regexp.MustCompile(`\bhttps?://\S+|\bwww\.\S+`)

	// timeRangeRe: "3-4pm", "3 - 4 pm" — a single shared am/pm for both ends.
	timeRangeRe = regexp.MustCompile(`(?i)\b(\d{1,2})\s*-\s*(\d{1,2})\s*(am|pm)\b`)
	// timeAmPmRe: "3pm", "3 pm", "3:00pm", "3:30 PM" — an explicit am/pm.
	timeAmPmRe = regexp.MustCompile(`(?i)\b(\d{1,2})(?::([0-5]\d))?\s*(am|pm)\b`)
	// timeBareRe: "15:00", "3:30" — no am/pm marker at all.
	timeBareRe = regexp.MustCompile(`\b([01]?\d|2[0-3]):([0-5]\d)\b`)

	isoDateRe   = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`)
	shortDateRe = regexp.MustCompile(`\b(Mon|Tue|Wed|Thu|Fri|Sat|Sun)\s+(\d{1,2})\s+(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\b`)
)

var weekdayFull = map[string]string{
	"Mon": "Monday", "Tue": "Tuesday", "Wed": "Wednesday", "Thu": "Thursday",
	"Fri": "Friday", "Sat": "Saturday", "Sun": "Sunday",
}

var monthFull = map[string]string{
	"Jan": "January", "Feb": "February", "Mar": "March", "Apr": "April",
	"May": "May", "Jun": "June", "Jul": "July", "Aug": "August",
	"Sep": "September", "Oct": "October", "Nov": "November", "Dec": "December",
}

// formatClock renders an hour/minute as spoken 12-hour time, e.g. (15,0) ->
// "3 PM", (3,30) -> "3:30 PM". Minutes are omitted when zero.
func formatClock(hour, minute int, pm bool) string {
	h := hour % 12
	if h == 0 {
		h = 12
	}
	period := "AM"
	if pm {
		period = "PM"
	}
	if minute == 0 {
		return strconv.Itoa(h) + " " + period
	}
	return strconv.Itoa(h) + ":" + twoDigits(minute) + " " + period
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// bareHourIsPM mirrors internal/nervous/slots' bare-hour rule (a bare 1-7
// defaults to PM, 8-11 to AM) for a "H:MM" with no am/pm marker and an hour
// in 1-12. 12 defaults to PM (noon), matching common usage.
func bareHourIsPM(hour int) bool {
	switch {
	case hour == 12:
		return true
	case hour >= 1 && hour <= 7:
		return true
	default:
		return false
	}
}

func normalizeTimes(s string) string {
	s = timeRangeRe.ReplaceAllStringFunc(s, func(m string) string {
		g := timeRangeRe.FindStringSubmatch(m)
		h1, _ := strconv.Atoi(g[1])
		h2, _ := strconv.Atoi(g[2])
		pm := strings.EqualFold(g[3], "pm")
		h1n := h1 % 12
		if h1n == 0 {
			h1n = 12
		}
		return strconv.Itoa(h1n) + " to " + formatClock(h2, 0, pm)
	})
	s = timeAmPmRe.ReplaceAllStringFunc(s, func(m string) string {
		g := timeAmPmRe.FindStringSubmatch(m)
		hour, _ := strconv.Atoi(g[1])
		minute := 0
		if g[2] != "" {
			minute, _ = strconv.Atoi(g[2])
		}
		return formatClock(hour, minute, strings.EqualFold(g[3], "pm"))
	})
	s = timeBareRe.ReplaceAllStringFunc(s, func(m string) string {
		g := timeBareRe.FindStringSubmatch(m)
		hour, _ := strconv.Atoi(g[1])
		minute, _ := strconv.Atoi(g[2])
		if hour >= 13 && hour <= 23 {
			return formatClock(hour-12, minute, true)
		}
		if hour == 0 {
			return formatClock(12, minute, false)
		}
		return formatClock(hour, minute, bareHourIsPM(hour))
	})
	return s
}

func normalizeDates(s string) string {
	s = isoDateRe.ReplaceAllStringFunc(s, func(m string) string {
		t, err := time.Parse("2006-01-02", m)
		if err != nil {
			return m
		}
		return t.Format("Monday, January 2")
	})
	s = shortDateRe.ReplaceAllStringFunc(s, func(m string) string {
		g := shortDateRe.FindStringSubmatch(m)
		wd, month := weekdayFull[g[1]], monthFull[g[3]]
		if wd == "" || month == "" {
			return m
		}
		return wd + ", " + month + " " + g[2]
	})
	return s
}

// stripEmoji removes emoji/pictographs (Unicode category So), variation
// selectors, zero-width joiners and regional-indicator flag letters — never
// ordinary punctuation or letters.
func stripEmoji(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.Is(unicode.So, r):
		case r == '️' || r == '︎': // variation selectors
		case r == '‍': // zero-width joiner
		case r >= '\U0001F1E6' && r <= '\U0001F1FF': // regional indicators (flags)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// capItems drops list-shaped lines (produced by FlattenMarkdown's own
// sentence-per-line convention only matters for structured Items upstream;
// here capItems operates on already-flattened text lines) beyond max, and
// appends an "and N more." sentence. max<=0 means no cap.
func capItems(s string, max int) string {
	if max <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	var kept []string
	dropped := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if len(kept) < max {
			kept = append(kept, l)
		} else {
			dropped++
		}
	}
	if dropped > 0 {
		kept = append(kept, "and "+strconv.Itoa(dropped)+" more.")
	}
	return strings.Join(kept, "\n")
}

// capChars hard-caps s at maxChars on a sentence boundary (the last
// '.', '?' or '!' at or before the limit), falling back to a word boundary,
// then a hard cut, so voice output never trails off mid-word. maxChars<=0
// means no cap.
func capChars(s string, maxChars int) string {
	if maxChars <= 0 || len(s) <= maxChars {
		return s
	}
	cut := s[:maxChars]
	if i := strings.LastIndexAny(cut, ".?!"); i >= 0 {
		return strings.TrimSpace(cut[:i+1])
	}
	if i := strings.LastIndex(cut, " "); i >= 0 {
		return strings.TrimSpace(cut[:i]) + "."
	}
	return strings.TrimSpace(cut)
}

// Speakable is the full voice-channel normalization every tier's output
// passes through: FlattenMarkdown, then bare-URL, emoji, time and date
// normalization, then list and length capping. Order matches Design §8.3.
func Speakable(s string, o Options) string {
	s = FlattenMarkdown(s)
	s = bareURLRe.ReplaceAllString(s, "a link")
	s = stripEmoji(s)
	s = normalizeTimes(s)
	s = normalizeDates(s)
	s = capItems(s, o.MaxListItems)
	s = capChars(s, o.MaxChars)
	return s
}
