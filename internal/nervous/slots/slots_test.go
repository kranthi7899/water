package slots

import "testing"

func TestResolveText(t *testing.T) {
	c := capN("sounds", "good")
	c.Raw = "Sounds good, see you at 3!"
	v, outcome, _ := Resolve(TypeText, c, Spec{}, ref, Entities{})
	if outcome != Resolved {
		t.Fatalf("outcome = %v, want Resolved", outcome)
	}
	if v.Text != "Sounds good, see you at 3!" {
		t.Errorf("Text = %q, want original raw text preserved", v.Text)
	}
}

func TestResolveTextEmpty(t *testing.T) {
	c := capN()
	c.Raw = "   "
	_, outcome, _ := Resolve(TypeText, c, Spec{}, ref, Entities{})
	if outcome != Unresolved {
		t.Errorf("outcome = %v, want Unresolved", outcome)
	}
}

func TestResolveProjectAlwaysUnresolved(t *testing.T) {
	_, outcome, _ := Resolve(TypeProject, cap1("water"), Spec{}, ref, Entities{})
	if outcome != Unresolved {
		t.Errorf("outcome = %v, want Unresolved (project resolution is reserved)", outcome)
	}
}

func TestResolveString(t *testing.T) {
	v, outcome, _ := ResolveString(TypeDate, "tomorrow", Spec{}, ref, Entities{})
	if outcome != Resolved {
		t.Fatalf("outcome = %v, want Resolved", outcome)
	}
	want := dayStart(ref).AddDate(0, 0, 1)
	if !v.Start.Equal(want) {
		t.Errorf("Start = %v, want %v", v.Start, want)
	}
}

func TestResolveStringTextKeepsCasing(t *testing.T) {
	v, outcome, _ := ResolveString(TypeText, "Sounds Good!", Spec{}, ref, Entities{})
	if outcome != Resolved {
		t.Fatalf("outcome = %v, want Resolved", outcome)
	}
	if v.Text != "Sounds Good!" {
		t.Errorf("Text = %q, want original casing preserved", v.Text)
	}
}
