package meetings

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"water/internal/store"
)

func newHelpManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st), st
}

func TestHelpReturnsOnlyRecentSegmentsAndIsAlwaysTainted(t *testing.T) {
	m, _ := newHelpManager(t)
	ctx := context.Background()
	s, err := m.Start(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)
	old := Segment{At: now.Add(-10 * time.Minute), Channel: System, Text: "ancient unrelated remark"}
	recent := Segment{At: now.Add(-1 * time.Minute), Channel: Mic, Text: "how's the kafka budget looking"}
	for _, seg := range []Segment{old, recent} {
		if err := m.AddSegment(ctx, s.ID, seg); err != nil {
			t.Fatal(err)
		}
	}
	hc, err := m.Help(ctx, s.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if !hc.Tainted {
		t.Fatal("help context must always be tainted: meeting speech is untrusted unconditionally")
	}
	if len(hc.Segments) != 1 || hc.Segments[0].Text != recent.Text {
		t.Fatalf("segments = %+v, want only the one inside RecentWindow", hc.Segments)
	}
}

// TestHelpTaintedEvenWithNoSegmentsYet is the on-demand-help side of Slice
// M's core invariant: finding a live meeting session at all is enough to
// taint the turn, even before any segment has been posted for it.
func TestHelpTaintedEvenWithNoSegmentsYet(t *testing.T) {
	m, _ := newHelpManager(t)
	ctx := context.Background()
	s, err := m.Start(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	hc, err := m.Help(ctx, s.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !hc.Tainted || len(hc.Segments) != 0 {
		t.Fatalf("hc = %+v, want tainted with zero segments", hc)
	}
}

func TestHelpUnknownSessionIsNotFound(t *testing.T) {
	m, _ := newHelpManager(t)
	if _, err := m.Help(context.Background(), "mtg_nope", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestHelpFTSFallbackFindsLocalMessagesAndDocuments exercises section 4's
// "local data first" retrieval fallback: a local message and document the
// transcript references by word, but doesn't itself contain, both surface.
func TestHelpFTSFallbackFindsLocalMessagesAndDocuments(t *testing.T) {
	m, st := newHelpManager(t)
	ctx := context.Background()
	if err := st.Upsert(ctx, &store.Message{
		Meta: store.Meta{Source: "gmail", SourceID: "m1", External: true},
		From: "priya@acme.com", Subject: "Compute", Body: "The Kafka budgets are over by 20%",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Upsert(ctx, &store.Document{
		Meta:  store.Meta{Source: "gdrive", SourceID: "d1", External: true},
		Title: "Kafka budget FY27", Excerpt: "broker costs",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Upsert(ctx, &store.Message{
		Meta: store.Meta{Source: "gmail", SourceID: "m2", External: true},
		From: "dana@acme.com", Subject: "Lunch", Body: "tacos?",
	}); err != nil {
		t.Fatal(err)
	}
	s, err := m.Start(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AddSegment(ctx, s.ID, Segment{Channel: Mic, Text: "what did priya say about the kafka budget"}); err != nil {
		t.Fatal(err)
	}
	hc, err := m.Help(ctx, s.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(hc.Messages) != 1 || hc.Messages[0].SourceID != "m1" {
		t.Fatalf("messages = %+v, want only m1 (the unrelated lunch message must not match)", hc.Messages)
	}
	if len(hc.Documents) != 1 || hc.Documents[0].SourceID != "d1" {
		t.Fatalf("documents = %+v", hc.Documents)
	}
	rendered := RenderHelpContext(hc)
	if !strings.Contains(rendered, "kafka budget") {
		t.Fatalf("rendered context missing the transcript line verbatim: %q", rendered)
	}
	if !strings.Contains(rendered, "priya@acme.com") || !strings.Contains(rendered, "Kafka budget FY27") {
		t.Fatalf("rendered context missing the retrieved local items: %q", rendered)
	}
}

func TestFTSQueryQuotesTermsAndSkipsStopwordsAndShortWords(t *testing.T) {
	segs := []Segment{{Text: "what is the budget for the new GPU cluster"}}
	q := ftsQuery(segs)
	if q == "" {
		t.Fatal("expected a non-empty query")
	}
	if strings.Contains(q, `"the"`) || strings.Contains(q, `"is"`) {
		t.Fatalf("query = %q, stopword/short word leaked in", q)
	}
	if !strings.Contains(q, `"budget"`) || !strings.Contains(q, `"cluster"`) {
		t.Fatalf("query = %q, missing expected quoted terms", q)
	}
}

func TestFTSQueryEmptyForNoSegments(t *testing.T) {
	if q := ftsQuery(nil); q != "" {
		t.Fatalf("query = %q, want empty", q)
	}
}

// TestFTSQuerySurvivesHostileText: transcript and stored text full of FTS5
// query syntax never produce a MATCH error (Help would swallow one, so this
// calls the store directly with the query Help builds).
func TestFTSQuerySurvivesHostileText(t *testing.T) {
	_, st := newHelpManager(t)
	ctx := context.Background()
	hostile := `budget" OR NEAR(kafka* "x") AND NOT {subject}: ^col - it's 100% "unterminated`
	if err := st.Upsert(ctx, &store.Message{
		Meta: store.Meta{Source: "gmail", SourceID: "m1", External: true},
		From: `"eve" <e@x.com>`, Subject: hostile, Body: "kafka budget",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Upsert(ctx, &store.Document{Meta: store.Meta{Source: "gdrive", SourceID: "d1", External: true}, Title: hostile}); err != nil {
		t.Fatal(err)
	}
	q := ftsQuery([]Segment{{Text: hostile}, {Text: `AND OR NOT NEAR "" '' ** ::`}})
	if q == "" {
		t.Fatal("expected a query")
	}
	msgs, err := st.SearchMessages(ctx, q, 5)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("SearchMessages(%q) = %+v, %v", q, msgs, err)
	}
	if _, err := st.SearchDocuments(ctx, q, 5); err != nil {
		t.Fatalf("SearchDocuments(%q): %v", q, err)
	}
}
