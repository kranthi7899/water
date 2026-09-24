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

func TestSentenceSplitterDoesNotSplitMidNumber(t *testing.T) {
	var s SentenceSplitter
	got := s.Feed("The rate is 3.5 percent today.")
	got = append(got, s.Flush()...)
	want := []string{"The rate is 3.5 percent today."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
