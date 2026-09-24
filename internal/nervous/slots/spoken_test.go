package slots

import "testing"

func TestSpokenDate(t *testing.T) {
	cases := []struct {
		toks []string
		want string
	}{
		{[]string{"today"}, "Today, " + spokenDate(dayStart(ref))},
		{[]string{"tomorrow"}, "Tomorrow, " + spokenDate(dayStart(ref).AddDate(0, 0, 1))},
		{[]string{weekdayToken(ref.Weekday())}, "Today, " + spokenDate(dayStart(ref))}, // bare weekday == today
		{[]string{"next", weekdayToken(ref.Weekday())}, spokenDate(dayStart(ref).AddDate(0, 0, 7))},
	}
	for _, c := range cases {
		v, outcome, _ := Resolve(TypeDate, capN(c.toks...), Spec{}, ref, Entities{})
		if outcome != Resolved {
			t.Fatalf("Resolve(%v) outcome = %v, want Resolved", c.toks, outcome)
		}
		if v.Spoken != c.want {
			t.Errorf("Resolve(%v).Spoken = %q, want %q", c.toks, v.Spoken, c.want)
		}
	}
}

func TestSpokenDateRangeNextWeek(t *testing.T) {
	v, outcome, _ := Resolve(TypeDateRange, capN("next", "week"), Spec{}, ref, Entities{})
	if outcome != Resolved {
		t.Fatalf("outcome = %v, want Resolved", outcome)
	}
	if v.Spoken != "next week" {
		t.Errorf("Spoken = %q, want %q", v.Spoken, "next week")
	}
}

func TestSpokenTime(t *testing.T) {
	v, outcome, _ := Resolve(TypeTime, cap1("3pm"), Spec{}, ref, Entities{})
	if outcome != Resolved {
		t.Fatalf("outcome = %v, want Resolved", outcome)
	}
	if v.Spoken != "3pm" {
		t.Errorf("Spoken = %q, want %q", v.Spoken, "3pm")
	}
}

func TestSpokenPartOfDay(t *testing.T) {
	v, outcome, _ := Resolve(TypePartOfDay, cap1("afternoon"), Spec{}, ref, Entities{})
	if outcome != Resolved {
		t.Fatalf("outcome = %v, want Resolved", outcome)
	}
	if v.Spoken != "this afternoon" {
		t.Errorf("Spoken = %q, want %q", v.Spoken, "this afternoon")
	}
}
