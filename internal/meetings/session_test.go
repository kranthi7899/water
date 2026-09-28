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
	t0 := time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)
	clock := t0.Add(-time.Minute)
	useClock(m, &clock)
	a, _ := m.Start(ctx, "")
	b, _ := m.Start(ctx, "")
	for _, add := range []struct {
		id  string
		seg Segment
	}{
		{a.ID, Segment{At: t0.Add(2 * time.Second), Channel: System, Text: "compute is over budget"}},
		{a.ID, Segment{At: t0, Channel: Mic, Text: "  let's start  "}},
		{b.ID, Segment{At: t0, Channel: System, Text: "other meeting"}},
	} {
		clock = add.seg.At // received as soon as it was spoken
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

// useClock points m's clock at *c, so a test controls when segments arrive.
func useClock(m *Manager, c *time.Time) { m.now = func() time.Time { return *c } }

// TestSegmentWindowsUseArrivalAndAtIsClamped is the review finding: a long
// utterance stamped with when it began arrives ~47s later, already outside
// the 45s cue window by its own At. The rolling windows select on arrival;
// At still orders the transcript, clamped to [start, now+skew].
func TestSegmentWindowsUseArrivalAndAtIsClamped(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	start := time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)
	clock := start
	useClock(m, &clock)
	s, err := m.Start(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	clock = start.Add(5 * time.Minute)
	if err := m.AddSegment(ctx, s.ID, Segment{At: clock.Add(-47 * time.Second), Channel: System, Text: "a long monologue"}); err != nil {
		t.Fatal(err)
	}
	recent, err := m.SegmentsSince(ctx, s.ID, clock.Add(-CueWindow))
	if err != nil || len(recent) != 1 {
		t.Fatalf("cue window = %+v, %v; want the just-arrived monologue", recent, err)
	}
	if !recent[0].At.Equal(clock.Add(-47 * time.Second)) {
		t.Fatalf("At = %v, want the client's speech-start time kept", recent[0].At)
	}

	// A future stamp is clamped to now; one before the session to its start.
	if err := m.AddSegment(ctx, s.ID, Segment{At: clock.Add(time.Hour), Channel: Mic, Text: "future"}); err != nil {
		t.Fatal(err)
	}
	if err := m.AddSegment(ctx, s.ID, Segment{At: start.Add(-time.Hour), Channel: Mic, Text: "past"}); err != nil {
		t.Fatal(err)
	}
	all, err := m.Segments(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]time.Time{}
	for _, seg := range all {
		got[seg.Text] = seg.At
	}
	if !got["future"].Equal(clock) || !got["past"].Equal(start) {
		t.Fatalf("clamped At: future=%v (want %v) past=%v (want %v)", got["future"], clock, got["past"], start)
	}
	// Ten minutes on, nothing is recent any more, whatever its stamp said.
	clock = clock.Add(10 * time.Minute)
	if later, _ := m.SegmentsSince(ctx, s.ID, clock.Add(-CueWindow)); len(later) != 0 {
		t.Fatalf("stale segments still in the window: %+v", later)
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
