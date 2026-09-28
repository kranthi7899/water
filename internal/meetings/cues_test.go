package meetings

import (
	"context"
	"errors"
	"testing"
	"time"

	"water/internal/store"
)

func TestCuesFindsRelatedItemsFromRecentSegment(t *testing.T) {
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
	s, err := m.Start(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)
	if err := m.AddSegment(ctx, s.ID, Segment{At: now, Channel: System, Text: "what about the kafka budget"}); err != nil {
		t.Fatal(err)
	}
	cs, err := m.Cues(ctx, s.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if !cs.Tainted {
		t.Fatal("cues must be tainted: they read live meeting content")
	}
	if len(cs.Items) == 0 || len(cs.Items) > CueLimit {
		t.Fatalf("items = %+v, want 1-%d", cs.Items, CueLimit)
	}
	var sawMsg, sawDoc bool
	for _, it := range cs.Items {
		switch {
		case it.Kind == "message" && it.Ref == "m1":
			sawMsg = true
		case it.Kind == "document" && it.Ref == "d1":
			sawDoc = true
		}
	}
	if !sawMsg || !sawDoc {
		t.Fatalf("items = %+v, want both the local message and document", cs.Items)
	}
}

func TestCuesEmptyWithNoRecentSegmentsOrNoMatch(t *testing.T) {
	m, _ := newHelpManager(t)
	ctx := context.Background()
	s, err := m.Start(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)
	cs, err := m.Cues(ctx, s.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if !cs.Tainted || len(cs.Items) != 0 {
		t.Fatalf("cs = %+v, want tainted with zero items", cs)
	}

	if err := m.AddSegment(ctx, s.ID, Segment{At: now, Channel: Mic, Text: "totally unrelated smalltalk about lunch"}); err != nil {
		t.Fatal(err)
	}
	cs, err = m.Cues(ctx, s.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Items) != 0 {
		t.Fatalf("items = %+v, want none: nothing local matches", cs.Items)
	}
}

func TestCuesOnlyLooksAtTheRecentWindow(t *testing.T) {
	m, st := newHelpManager(t)
	ctx := context.Background()
	if err := st.Upsert(ctx, &store.Document{
		Meta:  store.Meta{Source: "gdrive", SourceID: "d1", External: true},
		Title: "Kafka budget FY27",
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Minute)
	clock := now.Add(-10 * time.Minute)
	useClock(m, &clock)
	s, err := m.Start(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	clock = old // it arrived two minutes ago
	if err := m.AddSegment(ctx, s.ID, Segment{At: old, Channel: Mic, Text: "the kafka budget came up earlier"}); err != nil {
		t.Fatal(err)
	}
	cs, err := m.Cues(ctx, s.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Items) != 0 {
		t.Fatalf("items = %+v, want none: that segment is outside CueWindow", cs.Items)
	}
}

// TestCuesRateLimited is section 6's rate cap: an identical follow-up batch,
// or one requested too soon after the last non-empty one, is suppressed.
func TestCuesRateLimited(t *testing.T) {
	m, st := newHelpManager(t)
	ctx := context.Background()
	if err := st.Upsert(ctx, &store.Document{
		Meta: store.Meta{Source: "gdrive", SourceID: "d1", External: true}, Title: "Kafka budget FY27",
	}); err != nil {
		t.Fatal(err)
	}
	s, err := m.Start(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)
	if err := m.AddSegment(ctx, s.ID, Segment{At: now, Channel: Mic, Text: "the kafka budget again"}); err != nil {
		t.Fatal(err)
	}
	first, err := m.Cues(ctx, s.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) == 0 {
		t.Fatal("expected the first non-empty batch")
	}
	// Immediately again: same content, well inside CueMinInterval.
	again, err := m.Cues(ctx, s.ID, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Items) != 0 {
		t.Fatalf("items = %+v, want suppressed (rate-limited/duplicate)", again.Items)
	}
	// Well after the window, same content: still suppressed as a repeat of
	// the last shown batch.
	later, err := m.Cues(ctx, s.ID, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(later.Items) != 0 {
		t.Fatalf("items = %+v, want suppressed: identical to the last shown batch", later.Items)
	}
}

func TestCuesUnknownSessionIsNotFound(t *testing.T) {
	m, _ := newHelpManager(t)
	if _, err := m.Cues(context.Background(), "mtg_nope", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
