package slots

import (
	"strconv"

	"water/internal/nervous/tmpl"
)

var numberWords = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
	"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
	"eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15,
	"sixteen": 16, "seventeen": 17, "eighteen": 18, "nineteen": 19, "twenty": 20,
}

// parseNumberWord parses a digit or a number word up to twenty.
func parseNumberWord(tok string) (int, bool) {
	if n, err := strconv.Atoi(tok); err == nil {
		return n, true
	}
	if n, ok := numberWords[tok]; ok {
		return n, true
	}
	return 0, false
}

// resolveCount parses a digit or number word, and treats a value outside
// spec's [Min,Max] bounds as Unresolved rather than clamping it.
func resolveCount(c tmpl.Capture, spec Spec) (Value, Outcome, []string) {
	if len(c.Tokens) != 1 {
		return Value{Type: TypeCount}, Unresolved, nil
	}
	n, ok := parseNumberWord(c.Tokens[0])
	if !ok {
		return Value{Type: TypeCount}, Unresolved, nil
	}
	if spec.Min != nil && n < *spec.Min {
		return Value{Type: TypeCount}, Unresolved, nil
	}
	if spec.Max != nil && n > *spec.Max {
		return Value{Type: TypeCount}, Unresolved, nil
	}
	label := strconv.Itoa(n)
	return Value{Type: TypeCount, Label: label, Spoken: label, N: n}, Resolved, nil
}
