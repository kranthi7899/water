package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestListMeetingSessionsNewestFirstWithLimit(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	for i, id := range []string{"mtg_a", "mtg_b", "mtg_c"} {
		if err := st.InsertMeetingSession(ctx, MeetingSessionRow{ID: id, StartedAt: base.Add(time.Duration(i) * time.Hour), EventID: map[bool]string{true: "ev1"}[i == 1]}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.EndMeetingSession(ctx, "mtg_a", base.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}

	all, err := st.ListMeetingSessions(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].ID != "mtg_c" || all[1].ID != "mtg_b" || all[2].ID != "mtg_a" {
		t.Fatalf("order = %+v, want mtg_c, mtg_b, mtg_a", all)
	}
	if all[1].EventID != "ev1" || all[0].EventID != "" {
		t.Fatalf("event ids = %q/%q", all[1].EventID, all[0].EventID)
	}
	if all[2].EndedAt == nil || all[0].EndedAt != nil {
		t.Fatalf("ended_at: mtg_a=%v mtg_c=%v", all[2].EndedAt, all[0].EndedAt)
	}

	two, err := st.ListMeetingSessions(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(two) != 2 || two[0].ID != "mtg_c" {
		t.Fatalf("limit 2 = %+v", two)
	}
}

func TestEventBySourceID(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	now := time.Now()
	if err := st.Upsert(ctx, &Event{Meta: Meta{Source: "gcal", SourceID: "ev-9", CreatedAt: now, UpdatedAt: now}, Title: "Board sync", StartAt: now}); err != nil {
		t.Fatal(err)
	}
	e, err := EventBySourceID(ctx, st, "ev-9")
	if err != nil || e.Title != "Board sync" {
		t.Fatalf("EventBySourceID = %+v, %v", e, err)
	}
	if _, err := EventBySourceID(ctx, st, "nope"); err != ErrNotFound {
		t.Fatalf("missing: err = %v, want ErrNotFound", err)
	}
}
