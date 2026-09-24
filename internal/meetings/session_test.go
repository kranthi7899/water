package meetings

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"water/internal/store"
)

func newManager(t *testing.T) *Manager {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st)
}

func TestSessionLifecycle(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	s, err := m.Start(ctx, "evt-1")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID == "" || s.EndedAt != nil || s.EventID != "evt-1" {
		t.Fatalf("started = %+v", s)
	}
	got, err := m.Get(ctx, s.ID)
	if err != nil || got.EventID != "evt-1" || got.EndedAt != nil {
		t.Fatalf("get = %+v, %v", got, err)
	}
	stopped, err := m.Stop(ctx, s.ID)
	if err != nil || stopped.EndedAt == nil {
		t.Fatalf("stop = %+v, %v", stopped, err)
	}
	again, err := m.Stop(ctx, s.ID)
	if err != nil || again.EndedAt == nil || !again.EndedAt.Equal(*stopped.EndedAt) {
		t.Fatalf("second stop = %+v, %v; want the original end time", again, err)
	}
	if err := m.AddSegment(ctx, s.ID, Segment{Channel: Mic, Text: "late"}); !errors.Is(err, ErrEnded) {
		t.Fatalf("segment after stop: err = %v, want ErrEnded", err)
	}

	manual, err := m.Start(ctx, "")
	if err != nil || manual.EventID != "" {
		t.Fatalf("manual start = %+v, %v", manual, err)
	}
}

func TestSegmentsPersistWithChannelAndSession(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	a, _ := m.Start(ctx, "")
	b, _ := m.Start(ctx, "")
	t0 := time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)
	for _, add := range []struct {
		id  string
		seg Segment
	}{
		{a.ID, Segment{At: t0.Add(2 * time.Second), Channel: System, Text: "compute is over budget"}},
		{a.ID, Segment{At: t0, Channel: Mic, Text: "  let's start  "}},
		{b.ID, Segment{At: t0, Channel: System, Text: "other meeting"}},
	} {
		if err := m.AddSegment(ctx, add.id, add.seg); err != nil {
			t.Fatal(err)
		}
	}
	segs, err := m.Segments(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 {
		t.Fatalf("segments = %+v", segs)
	}
	if segs[0].Channel != Mic || segs[0].Text != "let's start" || !segs[0].At.Equal(t0) ||
		segs[1].Channel != System || segs[1].Text != "compute is over budget" {
		t.Fatalf("segments = %+v", segs)
	}
	for _, s := range segs {
		if !s.External || s.SessionID != a.ID {
			t.Fatalf("segment %+v: want External=true on every channel and session %s", s, a.ID)
		}
	}
	recent, err := m.SegmentsSince(ctx, a.ID, t0.Add(time.Second))
	if err != nil || len(recent) != 1 || recent[0].Channel != System {
		t.Fatalf("since = %+v, %v", recent, err)
	}
}

func TestSegmentValidationAndUnknownSession(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	s, _ := m.Start(ctx, "")
	for _, seg := range []Segment{
		{Channel: "speaker", Text: "x"},
		{Channel: "", Text: "x"},
		{Channel: Mic, Text: "   "},
		{Channel: Mic, Text: string(make([]byte, MaxSegmentText+1))},
	} {
		if err := m.AddSegment(ctx, s.ID, seg); !errors.Is(err, ErrBadSegment) {
			t.Fatalf("AddSegment(%q, %d bytes): err = %v, want ErrBadSegment", seg.Channel, len(seg.Text), err)
		}
	}
	for _, id := range []string{"mtg_nope", "", "'; DROP TABLE meeting_sessions; --"} {
		if err := m.AddSegment(ctx, id, Segment{Channel: Mic, Text: "hi"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("AddSegment(%q): err = %v, want ErrNotFound", id, err)
		}
		if _, err := m.Stop(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Stop(%q): err = %v, want ErrNotFound", id, err)
		}
		if _, err := m.Segments(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Segments(%q): err = %v, want ErrNotFound", id, err)
		}
	}
}
