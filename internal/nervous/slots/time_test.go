package slots

import (
	"testing"
	"time"
)

func TestResolveTime(t *testing.T) {
	cases := []struct {
		toks       []string
		wantHour   int
		wantMinute int
	}{
		{[]string{"3pm"}, 15, 0},
		{[]string{"3", "pm"}, 15, 0},
		{[]string{"3am"}, 3, 0},
		{[]string{"12pm"}, 12, 0},
		{[]string{"12am"}, 0, 0},
		{[]string{"3:30"}, 15, 30}, // bare hour 3 -> pm heuristic
		{[]string{"3:30pm"}, 15, 30},
		{[]string{"3:30am"}, 3, 30},
		{[]string{"15:00"}, 15, 0},
		{[]string{"9:15"}, 9, 15}, // bare hour 9 -> am heuristic (8-11)
		{[]string{"noon"}, 12, 0},
		{[]string{"midnight"}, 0, 0},
		{[]string{"1"}, 13, 0},  // bare hour 1-7 -> pm
		{[]string{"8"}, 8, 0},   // bare hour 8-11 -> am
		{[]string{"11"}, 11, 0}, // bare hour 8-11 -> am
	}
	for _, c := range cases {
		v, outcome, _ := Resolve(TypeTime, capN(c.toks...), Spec{}, ref, Entities{})
		if outcome != Resolved {
			t.Errorf("Resolve(%v) outcome = %v, want Resolved", c.toks, outcome)
			continue
		}
		if v.At.Hour() != c.wantHour || v.At.Minute() != c.wantMinute {
			t.Errorf("Resolve(%v) = %02d:%02d, want %02d:%02d", c.toks, v.At.Hour(), v.At.Minute(), c.wantHour, c.wantMinute)
		}
		if v.Spoken == "" {
			t.Errorf("Resolve(%v).Spoken is empty", c.toks)
		}
	}
}

func TestResolveTimeUnresolved(t *testing.T) {
	cases := [][]string{
		{"whenever"},
		{"25:00"},
		{"3:70"},
		{"13pm"},
	}
	for _, toks := range cases {
		_, outcome, _ := Resolve(TypeTime, capN(toks...), Spec{}, ref, Entities{})
		if outcome != Unresolved {
			t.Errorf("Resolve(%v) outcome = %v, want Unresolved", toks, outcome)
		}
	}
}

func TestResolvePartOfDay(t *testing.T) {
	cases := []struct {
		tok       string
		startHour int
		endHour   int
	}{
		{"morning", 8, 12},
		{"afternoon", 12, 17},
		{"evening", 17, 21},
	}
	today := dayStart(ref)
	for _, c := range cases {
		v, outcome, _ := Resolve(TypePartOfDay, cap1(c.tok), Spec{}, ref, Entities{})
		if outcome != Resolved {
			t.Fatalf("Resolve(%q) outcome = %v, want Resolved", c.tok, outcome)
		}
		wantStart := today.Add(time.Duration(c.startHour) * time.Hour)
		wantEnd := today.Add(time.Duration(c.endHour) * time.Hour)
		if !v.Start.Equal(wantStart) || !v.End.Equal(wantEnd) {
			t.Errorf("Resolve(%q) = [%v,%v), want [%v,%v)", c.tok, v.Start, v.End, wantStart, wantEnd)
		}
	}
}

func TestResolvePartOfDayUnresolved(t *testing.T) {
	_, outcome, _ := Resolve(TypePartOfDay, cap1("night"), Spec{}, ref, Entities{})
	if outcome != Unresolved {
		t.Errorf("outcome = %v, want Unresolved", outcome)
	}
}
