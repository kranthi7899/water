package session

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSliceReviewTailPreservesActiveContext(t *testing.T) {
	s := New(t.TempDir(), "ceo")
	if err := s.Create("review-tail", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Append("review-tail", Entry{Kind: KindSummary, Turn: 40, Text: "Decision: never delete the customer records."}); err != nil {
		t.Fatal(err)
	}
	for i := 41; i <= 60; i++ {
		if err := s.Append("review-tail", Entry{Kind: KindAssistant, Turn: i, Text: strings.Repeat("x", 32*1024)}); err != nil {
			t.Fatal(err)
		}
	}
	begin := time.Now()
	c, err := s.Open("review-tail")
	if err != nil {
		t.Logf("explicit refusal is safe: %v", err)
		return
	}
	st, _ := os.Stat(s.Path("review-tail"))
	t.Logf("file=%d bytes, Open=%s, summary present=%t, entries=%d/20, tail offset=%d", st.Size(), time.Since(begin), c.Summary != nil, len(c.Entries), c.ReadFrom)
	if c.Summary == nil || len(c.Entries) != 20 {
		t.Fatal("Open silently discarded the last summary and active turns before the 40-turn compaction interval")
	}
}

func TestSliceReviewOversizedLatestTurn(t *testing.T) {
	s := New(t.TempDir(), "ceo")
	if err := s.Create("review-large", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Append("review-large", Entry{Kind: KindUser, Turn: 1, Text: strings.Repeat("x", TailBytes+1024)}); err != nil {
		t.Fatal(err)
	}
	c, err := s.Open("review-large")
	if err != nil {
		t.Logf("explicit refusal is safe: %v", err)
		return
	}
	t.Logf("latest turn accepted by Append; Open entries=%d, turns=%d", len(c.Entries), c.Turns)
	if len(c.Entries) != 1 || c.Turns != 1 {
		t.Fatal("latest accepted user turn silently disappeared on Open")
	}
}

func BenchmarkSliceReviewSessionOpen(b *testing.B) {
	for _, mib := range []int{1, 16} {
		b.Run(fmt.Sprintf("%dMiB", mib), func(b *testing.B) {
			s := New(b.TempDir(), "ceo")
			if err := s.Create("bench", ""); err != nil {
				b.Fatal(err)
			}
			for i := 0; i < mib*256; i++ {
				if err := s.Append("bench", Entry{Kind: KindAssistant, Turn: i + 1, Text: strings.Repeat("x", 4096)}); err != nil {
					b.Fatal(err)
				}
			}
			if err := s.Append("bench", Entry{Kind: KindSummary, Turn: mib * 256, Text: "latest summary"}); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.Open("bench"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
