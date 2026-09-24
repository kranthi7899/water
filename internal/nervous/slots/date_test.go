package slots

import (
	"testing"
	"time"

	"water/internal/nervous/tmpl"
)

// ref is the fixed reference "now" every test in this package resolves
// against: 2026-09-24 09:00 in a fixed -7h zone. Tests read ref.Weekday()
// rather than hardcoding which weekday that is.
var ref = time.Date(2026, 9, 24, 9, 0, 0, 0, time.FixedZone("PT", -7*3600))

func cap1(tok string) tmpl.Capture     { return tmpl.Capture{Tokens: []string{tok}} }
func capN(toks ...string) tmpl.Capture { return tmpl.Capture{Tokens: toks} }
func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, ref.Location())
}

func TestResolveDate(t *testing.T) {
	today := dayStart(ref) // 2026-09-24

	cases := []struct {
		name  string
		toks  []string
		want  time.Time
		label string
	}{
		{"today", []string{"today"}, today, "today"},
		{"tonight", []string{"tonight"}, today, "tonight"},
		{"tomorrow", []string{"tomorrow"}, today.AddDate(0, 0, 1), "tomorrow"},
		{"yesterday", []string{"yesterday"}, today.AddDate(0, 0, -1), "yesterday"},
		{"day after tomorrow", []string{"day", "after", "tomorrow"}, today.AddDate(0, 0, 2), "day after tomorrow"},
		{"bare weekday == today", []string{weekdayToken(ref.Weekday())}, today, weekdayToken(ref.Weekday())},
		{"this <weekday> == today", []string{"this", weekdayToken(ref.Weekday())}, today, weekdayToken(ref.Weekday())},
		{"next <weekday> skips this week", []string{"next", weekdayToken(ref.Weekday())}, today.AddDate(0, 0, 7), "next " + weekdayToken(ref.Weekday())},
		{"iso date", []string{"2026-10-02"}, day(2026, 10, 2), "2026-10-02"},
		{"slash date M/D future this year", []string{"10/2"}, day(2026, 10, 2), "10/2"},
		{"slash date M/D already passed rolls to next year", []string{"1/2"}, day(2027, 1, 2), "1/2"},
		{"month day", []string{"october", "2"}, day(2026, 10, 2), "october 2"},
		{"day month", []string{"2", "october"}, day(2026, 10, 2), "2 october"},
		{"month ordinal day", []string{"october", "2nd"}, day(2026, 10, 2), "october 2nd"},
		{"month day already passed rolls to next year", []string{"january", "2"}, day(2027, 1, 2), "january 2"},
		{"month abbrev day", []string{"oct", "2"}, day(2026, 10, 2), "oct 2"},
		{"day month abbrev", []string{"2", "oct"}, day(2026, 10, 2), "2 oct"},
		{"month ordinal 3rd", []string{"november", "3rd"}, day(2026, 11, 3), "november 3rd"},
		{"month ordinal 21st", []string{"december", "21st"}, day(2026, 12, 21), "december 21st"},
		{"month ordinal 4th", []string{"october", "4th"}, day(2026, 10, 4), "october 4th"},
		{"this monday", []string{"this", "monday"}, nextWeekdayIncludingToday(today, time.Monday), "monday"},
		{"this tuesday", []string{"this", "tuesday"}, nextWeekdayIncludingToday(today, time.Tuesday), "tuesday"},
		{"this friday", []string{"this", "friday"}, nextWeekdayIncludingToday(today, time.Friday), "friday"},
		{"next monday", []string{"next", "monday"}, nextWeekOccurrence(today, time.Monday), "next monday"},
		{"next friday", []string{"next", "friday"}, nextWeekOccurrence(today, time.Friday), "next friday"},
		{"next sunday", []string{"next", "sunday"}, nextWeekOccurrence(today, time.Sunday), "next sunday"},
		{"bare monday", []string{"monday"}, nextWeekdayIncludingToday(today, time.Monday), "monday"},
		{"bare friday", []string{"friday"}, nextWeekdayIncludingToday(today, time.Friday), "friday"},
		{"bare sunday", []string{"sunday"}, nextWeekdayIncludingToday(today, time.Sunday), "sunday"},
		{"weekday abbrev mon", []string{"mon"}, nextWeekdayIncludingToday(today, time.Monday), "mon"},
		{"weekday abbrev thu", []string{"thu"}, nextWeekdayIncludingToday(today, time.Thursday), "thu"},
		{"weekday abbrev sat", []string{"sat"}, nextWeekdayIncludingToday(today, time.Saturday), "sat"},
		{"slash date another month", []string{"12/25"}, day(2026, 12, 25), "12/25"},
		{"slash date rolls next year", []string{"2/1"}, day(2027, 2, 1), "2/1"},
		{"iso date another year boundary", []string{"2027-01-15"}, day(2027, 1, 15), "2027-01-15"},
		{"iso date same day as ref", []string{"2026-09-24"}, day(2026, 9, 24), "2026-09-24"},
		{"month day sept later this year", []string{"september", "30"}, day(2026, 9, 30), "september 30"},
		{"month day sept earlier already passed", []string{"september", "1"}, day(2027, 9, 1), "september 1"},
		{"day month sept later this year", []string{"30", "september"}, day(2026, 9, 30), "30 september"},
		{"month day july", []string{"july", "4"}, day(2027, 7, 4), "july 4"},
		{"month day march", []string{"march", "15"}, day(2027, 3, 15), "march 15"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, outcome, _ := resolveDate(capN(c.toks...), ref)
			if outcome != Resolved {
				t.Fatalf("outcome = %v, want Resolved", outcome)
			}
			if !v.Start.Equal(c.want) {
				t.Errorf("Start = %v, want %v", v.Start, c.want)
			}
			if !v.End.Equal(c.want.AddDate(0, 0, 1)) {
				t.Errorf("End = %v, want %v", v.End, c.want.AddDate(0, 0, 1))
			}
			if v.Spoken == "" {
				t.Errorf("Spoken is empty")
			}
		})
	}
}

func TestResolveDateNextWeekdayAcrossWeekBoundary(t *testing.T) {
	// ref is a fixed day; "next <same weekday>" must land 7 days out, in
	// the ISO week *after* the current one, even though ref's own weekday
	// hasn't occurred again within the current week.
	wd := ref.Weekday()
	v, outcome, _ := resolveDate(capN("next", weekdayToken(wd)), ref)
	if outcome != Resolved {
		t.Fatalf("outcome = %v, want Resolved", outcome)
	}
	today := dayStart(ref)
	if !v.Start.Equal(today.AddDate(0, 0, 7)) {
		t.Errorf("Start = %v, want %v (7 days out)", v.Start, today.AddDate(0, 0, 7))
	}
	if v.Start.Weekday() != wd {
		t.Errorf("resolved weekday = %v, want %v", v.Start.Weekday(), wd)
	}
}

func TestResolveDateUnresolved(t *testing.T) {
	cases := [][]string{
		{"sometime"},
		{"later"},
		{"whenever"},
		{"foo", "bar", "baz"},
	}
	for _, toks := range cases {
		_, outcome, _ := resolveDate(capN(toks...), ref)
		if outcome != Unresolved {
			t.Errorf("resolveDate(%v) outcome = %v, want Unresolved", toks, outcome)
		}
	}
}

func TestResolveDateRangeWeekAndWeekend(t *testing.T) {
	today := dayStart(ref)
	monday := weekStart(ref)

	cases := []struct {
		name      string
		toks      []string
		wantStart time.Time
		wantEnd   time.Time
	}{
		{"this week", []string{"this", "week"}, today, monday.AddDate(0, 0, 7)},
		{"next week", []string{"next", "week"}, monday.AddDate(0, 0, 7), monday.AddDate(0, 0, 14)},
		{"this weekend", []string{"this", "weekend"}, monday.AddDate(0, 0, 5), monday.AddDate(0, 0, 7)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, outcome, _ := resolveDateRange(capN(c.toks...), ref)
			if outcome != Resolved {
				t.Fatalf("outcome = %v, want Resolved", outcome)
			}
			if v.Type != TypeDateRange {
				t.Errorf("Type = %v, want TypeDateRange", v.Type)
			}
			if !v.Start.Equal(c.wantStart) || !v.End.Equal(c.wantEnd) {
				t.Errorf("range = [%v,%v), want [%v,%v)", v.Start, v.End, c.wantStart, c.wantEnd)
			}
		})
	}
}

func TestResolveDateRangeFallsBackToSingleDay(t *testing.T) {
	v, outcome, _ := resolveDateRange(cap1("tomorrow"), ref)
	if outcome != Resolved {
		t.Fatalf("outcome = %v, want Resolved", outcome)
	}
	if v.Type != TypeDateRange {
		t.Errorf("Type = %v, want TypeDateRange", v.Type)
	}
	wantStart := dayStart(ref).AddDate(0, 0, 1)
	if !v.Start.Equal(wantStart) {
		t.Errorf("Start = %v, want %v", v.Start, wantStart)
	}
}

func TestResolveDateRangeUnresolved(t *testing.T) {
	_, outcome, _ := resolveDateRange(cap1("sometime"), ref)
	if outcome != Unresolved {
		t.Errorf("outcome = %v, want Unresolved", outcome)
	}
}

// weekdayToken returns the lowercase weekday name resolveDate accepts.
func weekdayToken(wd time.Weekday) string {
	names := map[time.Weekday]string{
		time.Sunday: "sunday", time.Monday: "monday", time.Tuesday: "tuesday",
		time.Wednesday: "wednesday", time.Thursday: "thursday", time.Friday: "friday",
		time.Saturday: "saturday",
	}
	return names[wd]
}
