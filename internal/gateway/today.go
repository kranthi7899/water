package gateway

import (
	"net/http"
	"time"

	"water/internal/needsyou"
	"water/internal/store"
)

// scheduleEntry is one calendar event on GET /v1/today's schedule list: a
// small projection of store.Event, not the full record (an Event carries
// Attendees/Organizer/Status too, none of which the workspace UI's day view
// needs).
type scheduleEntry struct {
	Title    string    `json:"title"`
	StartAt  time.Time `json:"start_at"`
	EndAt    time.Time `json:"end_at"`
	AllDay   bool      `json:"all_day"`
	Location string    `json:"location"`
}

// todayResponse is GET /v1/today's body.
type todayResponse struct {
	GeneratedAt time.Time       `json:"generated_at"`
	NeedsYou    []needsyou.Item `json:"needs_you"`
	Schedule    []scheduleEntry `json:"schedule"`
}

// todayBounds returns [start, end) for now's local day: local midnight
// through the following local midnight. This matches
// internal/runtime/runtime.go's own unexported startOfDay (used by
// computeBriefSignals in internal/runtime/brief.go for the exact same
// "today's events" query) exactly, kept as a small local copy rather than
// an import: internal/runtime is not in this task's file list, and the
// logic is three lines with nothing to share beyond it.
func todayBounds(now time.Time) (start, end time.Time) {
	y, m, d := now.Date()
	start = time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	return start, start.Add(24 * time.Hour)
}

// handleToday serves GET /v1/today: the "needs you" snapshot (decisions and
// approvals that have crossed docs/slices/V.md §5's threshold, recomputed on
// its own background tick — see internal/cli/cmd_daemon.go's runDaemon) plus
// today's local-day calendar schedule, read fresh from the store on every
// request (EventsInRange is a cheap indexed local read, unlike the "needs
// you" recompute).
func (d *Daemon) handleToday(w http.ResponseWriter, r *http.Request) {
	now := time.Now()

	needsYou := []needsyou.Item{}
	if d.cfg.NeedsYou != nil {
		if s := d.cfg.NeedsYou.Snapshot(); s != nil {
			needsYou = s
		}
	}

	start, end := todayBounds(now)
	events, err := store.EventsInRange(r.Context(), d.cfg.Store, start, end)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	schedule := make([]scheduleEntry, 0, len(events))
	for _, e := range events {
		schedule = append(schedule, scheduleEntry{
			Title: e.Title, StartAt: e.StartAt, EndAt: e.EndAt,
			AllDay: e.AllDay(), Location: e.Location,
		})
	}

	writeJSON(w, http.StatusOK, todayResponse{
		GeneratedAt: now,
		NeedsYou:    needsYou,
		Schedule:    schedule,
	})
}
