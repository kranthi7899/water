package slots

import "testing"

func TestResolveCount(t *testing.T) {
	min1, max20 := 1, 20
	spec := Spec{Min: &min1, Max: &max20}

	cases := []struct {
		tok  string
		want int
	}{
		{"1", 1}, {"5", 5}, {"20", 20},
		{"one", 1}, {"five", 5}, {"twenty", 20}, {"twelve", 12},
	}
	for _, c := range cases {
		v, outcome, _ := Resolve(TypeCount, cap1(c.tok), spec, ref, Entities{})
		if outcome != Resolved {
			t.Errorf("Resolve(%q) outcome = %v, want Resolved", c.tok, outcome)
			continue
		}
		if v.N != c.want {
			t.Errorf("Resolve(%q).N = %d, want %d", c.tok, v.N, c.want)
		}
	}
}

func TestResolveCountOutOfRange(t *testing.T) {
	min1, max20 := 1, 20
	spec := Spec{Min: &min1, Max: &max20}

	// Out of [min,max] is Unresolved, not clamped into range.
	for _, tok := range []string{"0", "21", "100"} {
		v, outcome, _ := Resolve(TypeCount, cap1(tok), spec, ref, Entities{})
		if outcome != Unresolved {
			t.Errorf("Resolve(%q) outcome = %v (N=%d), want Unresolved", tok, outcome, v.N)
		}
	}
}

func TestResolveCountNoBounds(t *testing.T) {
	v, outcome, _ := Resolve(TypeCount, cap1("15"), Spec{}, ref, Entities{})
	if outcome != Resolved || v.N != 15 {
		t.Errorf("Resolve(15) = (%d, %v), want (15, Resolved)", v.N, outcome)
	}
}

func TestResolveCountUnresolved(t *testing.T) {
	for _, tok := range []string{"a", "many", "twenty-one"} {
		_, outcome, _ := Resolve(TypeCount, cap1(tok), Spec{}, ref, Entities{})
		if outcome != Unresolved {
			t.Errorf("Resolve(%q) outcome = %v, want Unresolved", tok, outcome)
		}
	}
}
