package session

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestClearKeepsTranscript(t *testing.T) {
	s := New(t.TempDir(), "ceo")
	slug := s.Slugify("Plan the Q3 capital allocation", time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC))
	if slug != "plan-the-q3-capital-allocation-0914" {
		t.Fatalf("slug %q", slug)
	}
	if err := s.Create(slug, ""); err != nil {
		t.Fatal(err)
	}
	_ = s.Append(slug, Entry{Kind: KindUser, Turn: 1, Text: "hello"})
	_ = s.Append(slug, Entry{Kind: KindAssistant, Turn: 1, Text: "hi"})
	before, _ := os.ReadFile(s.Path(slug))
	// /clear resets the ACTIVE context in the app; the file must be untouched
	// and a fresh Open still sees everything.
	c, err := s.Open(slug)
	if err != nil || len(c.Entries) != 2 || c.Turns != 1 {
		t.Fatalf("%+v %v", c, err)
	}
	after, _ := os.ReadFile(s.Path(slug))
	if string(before) != string(after) {
		t.Fatal("open modified the transcript")
	}
	if _, err := s.Open("nope"); err == nil {
		t.Fatal("missing session should error")
	}
}

func TestPinnedSurvivesPrune(t *testing.T) {
	s := New(t.TempDir(), "cto")
	now := time.Now()
	for i, slug := range []string{"a", "b", "c", "d"} {
		_ = s.Create(slug, "")
		_ = s.Append(slug, Entry{Kind: KindUser, Turn: 1, Text: "x"})
		os.Chtimes(s.Path(slug), now.Add(-time.Duration(4-i)*time.Hour), now.Add(-time.Duration(4-i)*time.Hour))
	}
	if err := s.Pin("a", "keeper", true); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(s.Path("a"), now.Add(-4*time.Hour), now.Add(-4*time.Hour)) // still the oldest
	removed, err := s.Prune(Retention{Keep: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "b" {
		t.Fatalf("removed %v (want only b: a is pinned, c/d are the 2 newest)", removed)
	}
	if !s.Exists("a") {
		t.Fatal("pinned session was pruned")
	}
	// Age backstop.
	removed, _ = s.Prune(Retention{MaxAge: 90 * time.Minute}, now)
	if len(removed) != 1 || removed[0] != "c" {
		t.Fatalf("age prune removed %v", removed)
	}
	if !s.Exists("a") || !s.Exists("d") {
		t.Fatal("age prune touched pinned or fresh session")
	}
	infos, _ := s.List()
	if len(infos) != 2 || infos[1].Name != "keeper" || !infos[1].Pinned {
		t.Fatalf("list %+v", infos)
	}
}

// TestCompactionNoDeadlock — an oversized transcript compacts without the
// whole file being read: Open reads at most TailBytes from the end.
func TestCompactionNoDeadlock(t *testing.T) {
	s := New(t.TempDir(), "coo")
	slug := "huge"
	if err := s.Create(slug, ""); err != nil {
		t.Fatal(err)
	}
	// Write ~4 MiB directly (bypassing Append's per-line cost) with an early
	// summary and many turns after it.
	f, _ := os.OpenFile(s.Path(slug), os.O_APPEND|os.O_WRONLY, 0o600)
	line := `{"kind":"assistant","turn":%d,"text":"` + strings.Repeat("y", 900) + `"}` + "\n"
	for i := 1; i <= 4500; i++ {
		f.WriteString(strings.Replace(line, "%d", itoa(i), 1))
	}
	f.Close()
	st, _ := os.Stat(s.Path(slug))
	if st.Size() < 8*TailBytes {
		t.Fatalf("fixture too small to prove anything: %d", st.Size())
	}
	c, err := s.Open(slug)
	if err != nil {
		t.Fatal(err)
	}
	if c.ReadFrom == 0 {
		t.Fatal("open read from the start of an oversized file")
	}
	if c.Turns != 4500 || len(c.Entries) == 0 || len(c.Entries) > 700 {
		t.Fatalf("tail window wrong: turns=%d entries=%d", c.Turns, len(c.Entries))
	}
	calls := 0
	c2, err := s.Compact(slug, "decisions", func(c *Context, focus string) (string, error) {
		calls++
		if focus != "decisions" {
			t.Fatalf("focus %q", focus)
		}
		return "SUMMARY of " + itoa(len(c.Entries)) + " entries", nil
	})
	if err != nil || calls != 1 {
		t.Fatal(err)
	}
	if c2.Summary == nil || !strings.HasPrefix(c2.Summary.Text, "SUMMARY") || len(c2.Entries) != 0 {
		t.Fatalf("after compaction: %+v", c2)
	}
	// New turns after the checkpoint are the only ones loaded next time.
	_ = s.Append(slug, Entry{Kind: KindUser, Turn: 4501, Text: "next"})
	c3, _ := s.Open(slug)
	if len(c3.Entries) != 1 || c3.Summary == nil {
		t.Fatalf("post-compaction context: %d entries", len(c3.Entries))
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
