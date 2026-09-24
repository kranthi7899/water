package gcal

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"water/internal/store"
)

// withLocal runs the test with time.Local set to name, restoring it after.
// These tests do not run in parallel, so swapping the process zone is safe.
func withLocal(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("zone %s unavailable: %v", name, err)
	}
	prev := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = prev })
	return loc
}

// TestAllDayEventLandsOnItsLocalDay is the review finding: an all-day event
// ("date", not "dateTime") was stored at UTC midnight, which is the previous
// evening in New York, so today's local-day window missed it and it showed
// as "20:00 Offsite" the day before.
func TestAllDayEventLandsOnItsLocalDay(t *testing.T) {
	for _, zone := range []string{"America/New_York", "Asia/Kolkata"} {
		t.Run(zone, func(t *testing.T) {
			loc := withLocal(t, zone)
			st, err := store.Open(filepath.Join(t.TempDir(), "water.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			ctx := context.Background()
			ev := Event{ID: "offsite", CalendarID: "primary", Title: "Offsite", Start: "2026-09-24", End: "2026-09-25"}
			if err := st.Upsert(ctx, toEventRecord("gcal", ev, true)); err != nil {
				t.Fatal(err)
			}
			day := time.Date(2026, 9, 24, 0, 0, 0, 0, loc)
			got, err := store.EventsInRange(ctx, st, day, day.AddDate(0, 0, 1))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Title != "Offsite" {
				t.Fatalf("local day 2026-09-24 events = %+v, want the all-day Offsite", got)
			}
			if !got[0].AllDay() || got[0].Clock() != "all day" {
				t.Fatalf("AllDay=%v Clock=%q, want an all-day event", got[0].AllDay(), got[0].Clock())
			}
			prev, err := store.EventsInRange(ctx, st, day.AddDate(0, 0, -1), day)
			if err != nil {
				t.Fatal(err)
			}
			if len(prev) != 0 {
				t.Fatalf("previous local day events = %+v, want none", prev)
			}
		})
	}
}

func TestTimedEventIsNotAllDay(t *testing.T) {
	withLocal(t, "America/New_York")
	rec := toEventRecord("gcal", Event{ID: "e", CalendarID: "primary", Start: "2026-09-24T09:30:00-04:00", End: "2026-09-24T10:00:00-04:00"}, true).(*store.Event)
	if rec.AllDay() || rec.Clock() != "09:30" {
		t.Fatalf("AllDay=%v Clock=%q, want a timed 09:30 event", rec.AllDay(), rec.Clock())
	}
}
