package tmpl

import (
	"strings"
	"testing"
	"time"
)

func mustCompile(tb testing.TB, src string, rules map[string]string, slotTypes map[string]string) *Template {
	tb.Helper()
	tmpl, err := Compile(src, rules, slotTypes)
	if err != nil {
		tb.Fatalf("Compile(%q) failed: %v", src, err)
	}
	return tmpl
}

func firstMatch(tmpl *Template, u Utterance) (Match, bool) {
	var got Match
	found := false
	tmpl.MatchAll(u, func(m Match) bool {
		got = m
		found = true
		return true
	})
	return got, found
}

func slotJoin(m Match, slot string) (string, bool) {
	for _, c := range m.Captures {
		if c.Slot == slot {
			return strings.Join(c.Tokens, " "), true
		}
	}
	return "", false
}

func TestGrammarMatching(t *testing.T) {
	slotTypes := map[string]string{"when": "date"}
	rules := map[string]string{"greeting": "(hi|hello)"}

	cases := []struct {
		name      string
		src       string
		utterance string
		want      bool
		wantSlots map[string]string
	}{
		{"alternatives", "(what's|what is|what have i got) on my calendar", "what is on my calendar", true, nil},
		{"nested alt in optional", "on [my|the] calendar", "on the calendar", true, nil},
		{"optional absent", "on [my] calendar", "on calendar", true, nil},
		{"optional present", "on [my] calendar", "on my calendar", true, nil},
		{"slot capture", "on my calendar for {when}", "on my calendar for tomorrow", true, map[string]string{"when": "tomorrow"}},
		{"rule expansion hi", "<greeting> there", "hi there", true, nil},
		{"rule expansion hello", "<greeting> there", "hello there", true, nil},
		{"apostrophe normalization", "what's up", "what’s up", true, nil},
		{"case normalization", "hello there", "HELLO THERE", true, nil},
		{"non match", "hello there", "goodbye now", false, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tmpl := mustCompile(t, c.src, rules, slotTypes)
			u := Normalize(c.utterance, nil)
			m, found := firstMatch(tmpl, u)
			if found != c.want {
				t.Fatalf("match=%v, want %v", found, c.want)
			}
			for slot, want := range c.wantSlots {
				got, ok := slotJoin(m, slot)
				if !ok || got != want {
					t.Fatalf("slot %s = %q (ok=%v), want %q", slot, got, ok, want)
				}
			}
		})
	}
}

func TestSkipWords(t *testing.T) {
	tmpl := mustCompile(t, "on my calendar", nil, nil)
	skip := map[string]bool{"please": true, "hey": true}
	u := Normalize("hey please on my calendar", skip)
	if _, found := firstMatch(tmpl, u); !found {
		t.Fatal("expected a match once skip words are dropped")
	}
	// Without skip words the same raw utterance must not match, proving
	// Normalize actually removed them rather than the grammar tolerating them.
	u2 := Normalize("hey please on my calendar", nil)
	if _, found := firstMatch(tmpl, u2); found {
		t.Fatal("expected no match without skip words configured")
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name      string
		src       string
		rules     map[string]string
		slotTypes map[string]string
	}{
		{"unbalanced open paren", "(hello", nil, nil},
		{"unbalanced open bracket", "[hello", nil, nil},
		{"stray close paren", "hello)", nil, nil},
		{"stray close bracket", "hello]", nil, nil},
		{"unknown rule", "<bar> there", nil, nil},
		{"undeclared slot", "{foo}", nil, nil},
		{"direct recursive rule", "<a>", map[string]string{"a": "<a>"}, nil},
		{"indirect recursive rule", "<a>", map[string]string{"a": "<b>", "b": "<a>"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Compile(c.src, c.rules, c.slotTypes); err == nil {
				t.Fatalf("Compile(%q) succeeded, want an error", c.src)
			}
		})
	}
}

func TestTextSlotMustBeLast(t *testing.T) {
	slotTypes := map[string]string{"body": "text", "who": "person"}

	if _, err := Compile("{body} now", nil, slotTypes); err == nil {
		t.Fatal("expected a compile error for a non-final text slot")
	}
	if _, err := Compile("reply to {who} saying {body} now", nil, slotTypes); err == nil {
		t.Fatal("expected a compile error for a text slot followed by more content")
	}
	if _, err := Compile("reply to {who} saying {body}", nil, slotTypes); err != nil {
		t.Fatalf("expected a template-final text slot to compile, got: %v", err)
	}
}

func TestCaptureRawPreservesCaseAndPunctuation(t *testing.T) {
	slotTypes := map[string]string{"who": "person", "body": "text"}
	tmpl := mustCompile(t, "reply to {who} saying {body}", nil, slotTypes)
	u := Normalize("reply to alex saying Sounds good, see you at 3!", nil)
	m, found := firstMatch(tmpl, u)
	if !found {
		t.Fatal("expected a match")
	}
	var body string
	for _, c := range m.Captures {
		if c.Slot == "body" {
			body = c.Raw
		}
	}
	want := "Sounds good, see you at 3!"
	if body != want {
		t.Fatalf("body raw = %q, want %q", body, want)
	}
}

func TestSpecificityOrdering(t *testing.T) {
	slotTypes := map[string]string{"when": "date"}
	tmpl := mustCompile(t, "[what's on] {when}", nil, slotTypes)
	u := Normalize("what's on tomorrow", nil)

	var literalCounts []int
	tmpl.MatchAll(u, func(m Match) bool {
		literalCounts = append(literalCounts, m.Literals)
		return false // keep enumerating
	})
	if len(literalCounts) < 2 {
		t.Fatalf("expected at least 2 candidate matches (optional present/absent), got %d", len(literalCounts))
	}
	min, max := literalCounts[0], literalCounts[0]
	for _, l := range literalCounts {
		if l < min {
			min = l
		}
		if l > max {
			max = l
		}
	}
	if max <= min {
		t.Fatalf("expected differing specificity across matches, got uniform Literals=%d", max)
	}
}

// TestStepBudgetExhaustion constructs a template whose optional prefix
// generates exponentially many backtracking combinations (2^20) against an
// utterance that never satisfies the trailing literal, forcing an
// exhaustive search that the step budget must cut short well before it
// would otherwise complete. A hang or an unbounded search time here would
// mean the budget isn't actually being enforced.
func TestStepBudgetExhaustion(t *testing.T) {
	var pat strings.Builder
	for i := 0; i < 20; i++ {
		pat.WriteString("[a] ")
	}
	pat.WriteString("b")
	tmpl := mustCompile(t, pat.String(), nil, nil)

	var utter strings.Builder
	for i := 0; i < 20; i++ {
		if i > 0 {
			utter.WriteString(" ")
		}
		utter.WriteString("a")
	}
	u := Normalize(utter.String(), nil) // deliberately no trailing "b"

	done := make(chan int, 1)
	go func() {
		n := 0
		tmpl.MatchAll(u, func(m Match) bool { n++; return false })
		done <- n
	}()
	select {
	case n := <-done:
		if n != 0 {
			t.Fatalf("expected no match, got %d", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("MatchAll did not return within 2s; the step budget does not appear to be bounding the search")
	}
}

func TestLiteralVocabulary(t *testing.T) {
	slotTypes := map[string]string{"when": "date"}
	tmpl := mustCompile(t, "(cancel|stop) [my] (meeting|event) [for] {when}", nil, slotTypes)
	vocab := tmpl.LiteralVocabulary()
	for _, w := range []string{"cancel", "stop", "my", "meeting", "event", "for"} {
		if !vocab[w] {
			t.Errorf("expected %q in literal vocabulary", w)
		}
	}
	if vocab["when"] {
		t.Errorf("slot name %q leaked into literal vocabulary", "when")
	}
}

func BenchmarkMatch(b *testing.B) {
	slotTypes := map[string]string{"when": "date"}
	tmpl := mustCompile(b, "(what's|what is|what have i got) on [my] (calendar|schedule) [for] {when}", nil, slotTypes)
	u := Normalize("what's on my calendar for tomorrow", nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tmpl.MatchAll(u, func(m Match) bool { return true })
	}
}
