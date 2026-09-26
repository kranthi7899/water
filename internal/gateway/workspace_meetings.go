package gateway

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"water/internal/gate"
	"water/internal/meetings"
	"water/internal/store"
	"water/internal/twins"
)

// meetingRecapSource is the store.Meeting source meetings.Manager.Recap
// files a session's recap under (Meta.Source "meetings", SourceID the
// session id).
const meetingRecapSource = "meetings"

// recapTimeout bounds one background recap: the phrasing model call plus
// its store write.
const recapTimeout = 2 * time.Minute

// Recap states, as GET /v1/meetings reports them.
const (
	recapNone    = "none"    // never attempted (still live, or stopped before recap-on-stop existed)
	recapRunning = "running" // the background recap is in flight
	recapReady   = "ready"   // a recap is stored; see recap_text
	recapSkipped = "skipped" // nothing to recap (no segments) or no backend; see recap_error
	recapFailed  = "failed"  // the gate refused the model call or the call failed; see recap_error
)

type recapState struct {
	Status string
	Err    string
}

// startRecapOnStop starts the after-meeting recap for a session this
// request just stopped (docs/slices/V.md §0.6: meetings.Manager.Recap
// existed but was never called in production). It is gated exactly like
// other model work: one gate.ModelCall charge against the manifest's usage
// cap, at origin P1 (work that follows from something the CEO did — the
// same origin the decision classifier and agent-mail triage charge at),
// before the one phrasing model call Recap makes, on the cold backend (no
// tools, no warm session, never the model slot a CEO turn waits on). The
// recap runs in the background, detached from the request, so stopping a
// meeting answers at once; its result is the store.Meeting summary Recap
// writes, which GET /v1/meetings(/{id}) then reports. A session with no
// segments is skipped: there is nothing to recap, and it costs no model
// call. No classifier is passed, so the recap's project guess reports
// "unavailable" (the decisions Triager's classifier isn't reachable from
// the daemon's Config; wiring one would be a separate change).
//
// It returns the recap's state right after starting: running or skipped.
func (d *Daemon) startRecapOnStop(ctx context.Context, sessionID string) string {
	d.recapMu.Lock()
	if st, ok := d.recaps[sessionID]; ok {
		// Two stops racing on one session: only one recap.
		d.recapMu.Unlock()
		return st.Status
	}
	d.recaps[sessionID] = recapState{Status: recapRunning}
	d.recapMu.Unlock()

	set := func(st recapState) {
		d.recapMu.Lock()
		d.recaps[sessionID] = st
		d.recapMu.Unlock()
	}
	if d.cfg.Backend == nil {
		set(recapState{Status: recapSkipped, Err: "no model backend configured"})
		return recapSkipped
	}
	segs, err := d.meetings.Segments(ctx, sessionID)
	if err != nil {
		set(recapState{Status: recapFailed, Err: err.Error()})
		return recapFailed
	}
	if len(segs) == 0 {
		set(recapState{Status: recapSkipped, Err: "the session has no transcript segments"})
		return recapSkipped
	}

	d.bg.Add(1)
	go func() {
		defer d.bg.Done()
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recapTimeout)
		defer cancel()
		if d.cfg.Gate != nil {
			if err := d.cfg.Gate.ModelCall(gate.P1); err != nil {
				set(recapState{Status: recapFailed, Err: err.Error()})
				return
			}
		}
		model := d.cfg.Manifest.ModelFor(twins.TierFast)
		if _, err := d.meetings.Recap(rctx, sessionID, nil, d.cfg.Backend, model, recapTimeout); err != nil {
			set(recapState{Status: recapFailed, Err: err.Error()})
			return
		}
		set(recapState{Status: recapReady})
	}()
	return recapRunning
}

// meetingView is one meeting session as GET /v1/meetings(/{id}) returns
// it. recap_text (when recap is "ready") is the stored after-meeting recap;
// it is phrased from meeting speech, which is untrusted on both channels,
// so untrusted is always true: render it as text only, never as markup.
type meetingView struct {
	SessionID  string     `json:"session_id"`
	StartedAt  time.Time  `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at"`
	Live       bool       `json:"live"`
	EventID    string     `json:"event_id"`
	EventTitle string     `json:"event_title"`
	Recap      string     `json:"recap"` // none|running|ready|skipped|failed
	RecapText  string     `json:"recap_text,omitempty"`
	RecapError string     `json:"recap_error,omitempty"`
	Untrusted  bool       `json:"untrusted"`
}

func (d *Daemon) meetingViewOf(ctx context.Context, r store.MeetingSessionRow) meetingView {
	v := meetingView{
		SessionID: r.ID, StartedAt: r.StartedAt, EndedAt: r.EndedAt, Live: r.EndedAt == nil,
		EventID: r.EventID, Recap: recapNone, Untrusted: true,
	}
	if r.EventID != "" {
		// A session started from a calendar event names the event by its
		// SourceID; the calendar connector's source is not recorded on the
		// session, so match on source_id alone.
		if e, err := store.EventBySourceID(ctx, d.cfg.Store, r.EventID); err == nil {
			v.EventTitle = e.Title
		}
	}
	d.recapMu.Lock()
	st, tracked := d.recaps[r.ID]
	d.recapMu.Unlock()
	if tracked {
		v.Recap, v.RecapError = st.Status, st.Err
	}
	if !tracked || st.Status == recapReady {
		if m, err := store.Get[store.Meeting](ctx, d.cfg.Store, meetingRecapSource, r.ID); err == nil && strings.TrimSpace(m.Summary) != "" {
			v.Recap, v.RecapText = recapReady, m.Summary
		}
	}
	return v
}

const (
	defaultMeetingListLimit = 20
	maxMeetingListLimit     = 200
)

// handleListMeetings serves GET /v1/meetings?limit=N (default 20, max
// 200): recent meeting sessions, most recently started first, each with
// its recap state.
func (d *Daemon) handleListMeetings(w http.ResponseWriter, r *http.Request) {
	limit := defaultMeetingListLimit
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
			return
		}
		limit = min(n, maxMeetingListLimit)
	}
	rows, err := d.cfg.Store.ListMeetingSessions(r.Context(), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]meetingView, 0, len(rows))
	for _, row := range rows {
		out = append(out, d.meetingViewOf(r.Context(), row))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetMeeting serves GET /v1/meetings/{id}: one session with its
// recap state (what a client polls after stopping a meeting).
func (d *Daemon) handleGetMeeting(w http.ResponseWriter, r *http.Request) {
	row, err := d.cfg.Store.GetMeetingSession(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		meetingError(w, meetings.ErrNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, d.meetingViewOf(r.Context(), row))
}
