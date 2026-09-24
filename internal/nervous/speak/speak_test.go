package speak

import (
	"strings"
	"testing"
)

func TestFlattenMarkdown(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"heading", "# Title", "Title."},
		{"bullet", "- one\n- two", "one.\ntwo."},
		{"emphasis", "this is **bold** and *also bold*", "this is bold and also bold."},
		{"quote", "> a quote", "a quote."},
		{"fence", "before\n```go\ncode\n```\nafter", "before.\n\n(code block omitted)\n\nafter."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FlattenMarkdown(c.in)
			if got != c.want {
				t.Errorf("FlattenMarkdown(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSpeakableLinksAndURLs(t *testing.T) {
	got := Speakable("see [the doc](https://example.com/doc) or https://example.com or www.example.com", Options{})
	for _, bad := range []string{"http://", "https://", "www."} {
		if strings.Contains(got, bad) {
			t.Errorf("Speakable left a raw URL fragment %q in %q", bad, got)
		}
	}
	if !strings.Contains(got, "the doc") {
		t.Errorf("Speakable dropped the markdown link's label: %q", got)
	}
	if strings.Count(got, "a link") != 2 {
		t.Errorf("want exactly 2 bare URLs replaced with \"a link\", got %q", got)
	}
}

func TestSpeakableEmoji(t *testing.T) {
	got := Speakable("great work \U0001F389 done ❤️", Options{})
	for _, r := range got {
		if r == '\U0001F389' || r == '❤' || r == '️' {
			t.Fatalf("emoji/variation-selector survived: %q", got)
		}
	}
	if !strings.Contains(got, "great work") || !strings.Contains(got, "done") {
		t.Fatalf("ordinary text was damaged: %q", got)
	}
}

func TestSpeakableTimes(t *testing.T) {
	cases := map[string]string{
		"the meeting is at 15:00":        "3 PM",
		"call at 3pm today":              "3 PM",
		"call at 3:00pm today":           "3 PM",
		"back by 3:30":                   "3:30 PM",
		"free from 3-4pm":                "3 to 4 PM",
		"starts at 9:15am":               "9:15 AM",
		"the standup is at 08:05 sharp":  "8:05 AM",
		"call at 22:00 tonight if ready": "10 PM",
	}
	for in, want := range cases {
		got := Speakable(in, Options{})
		if !strings.Contains(got, want) {
			t.Errorf("Speakable(%q) = %q, want it to contain %q", in, got, want)
		}
	}
}

func TestSpeakableDates(t *testing.T) {
	got := Speakable("event on 2026-09-25 at noon", Options{})
	if !strings.Contains(got, "Friday, September 25") {
		t.Errorf("ISO date not expanded correctly: %q", got)
	}
	got2 := Speakable("event on Thu 25 Sep at noon", Options{})
	if !strings.Contains(got2, "Thursday, September 25") {
		t.Errorf("short date not expanded correctly: %q", got2)
	}
}

func TestSpeakableListCap(t *testing.T) {
	in := "one\ntwo\nthree\nfour\nfive"
	got := Speakable(in, Options{MaxListItems: 2})
	if strings.Contains(got, "four") || strings.Contains(got, "five") {
		t.Errorf("list cap did not drop items beyond the cap: %q", got)
	}
	if !strings.Contains(got, "and 3 more.") {
		t.Errorf("list cap did not append the overflow count: %q", got)
	}
}

func TestSpeakableCharCap(t *testing.T) {
	in := "First sentence is here. Second sentence follows after that. Third one too."
	got := Speakable(in, Options{MaxChars: 30})
	if len(got) > 30 {
		t.Errorf("Speakable exceeded MaxChars=30: got %d chars: %q", len(got), got)
	}
	if !strings.HasSuffix(got, ".") {
		t.Errorf("char cap should land on a sentence boundary, got %q", got)
	}
	if !strings.HasPrefix(got, "First sentence") {
		t.Errorf("char cap should keep the start of the text, got %q", got)
	}
}

func TestSpeakableNoCapWhenZero(t *testing.T) {
	long := strings.Repeat("word ", 200)
	got := Speakable(long, Options{})
	if len(got) < len(long)-10 {
		t.Errorf("MaxChars=0 should mean no cap, but output shrank a lot: %d vs %d", len(got), len(long))
	}
}
