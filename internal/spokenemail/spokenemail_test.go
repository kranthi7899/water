package spokenemail

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestCandidates(t *testing.T) {
	cases := []struct {
		name, in string
		want     []string // exact result; nil means none
		contains string   // when set, only this must be among the results
	}{
		// The incident (2026-09-26): Parakeet heard Indian-English "at the
		// rate" as "at the right", glued or not.
		{name: "incident glued", in: "The correct email address is Kranthetjob at the rightgmail.com.", want: []string{"kranthetjob@gmail.com"}},
		{name: "incident words", in: "draft a mail to kranti get a job at the right gmail dot com", contains: "krantigetajob@gmail.com"},
		{name: "incident words exact", in: "draft a mail to kranti get a job at the right gmail dot com", want: []string{"krantigetajob@gmail.com", "job@gmail.com"}},
		// The owner's eval phrasing (2026-09-26): a spelled initial plus four
		// heard words before the short last word.
		{name: "eval initial plus words", in: "Draft a mail to K Kranti get a job at the rate gmail dot com saying I'll call tomorrow.", want: []string{"kkrantigetajob@gmail.com", "job@gmail.com"}},
		{name: "five words is too many", in: "draft a mail to one two three four five six at the rate gmail dot com", want: []string{"six@gmail.com"}},
		{name: "at the rate", in: "my mail id is kranthi at the rate gmail dot com", want: []string{"kranthi@gmail.com"}},
		{name: "at the red", in: "send it to dana at the red outlook dot com", want: []string{"dana@outlook.com"}},
		{name: "at rate", in: "email is ravi at rate yahoo dot com", want: []string{"ravi@yahoo.com"}},
		{name: "indian co in", in: "kranthi underscore k at the rate yahoo dot co dot in", want: []string{"kranthi_k@yahoo.co.in"}},
		{name: "indian dot local", in: "my email id is kranthi dot k at the rate gmail dot com", want: []string{"kranthi.k@gmail.com"}},
		{name: "spelled letters", in: "k r a n t h i at gmail dot com", want: []string{"kranthi@gmail.com"}},
		{name: "spelled with digits", in: "send it to r a v i 9 9 at gmail dot com", want: []string{"ravi99@gmail.com"}},
		{name: "underscore", in: "john underscore doe at outlook dot com", want: []string{"john_doe@outlook.com"}},
		{name: "dash", in: "email mary dash ann at hotmail dot com", want: []string{"mary-ann@hotmail.com"}},
		{name: "hyphen", in: "write to mary hyphen ann at icloud dot com", want: []string{"mary-ann@icloud.com"}},
		{name: "us dot local", in: "email john dot smith at outlook dot com", want: []string{"john.smith@outlook.com"}},
		{name: "us company domain", in: "send the deck to dana at fenwick dot io", want: []string{"dana@fenwick.io"}},
		{name: "g mail split", in: "email priya at g mail dot com", want: []string{"priya@gmail.com"}},
		{name: "typed address", in: "Send it to dana@fenwick.io please.", want: []string{"dana@fenwick.io"}},
		{name: "gmail.com glued", in: "email her at priya at gmail.com", want: []string{"priya@gmail.com"}},
		{name: "two addresses", in: "email dana at fenwick dot io and cc ravi at gmail dot com", want: []string{"dana@fenwick.io", "ravi@gmail.com"}},
		{name: "dedupe", in: "email dana at fenwick dot io, yes dana at fenwick dot io", want: []string{"dana@fenwick.io"}},
		{name: "trailing punctuation", in: "The address is ravi at gmail dot com.", want: []string{"ravi@gmail.com"}},

		// No false positives on ordinary sentences.
		{name: "meet at office", in: "meet at the office at 5"},
		{name: "rate alone", in: "at the rate"},
		{name: "rate of interest", in: "email me the loan terms at the rate of five dot two percent"},
		{name: "article at site", in: "check out the article at nytimes.com"},
		{name: "look at", in: "take a look at the numbers dot"},
		{name: "right place", in: "we are at the right place"},
		{name: "at noon", in: "send the draft at noon"},
		{name: "empty", in: ""},
		{name: "dot only", in: "be there on the dot"},
		{name: "percent", in: "revenue grew at 5.2 percent"},
		{name: "me at", in: "send it to me at 3 dot 30"},
		{name: "stopword local", in: "email the team, I will be at google.com tomorrow"},
		{name: "bare at no domain", in: "draft a mail to kranthi at gmail"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Candidates(c.in)
			if c.contains != "" {
				if !slices.Contains(got, c.contains) {
					t.Fatalf("Candidates(%q) = %v, want it to contain %q", c.in, got, c.contains)
				}
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Candidates(%q) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestCandidatesCap(t *testing.T) {
	got := Candidates("email a1 at x dot io, b1 at x dot io, c1 at x dot io, d1 at x dot io")
	if len(got) != maxCandidates {
		t.Fatalf("got %v, want %d candidates", got, maxCandidates)
	}
}

func TestCandidatesNeverInventTheGluedDomain(t *testing.T) {
	for _, in := range []string{
		"Kranthetjob at the rightgmail.com",
		"kranti get a job at the right gmail dot com",
		"kranthi at the right gmail.com",
	} {
		for _, a := range Candidates(in) {
			if strings.Contains(a, "right") {
				t.Fatalf("Candidates(%q) = %v: the misheard 'at the right' leaked into a domain", in, a)
			}
		}
	}
}

func TestSpellOut(t *testing.T) {
	cases := map[string]string{
		"kranthi@gmail.com":             "k r a n t h i at gmail dot com",
		"john_doe@outlook.com":          "j o h n underscore d o e at outlook dot com",
		"ravi.99@yahoo.com":             "r a v i dot 9 9 at yahoo dot com",
		"mary-ann+x@icloud.com":         "m a r y dash a n n plus x at icloud dot com",
		"kranthetjob@therightgmail.com": "k r a n t h e t j o b at t h e r i g h t g m a i l dot com",
		"dana@fenwick.io":               "d a n a at f e n w i c k dot i o",
		"Dana@Fenwick.ORG":              "d a n a at f e n w i c k dot org",
	}
	for in, want := range cases {
		if got := SpellOut(in); got != want {
			t.Errorf("SpellOut(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckSyntax(t *testing.T) {
	good := []string{"a@b.co", "kranthi@gmail.com", "john.doe+x@mail.fenwick.io", "o'neil@x.org", "Dana@Fenwick.IO"}
	bad := []string{"", "kranthi", "kranthi@", "@gmail.com", "a@b", "a@b.c", "a@@b.com", "a b@c.com", "a@b c.com",
		".a@b.com", "a.@b.com", "a..b@c.com", "a@-b.com", "a@b-.com", "a@b.c0m", "a@b..com", "Dana <dana@x.com>",
		strings.Repeat("a", 65) + "@x.com"}
	for _, a := range good {
		if err := CheckSyntax(a); err != nil {
			t.Errorf("CheckSyntax(%q) = %v, want ok", a, err)
		}
	}
	for _, a := range bad {
		if CheckSyntax(a) == nil {
			t.Errorf("CheckSyntax(%q) = ok, want an error", a)
		}
	}
}

func TestNearMiss(t *testing.T) {
	known := append(KnownProviders(), "renaissance.ai")
	cases := []struct {
		domain, match string
		near          bool
	}{
		{"therightgmail.com", "gmail.com", true}, // the incident: 8 edits, caught by the glued rule
		{"gmial.com", "gmail.com", true},
		{"gmail.co", "gmail.com", true},
		{"gmail.cm", "gmail.com", true},
		{"gmail.c", "gmail.com", true},
		{"gmaill.com", "gmail.com", true},
		{"outlok.com", "outlook.com", true},
		{"outlook.co", "outlook.com", true},
		{"hotmial.com", "hotmail.com", true},
		{"yahooo.com", "yahoo.com", true},
		{"icloud.co", "icloud.com", true},
		{"protn.me", "proton.me", true},
		{"myyahoo.com", "yahoo.com", true},
		{"renaisance.ai", "renaissance.ai", true},
		{"renaissance.io", "renaissance.ai", true},
		{"GMIAL.COM", "gmail.com", true},

		{"gmail.com", "", false},
		{"Gmail.com", "", false},
		{"mail.google.com", "", false}, // a real subdomain: preceded by "."
		{"renaissance.ai", "", false},
		{"eng.renaissance.ai", "", false},
		{"yahoo.co.in", "", false},
		{"hotmail.co.uk", "", false},
		{"outlook.in", "", false},
		{"mail.com", "", false},
		{"email.com", "", false},
		{"ymail.com", "", false},
		{"fenwick.io", "", false},
		{"acme.com", "", false}, // ends in me.com: short known domains match exactly only
		{"ms.com", "", false},
		{"aon.com", "", false},
		{"olive.com", "", false},
		{"umass.edu", "", false},
		{"google.com", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, near := NearMiss(c.domain, known)
		if near != c.near || got != c.match {
			t.Errorf("NearMiss(%q) = (%q, %v), want (%q, %v)", c.domain, got, near, c.match, c.near)
		}
	}
}

func TestCheckRecipient(t *testing.T) {
	known := KnownProviders()
	// The incident address is refused as a near-miss of gmail.com, with a
	// message the model can act on.
	_, err := CheckRecipient("kranthetjob@therightgmail.com", known, false)
	if !errors.Is(err, ErrNearMiss) {
		t.Fatalf("incident address: err = %v, want ErrNearMiss", err)
	}
	for _, want := range []string{`"gmail.com"`, "spelled out", "confirm_unusual_recipient", "k r a n t h e t j o b at"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err, want)
		}
	}
	// Confirmed: a warning, not a refusal.
	w, err := CheckRecipient("kranthetjob@therightgmail.com", known, true)
	if err != nil || !strings.Contains(w, "therightgmail.com") || !strings.Contains(w, "gmail.com") {
		t.Fatalf("confirmed: (%q, %v), want a warning naming both domains", w, err)
	}
	// Syntax is refused even when "confirmed".
	if _, err := CheckRecipient("kranthi at gmail", known, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad syntax: err = %v, want ErrInvalid", err)
	}
	// Ordinary addresses, including a display-name form, pass silently.
	for _, a := range []string{"kranthi@gmail.com", "Dana Lee <dana@fenwick.io>", "ravi@yahoo.co.in"} {
		if w, err := CheckRecipient(a, known, false); w != "" || err != nil {
			t.Errorf("CheckRecipient(%q) = (%q, %v), want clean", a, w, err)
		}
	}
}

func TestDistance(t *testing.T) {
	cases := []struct {
		a, b string
		d    int
	}{
		{"gmail.com", "gmail.com", 0}, {"gmial.com", "gmail.com", 1}, {"therightgmail.com", "gmail.com", 8},
		{"", "abc", 3}, {"ca", "ac", 1}, {"outlok.com", "outlook.com", 1},
	}
	for _, c := range cases {
		if got := distance(c.a, c.b); got != c.d {
			t.Errorf("distance(%q, %q) = %d, want %d", c.a, c.b, got, c.d)
		}
	}
}
