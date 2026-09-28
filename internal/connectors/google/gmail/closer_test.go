package gmail

import "testing"

// TestStripCloserVariants covers docs/slices/BRAND.md task 6's exact named
// cases: each of the four closer words, case-insensitive, with and without
// a trailing comma, and the two-line "closer + name" form (the brief's own
// "Thank you,\nKranthi" example, plus a couple of siblings).
func TestStripCloserVariants(t *testing.T) {
	cases := []struct{ in, want string }{
		{"See you soon.\n\nThanks,", "See you soon."},
		{"See you soon.\n\nThanks", "See you soon."},
		{"See you soon.\n\nthanks,", "See you soon."},
		{"See you soon.\n\nTHANKS,", "See you soon."},
		{"See you soon.\n\nThank you,", "See you soon."},
		{"See you soon.\n\nthank you", "See you soon."},
		{"See you soon.\n\nBest,", "See you soon."},
		{"See you soon.\n\nbest", "See you soon."},
		{"See you soon.\n\nRegards,", "See you soon."},
		{"See you soon.\n\nREGARDS", "See you soon."},
		// The brief's own two-line example, exactly.
		{"See you soon.\n\nThank you,\nKranthi", "See you soon."},
		{"See you soon.\n\nBest,\nKranthi Koneti", "See you soon."},
		{"See you soon.\n\nRegards,\nAlex", "See you soon."},
		// A blank line between the closer and the name is still one closer.
		{"See you soon.\n\nThanks,\n\nKranthi", "See you soon."},
		// Trailing blank lines/whitespace after the name don't confuse it.
		{"See you soon.\n\nBest,\nKranthi\n\n", "See you soon."},
	}
	for _, c := range cases {
		if got := stripCloser(c.in); got != c.want {
			t.Errorf("stripCloser(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestStripCloserDoesNotMangleMidSentenceUsage is the brief's explicit
// "don't over-strip" requirement: a body that legitimately ends with one of
// the closer words as part of a real sentence, not as its own standalone
// line, must be left untouched.
func TestStripCloserDoesNotMangleMidSentenceUsage(t *testing.T) {
	cases := []string{
		"I just wanted to say a huge thanks.",
		"I really can't say thanks enough for this",
		"Regards to your family, see you at the reunion.",
		"That's the best I can do for now.",
		"My personal best, so far, was last year's number.",
		"Thanks a lot,", // more than just the closer word on the line
		"Thanks for everything you've done for the team",
	}
	for _, in := range cases {
		if got := stripCloser(in); got != in {
			t.Errorf("stripCloser mangled a legitimate line: stripCloser(%q) = %q", in, got)
		}
	}
}

// TestStripCloserNoCloserLeavesBodyUnchanged covers the common case: a body
// with no trailing closer at all passes through byte for byte.
func TestStripCloserNoCloserLeavesBodyUnchanged(t *testing.T) {
	in := "Here's the Q3 update.\n\nRevenue is up 12% quarter over quarter."
	if got := stripCloser(in); got != in {
		t.Errorf("stripCloser(%q) = %q, want unchanged", in, got)
	}
}
