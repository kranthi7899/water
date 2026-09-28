package slots

import (
	"testing"
	"time"
)

func TestResolveDurationAccepted(t *testing.T) {
	cases := []struct {
		toks []string
		want time.Duration
	}{
		{[]string{"15", "minutes"}, 15 * time.Minute},
		{[]string{"30", "minutes"}, 30 * time.Minute},
		{[]string{"45", "minutes"}, 45 * time.Minute},
		{[]string{"90", "minutes"}, 90 * time.Minute},
		{[]string{"half", "an", "hour"}, 30 * time.Minute},
		{[]string{"an", "hour"}, time.Hour},
		{[]string{"1", "hour"}, time.Hour},
		{[]string{"2", "hours"}, 2 * time.Hour},
		{[]string{"hour", "and", "a", "half"}, 90 * time.Minute},
		{[]string{"5", "minutes"}, 5 * time.Minute}, // lower bound, inclusive
		{[]string{"4", "hours"}, 4 * time.Hour},     // upper bound, inclusive
	}
	for _, c := range cases {
		v, outcome, _ := Resolve(TypeDuration, capN(c.toks...), Spec{}, ref, Entities{})
		if outcome != Resolved {
			t.Errorf("Resolve(%v) outcome = %v, want Resolved", c.toks, outcome)
			continue
		}
		if v.Dur != c.want {
			t.Errorf("Resolve(%v).Dur = %v, want %v", c.toks, v.Dur, c.want)
		}
	}
}

func TestResolveDurationOutOfRange(t *testing.T) {
	// Below the 5-minute floor and above the 4-hour ceiling are Unresolved,
	// not clamped.
	cases := [][]string{
		{"2", "minutes"},
		{"5", "hours"},
		{"1", "minute"},
	}
	for _, toks := range cases {
		_, outcome, _ := Resolve(TypeDuration, capN(toks...), Spec{}, ref, Entities{})
		if outcome != Unresolved {
			t.Errorf("Resolve(%v) outcome = %v, want Unresolved", toks, outcome)
		}
	}
}

func TestResolveDurationUnresolved(t *testing.T) {
	cases := [][]string{
		{"a", "while"},
		{"forever"},
		{"two", "days"},
	}
	for _, toks := range cases {
		_, outcome, _ := Resolve(TypeDuration, capN(toks...), Spec{}, ref, Entities{})
		if outcome != Unresolved {
			t.Errorf("Resolve(%v) outcome = %v, want Unresolved", toks, outcome)
		}
	}
}
