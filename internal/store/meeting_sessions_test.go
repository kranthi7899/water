package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestInsertMeetingSegmentRefusesEndedOrUnknownSession: the stopped check
// lives in the insert itself, so a segment racing a stop (checked open, then
// stopped, then inserted) can never land in a closed transcript.
func TestInsertMeetingSegmentRefusesEndedOrUnknownSession(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	t0 := time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)
	if err := s.InsertMeetingSession(ctx, MeetingSessionRow{ID: "mtg_a", StartedAt: t0}); err != nil {
		t.Fatal(err)
	}
	seg := MeetingSegmentRow{SessionID: "mtg_a", At: t0, Channel: "mic", Text: "open"}
	if err := s.InsertMeetingSegment(ctx, seg); err != nil {
		t.Fatalf("insert into open session: %v", err)
	}
	if _, err := s.EndMeetingSession(ctx, "mtg_a", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	seg.Text = "late"
	if err := s.InsertMeetingSegment(ctx, seg); !errors.Is(err, ErrMeetingEnded) {
		t.Fatalf("insert into ended session: err = %v, want ErrMeetingEnded", err)
	}
	seg.SessionID = "mtg_nope"
	if err := s.InsertMeetingSegment(ctx, seg); !errors.Is(err, ErrNotFound) {
		t.Fatalf("insert into unknown session: err = %v, want ErrNotFound", err)
	}
	rows, err := s.ListMeetingSegments(ctx, "mtg_a", time.Time{})
	if err != nil || len(rows) != 1 || rows[0].Text != "open" {
		t.Fatalf("segments = %+v, %v; want only the one posted while open", rows, err)
	}
}
