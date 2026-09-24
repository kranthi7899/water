package runtime

import (
	"reflect"
	"testing"
)

func TestSentenceSplitterFeedAndFlush(t *testing.T) {
	var s SentenceSplitter
	var got []string
	for _, chunk := range []string{"Hel", "lo. How are", " you? I'm", " fine"} {
		got = append(got, s.Feed(chunk)...)
	}
	got = append(got, s.Flush()...)
	want := []string{"Hello.", "How are you?", "I'm fine"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// TestSentenceSplitterKeepsListMarkersAndAbbreviations is the review
// finding: "1.", "2.", "e.g." and "Mr." were emitted as sentences of their
// own, so TTS read a bare "2.". Checked fed whole and rune by rune.
func TestSentenceSplitterKeepsListMarkersAndAbbreviations(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"Three things: 1. Call Dana. 2. Review e.g. the deck.", []string{"Three things: 1. Call Dana.", "2. Review e.g. the deck."}},
		{"Mr. Smith agreed. Dr. Lee did not.", []string{"Mr. Smith agreed.", "Dr. Lee did not."}},
		{"It is 3.5 million. That is final.", []string{"It is 3.5 million.", "That is final."}},
		{"Revenue grew to 42. Costs fell.", []string{"Revenue grew to 42.", "Costs fell."}},
		{"We met Acme Inc. yesterday. Good call!", []string{"We met Acme Inc. yesterday.", "Good call!"}},
		{"Plan:\na. Wait.\nb. Ship.", []string{"Plan:\na. Wait.", "b. Ship."}},
	} {
		var whole SentenceSplitter
		got := append(whole.Feed(tc.in), whole.Flush()...)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("whole %q:\n got %#v\nwant %#v", tc.in, got, tc.want)
		}
		var byRune SentenceSplitter
		got = nil
		for _, r := range tc.in {
			got = append(got, byRune.Feed(string(r))...)
		}
		got = append(got, byRune.Flush()...)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("by rune %q:\n got %#v\nwant %#v", tc.in, got, tc.want)
		}
	}
}

func TestSentenceSplitterDoesNotSplitMidNumber(t *testing.T) {
	var s SentenceSplitter
	got := s.Feed("The rate is 3.5 percent today.")
	got = append(got, s.Flush()...)
	want := []string{"The rate is 3.5 percent today."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
