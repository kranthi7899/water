package gateway

import "testing"

func TestGist(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"normal punctuation", "We should renew now. There is more detail below.", "We should renew now."},
		{"question mark", "Can we move the deadline? Let me know.", "Can we move the deadline?"},
		{"exclamation", "Ship it! Everyone is ready.", "Ship it!"},
		{"empty body", "", ""},
		{"only whitespace", "   \n\t  ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Gist(c.body); got != c.want {
				t.Errorf("Gist(%q) = %q, want %q", c.body, got, c.want)
			}
		})
	}
}

// TestGistIgnoresMidWordPunctuation: a '.' not followed by whitespace or the
// end of the string (as in "10.5") is not a sentence end, matching the
// documented, deliberately literal rule.
func TestGistIgnoresMidWordPunctuation(t *testing.T) {
	if got := Gist("The price is 10.5 per unit."); got != "The price is 10.5 per unit." {
		t.Errorf("Gist = %q, want the whole sentence (the mid-word '.' must not end it early)", got)
	}
}

// TestGistFallbackTruncation: a body with no sentence-ending punctuation at
// all is cut to gistMaxLen runes, backed off to a whole word, and marked
// with "…".
func TestGistFallbackTruncation(t *testing.T) {
	long := ""
	for i := 0; i < 40; i++ {
		long += "word "
	}
	got := Gist(long)
	if got == long {
		t.Fatal("a long punctuation-free body must be truncated")
	}
	if got == "" || got[len(got)-len("…"):] != "…" {
		t.Errorf("Gist fallback = %q, want it to end with an ellipsis", got)
	}
	if r := []rune(got); len(r) > gistMaxLen+1 {
		t.Errorf("Gist fallback is %d runes, want at most about gistMaxLen", len(r))
	}
	// A short, punctuation-free body is returned whole.
	if got := Gist("no punctuation here"); got != "no punctuation here" {
		t.Errorf("short fallback = %q, want the body unchanged", got)
	}
}

func TestRiskPhrase(t *testing.T) {
	cases := map[string]string{
		"low":     riskPhrases["low"],
		"medium":  riskPhrases["medium"],
		"high":    riskPhrases["high"],
		"":        "Risk not rated",
		"unknown": "Risk not rated",
	}
	seen := map[string]bool{}
	for risk, want := range cases {
		got := RiskPhrase(risk)
		if got != want {
			t.Errorf("RiskPhrase(%q) = %q, want %q", risk, got, want)
		}
		seen[got] = true
	}
	// low/medium/high must be three genuinely distinct phrases.
	if len(seen) < 4 { // low, medium, high, "Risk not rated"
		t.Errorf("risk phrases are not sufficiently distinct: %v", seen)
	}
}

func TestInitials(t *testing.T) {
	cases := map[string]string{
		"Lee Chen":            "LC",
		"  Ada Lovelace  ":    "AL",
		"Cher":                "CH",
		"X":                   "X",
		"":                    "?",
		"   ":                 "?",
		"Madonna Jones Smith": "MS", // first and last word only
	}
	for name, want := range cases {
		if got := Initials(name); got != want {
			t.Errorf("Initials(%q) = %q, want %q", name, got, want)
		}
	}
}
