package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"water/internal/decisions"
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
	// At is when this state was last set, used only by sweepRecaps to
	// decide when a terminal skipped/failed entry (the only states that
	// ever linger — see startRecapOnStop's ready case, which deletes
	// immediately instead of recording one) is old enough to evict.
	At time.Time
}

// recapRetention bounds how long a skipped/failed recap's in-memory state
// survives (found in an adversarial review, 2026-09-26: d.recaps grew
// without bound, since nothing ever removed an entry). A ready recap needs
// no retention at all -- startRecapOnStop deletes it the moment the
// durable store.Meeting write it depends on succeeds, since
// meetingViewOf's own !tracked fallback already reads that row correctly.
// skipped/failed recaps have no durable form, so they still need to
// survive long enough for a client to see the final status at least
// once; 24h is generous for that while keeping the map's lifetime bounded
// to roughly one day's worth of non-ready outcomes, not the app's whole
// uptime.
const recapRetention = 24 * time.Hour

// RecapSweepInterval is how often RunRecapSweep calls sweepRecaps when the
// daemon doesn't override it.
const RecapSweepInterval = time.Hour

// sweepRecaps evicts a skipped/failed recap entry once it is older than
// recapRetention. Never touches a running entry (bounded by recapTimeout
// anyway, and evicting one mid-flight would only cost a future set() call
// its cheap map re-insert, never lose data) or a ready one (already
// deleted immediately, see startRecapOnStop).
func (d *Daemon) sweepRecaps(now time.Time) {
	d.recapMu.Lock()
	defer d.recapMu.Unlock()
	for id, st := range d.recaps {
		if (st.Status == recapSkipped || st.Status == recapFailed) && now.Sub(st.At) > recapRetention {
			delete(d.recaps, id)
		}
	}
}

// RunRecapSweep calls sweepRecaps every interval until ctx is done,
// mirroring internal/nervous/turn.Table.RunSweep's exact pattern (the
// daemon runs both the same way, tied to its own shutdown context).
// interval <= 0 means RecapSweepInterval.
func (d *Daemon) RunRecapSweep(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = RecapSweepInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.sweepRecaps(time.Now())
		}
	}
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
// call.
//
// docs/slices/UI.md Phase 3d wires meetingProjectClassifier's
// meetings.ProjectClassifier in as Recap's classifier: a second, similarly
// gated model call (its own Charge hook against gate.ModelCall at the same
// origin P1, on the same cold backend) for the recap's project guess. That
// guess is always labelled ("likely: <name> (<bucket>)", never stated as
// fact) and never files a for_project link or any other anchor by
// itself — see meetingProjectClassifier's doc comment and
// TestRecapProjectGuessNeverWritesAForProjectLink (internal/meetings).
//
// It returns the recap's state right after starting: running or skipped.
func (d *Daemon) startRecapOnStop(ctx context.Context, sessionID string) string {
	d.recapMu.Lock()
	if st, ok := d.recaps[sessionID]; ok {
		// Two stops racing on one session: only one recap.
		d.recapMu.Unlock()
		return st.Status
	}
	d.recaps[sessionID] = recapState{Status: recapRunning, At: time.Now()}
	d.recapMu.Unlock()

	set := func(st recapState) {
		st.At = time.Now()
		d.recapMu.Lock()
		d.recaps[sessionID] = st
		d.recapMu.Unlock()
	}
	// clearReady deletes the entry outright instead of recording "ready":
	// by the time this is called, meetings.Recap has already durably
	// written the store.Meeting row, so meetingViewOf's own !tracked
	// fallback reads it correctly with no in-memory state needed at all —
	// the common, successful case leaves nothing in d.recaps to sweep or
	// grow (see recapRetention's own doc comment).
	clearReady := func() {
		d.recapMu.Lock()
		delete(d.recaps, sessionID)
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
		classifier := d.meetingProjectClassifier(rctx, model)
		if _, err := d.meetings.Recap(rctx, sessionID, classifier, d.cfg.Backend, model, recapTimeout); err != nil {
			set(recapState{Status: recapFailed, Err: err.Error()})
			return
		}
		clearReady()
	}()
	return recapRunning
}

// meetingProjectClassifier builds the recap's project-guess classifier from
// the roster's current "projects" table (internal/store.Project, source
// "seed"), or nil when there is no gate or no project to guess among yet —
// Recap treats a nil classifier as "none wired", reporting the guess as
// unavailable rather than erroring (docs/slices/UI.md Phase 3d).
//
// It is rebuilt on every recap rather than cached on Config, unlike
// buildDecisionsTrigger's ModelClassifier (built once at daemon startup
// from on-disk decision-type YAML): the projects table can change at
// runtime (internal/roster re-syncs it), and a project added or renamed
// since the daemon started should be guessable without a restart. The
// classifier itself is meetings.ProjectClassifier, not decisions.
// ModelClassifier — see that type's doc comment for why a second, small
// type is the honest choice here rather than forcing "which project" through
// a decisions.Registry of decision types.
func (d *Daemon) meetingProjectClassifier(ctx context.Context, model string) decisions.Classifier {
	if d.cfg.Gate == nil || d.cfg.Backend == nil {
		return nil
	}
	projects, err := store.List[store.Project](ctx, d.cfg.Store, store.Query{Source: "seed"})
	if err != nil || len(projects) == 0 {
		return nil
	}
	opts := make([]meetings.ProjectOption, 0, len(projects))
	for _, p := range projects {
		opts = append(opts, meetings.ProjectOption{ID: p.SourceID, Name: p.Name})
	}
	return &meetings.ProjectClassifier{
		Projects: opts,
		Backend:  d.cfg.Backend,
		Model:    model,
		Charge:   func() error { return d.cfg.Gate.ModelCall(gate.P1) },
	}
}

// meetingView is one meeting session as GET /v1/meetings(/{id}) returns
// it. recap_text (when recap is "ready") is the stored after-meeting recap;
// it is phrased from meeting speech, which is untrusted on both channels,
// so untrusted is always true: render it as text only, never as markup.
// RecapSignals/ProjectGuess (migration 0022, docs/slices/UI.md Phase 3d) are
// the recap's structured signal block and its labelled project guess;
// RecapSignals is omitted when the session predates the migration or its
// recap hasn't produced one yet.
type meetingView struct {
	SessionID    string            `json:"session_id"`
	StartedAt    time.Time         `json:"started_at"`
	EndedAt      *time.Time        `json:"ended_at"`
	Live         bool              `json:"live"`
	EventID      string            `json:"event_id"`
	EventTitle   string            `json:"event_title"`
	Recap        string            `json:"recap"` // none|running|ready|skipped|failed
	RecapText    string            `json:"recap_text,omitempty"`
	RecapError   string            `json:"recap_error,omitempty"`
	Untrusted    bool              `json:"untrusted"`
	RecapSignals *recapSignalsView `json:"recap_signals,omitempty"`
	ProjectGuess projectGuessView  `json:"project_guess"`
}

func (d *Daemon) meetingViewOf(ctx context.Context, r store.MeetingSessionRow) meetingView {
	v := meetingView{
		SessionID: r.ID, StartedAt: r.StartedAt, EndedAt: r.EndedAt, Live: r.EndedAt == nil,
		EventID: r.EventID, Recap: recapNone, Untrusted: true,
		ProjectGuess: d.projectGuessViewOf(ctx, r.ProjectGuessID, r.ProjectGuessConfidence),
	}
	if sig, ok := d.recapSignalsViewOf(ctx, r.RecapSignals); ok {
		v.RecapSignals = &sig
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

// recapItemView is one meetings.RecapItem as the recap view renders it.
type recapItemView struct {
	Text    string    `json:"text"`
	At      time.Time `json:"at"`
	Channel string    `json:"channel"`
}

func recapItemViewOf(it meetings.RecapItem) recapItemView {
	return recapItemView{Text: it.Text, At: it.At, Channel: string(it.Channel)}
}

// actionItemView is one meetings.ActionItem, plus OwnerInitials when Owner
// exactly matches one roster person's name.
type actionItemView struct {
	recapItemView
	Owner         string `json:"owner,omitempty"`
	OwnerInitials string `json:"owner_initials,omitempty"`
}

// recapSignalsView is the recap's four sections (docs/slices/UI.md Phase
// 3d: Decisions made, Action items, Open questions, FYI), decoded from
// meeting_sessions.recap_signals plus each action item's resolved owner
// avatar.
type recapSignalsView struct {
	Decisions     []recapItemView  `json:"decisions"`
	ActionItems   []actionItemView `json:"action_items"`
	OpenQuestions []recapItemView  `json:"open_questions"`
	FYI           []recapItemView  `json:"fyi"`
}

// recapSignalsViewOf decodes raw (meeting_sessions.recap_signals) into the
// four rendered sections, resolving each action item's owner avatar by an
// exact, case-sensitive match on a roster person's Name only — never a
// fuzzy or partial match (docs/slices/UI.md Phase 3d): a name the
// transcript mis-heard, or one nobody in the roster has, shows with no
// avatar rather than guessing who it might be. ok is false when raw is
// empty or not valid JSON (a session recapped before migration 0022, or one
// whose recap hasn't produced signals yet).
func (d *Daemon) recapSignalsViewOf(ctx context.Context, raw string) (recapSignalsView, bool) {
	if raw == "" {
		return recapSignalsView{}, false
	}
	var sig meetings.RecapSignals
	if err := json.Unmarshal([]byte(raw), &sig); err != nil {
		return recapSignalsView{}, false
	}
	v := recapSignalsView{
		Decisions:     make([]recapItemView, 0, len(sig.Decisions)),
		ActionItems:   make([]actionItemView, 0, len(sig.ActionItems)),
		OpenQuestions: make([]recapItemView, 0, len(sig.OpenQuestions)),
		FYI:           make([]recapItemView, 0, len(sig.FYI)),
	}
	for _, it := range sig.Decisions {
		v.Decisions = append(v.Decisions, recapItemViewOf(it))
	}
	for _, it := range sig.OpenQuestions {
		v.OpenQuestions = append(v.OpenQuestions, recapItemViewOf(it))
	}
	for _, it := range sig.FYI {
		v.FYI = append(v.FYI, recapItemViewOf(it))
	}
	for _, a := range sig.ActionItems {
		av := actionItemView{recapItemView: recapItemViewOf(a.RecapItem), Owner: a.Owner}
		if a.Owner != "" {
			av.OwnerInitials = d.exactRosterInitials(ctx, a.Owner)
		}
		v.ActionItems = append(v.ActionItems, av)
	}
	return v, true
}

// exactRosterInitials resolves name to a roster person's initials only
// when it exactly matches (after trimming surrounding whitespace) one
// roster person's Name — never a fuzzy or partial match. "" on any miss.
func (d *Daemon) exactRosterInitials(ctx context.Context, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	people, err := store.List[store.Person](ctx, d.cfg.Store, store.Query{Source: "seed"})
	if err != nil {
		return ""
	}
	for _, p := range people {
		if p.Name == name {
			return Initials(p.Name)
		}
	}
	return ""
}

// projectGuessView is the recap's project guess as the API renders it
// (docs/slices/UI.md Phase 3d): always a labelled guess (Label is never
// empty), never a value a client could mistake for a filed link. ProjectID
// is the raw store.Project id (migration 0022's project_guess_id);
// ProjectName is that project's resolved display name, or the bare id when
// it can't be resolved (a project renamed or removed since the recap ran).
type projectGuessView struct {
	Available   bool    `json:"available"`
	ProjectID   string  `json:"project_id,omitempty"`
	ProjectName string  `json:"project_name,omitempty"`
	Confidence  float64 `json:"confidence,omitempty"`
	Label       string  `json:"label"`
}

func (d *Daemon) projectGuessViewOf(ctx context.Context, id string, confidence *float64) projectGuessView {
	if id == "" || confidence == nil {
		return projectGuessView{Label: meetings.ProjectGuess{}.Label("")}
	}
	guess := meetings.ProjectGuess{Available: true, TypeID: id, Confidence: *confidence}
	name := id
	if p, err := store.Get[store.Project](ctx, d.cfg.Store, "seed", id); err == nil && p.Name != "" {
		name = p.Name
	}
	return projectGuessView{Available: true, ProjectID: id, ProjectName: name, Confidence: *confidence, Label: guess.Label(name)}
}

const (
	defaultMeetingListLimit = 20
	maxMeetingListLimit     = 200
	// upcomingMeetingsWindow bounds how far ahead ?upcoming=1 looks
	// (docs/slices/UI.md Phase 3d): a calendar-view horizon, not a target —
	// store.EventsInRange needs a bounded [from, to) range, and 30 days is
	// comfortably past any meeting a CEO plans around today.
	upcomingMeetingsWindow = 30 * 24 * time.Hour
)

// upcomingMeetingView is one calendar event GET /v1/meetings?upcoming=1
// returns: a future event from the events table, not a meeting_sessions
// row (docs/slices/UI.md Phase 3d) — nothing has necessarily been started
// for it yet, so there is no session_id, live state or recap to report.
type upcomingMeetingView struct {
	EventID  string    `json:"event_id"`
	Title    string    `json:"title"`
	StartAt  time.Time `json:"start_at"`
	EndAt    time.Time `json:"end_at"`
	Location string    `json:"location"`
}

// handleListMeetings serves GET /v1/meetings?limit=N (default 20, max 200):
// recent meeting sessions, most recently started first, each with its
// recap state. ?upcoming=1 switches it to a different, simpler query: up to
// limit future events from the events table (store.EventsInRange, [now,
// now+upcomingMeetingsWindow)) — not past events, and not meeting_sessions
// rows — ordered earliest first, for a "what's coming up" list rather than
// "what did we just record".
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
	if r.URL.Query().Get("upcoming") == "1" {
		now := time.Now()
		events, err := store.EventsInRange(r.Context(), d.cfg.Store, now, now.Add(upcomingMeetingsWindow))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(events) > limit {
			events = events[:limit]
		}
		out := make([]upcomingMeetingView, 0, len(events))
		for _, e := range events {
			out = append(out, upcomingMeetingView{EventID: e.SourceID, Title: e.Title, StartAt: e.StartAt, EndAt: e.EndAt, Location: e.Location})
		}
		writeJSON(w, http.StatusOK, out)
		return
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
