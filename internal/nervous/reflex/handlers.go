package reflex

import (
	"context"
	"errors"
	"fmt"
	"time"

	"water/internal/approvals"
	"water/internal/nervous/intents"
	"water/internal/nervous/render"
	"water/internal/nervous/slots"
	"water/internal/store"
)

// ErrBriefCacheMiss is returned by store.cached_brief when nothing is
// cached for the requested day. A quick tier must never compute a brief
// itself (that needs a model call); the caller escalates to the main path
// on this error, which then runs runtime.ComputeAndCacheBrief.
var ErrBriefCacheMiss = errors.New("reflex: no cached brief for today")

const workdayStartHour, workdayEndHour = 9, 18 // known gap: a fixed 09:00-18:00 window, not per-CEO configurable yet

func dayBounds(t time.Time) (start, end time.Time) {
	start = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return start, start.AddDate(0, 0, 1)
}

func scheduleHandler(ctx context.Context, d Deps, a Args) (render.Result, error) {
	when := a["when"]
	start, end := when.Start, when.End
	if start.IsZero() && end.IsZero() {
		start, end = dayBounds(d.now())
	}
	events, err := d.Store.EventsInRange(ctx, start, end)
	if err != nil {
		return render.Result{}, fmt.Errorf("reflex: schedule.on_date: %w", err)
	}
	items := make([]render.Item, 0, len(events))
	tainted := false
	for _, e := range events {
		items = append(items, render.Item{"time": e.StartAt.Local().Format("15:04"), "title": e.Title})
		if e.External {
			tainted = true
		}
	}
	return render.Result{
		Intent:         "schedule.on_date",
		Kind:           "read",
		Interpretation: when.Spoken,
		Facts:          map[string]string{"when_spoken": when.Spoken},
		Items:          items,
		Tainted:        tainted,
	}, nil
}

func nextEventHandler(ctx context.Context, d Deps, a Args) (render.Result, error) {
	ev, err := d.Store.NextEvent(ctx, d.now())
	// store.ErrNotFound (no future event exists) is the expected empty
	// case, not a failure — the pre-R-16 code only checked for a nil ev,
	// which NextEvent's actual implementation (internal/store/queries_router.go)
	// never returns on its own: a missing row surfaces as ErrNotFound.
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return render.Result{}, fmt.Errorf("reflex: schedule.next_event: %w", err)
	}
	r := render.Result{Intent: "schedule.next_event", Kind: "read", Facts: map[string]string{}}
	if ev == nil {
		r.Interpretation = "no upcoming events"
		return r, nil
	}
	r.Interpretation = ev.StartAt.Local().Format("Mon 2 Jan 15:04")
	r.Items = []render.Item{{"time": ev.StartAt.Local().Format("15:04"), "title": ev.Title}}
	r.Tainted = ev.External
	return r, nil
}

func freeSlotsHandler(ctx context.Context, d Deps, a Args) (render.Result, error) {
	when := a["when"]
	day := when.Start
	if day.IsZero() {
		day, _ = dayBounds(d.now())
	}
	loc := day.Location()
	winStart := time.Date(day.Year(), day.Month(), day.Day(), workdayStartHour, 0, 0, 0, loc)
	winEnd := time.Date(day.Year(), day.Month(), day.Day(), workdayEndHour, 0, 0, 0, loc)
	if part, ok := a["part"]; ok && !part.Start.IsZero() {
		if part.Start.After(winStart) {
			winStart = part.Start
		}
		if part.End.Before(winEnd) {
			winEnd = part.End
		}
	}

	events, err := d.Store.EventsInRange(ctx, winStart, winEnd)
	if err != nil {
		return render.Result{}, fmt.Errorf("reflex: schedule.free_time: %w", err)
	}

	type busy struct{ start, end time.Time }
	var busyRanges []busy
	tainted := false
	for _, e := range events {
		s, en := e.StartAt, e.EndAt
		if s.Before(winStart) {
			s = winStart
		}
		if en.After(winEnd) {
			en = winEnd
		}
		if s.Before(en) {
			busyRanges = append(busyRanges, busy{s, en})
		}
		if e.External {
			tainted = true
		}
	}

	cursor := winStart
	var items []render.Item
	for _, b := range busyRanges {
		if b.start.After(cursor) {
			items = append(items, render.Item{"from": cursor.Local().Format("15:04"), "to": b.start.Local().Format("15:04")})
		}
		if b.end.After(cursor) {
			cursor = b.end
		}
	}
	if cursor.Before(winEnd) {
		items = append(items, render.Item{"from": cursor.Local().Format("15:04"), "to": winEnd.Local().Format("15:04")})
	}

	interp := when.Spoken
	if part, ok := a["part"]; ok {
		interp = part.Spoken
	}
	return render.Result{
		Intent:         "schedule.free_time",
		Kind:           "read",
		Interpretation: interp,
		Facts:          map[string]string{"when_spoken": when.Spoken},
		Items:          items,
		Tainted:        tainted,
	}, nil
}

func latestMailHandler(ctx context.Context, d Deps, a Args) (render.Result, error) {
	n := 5
	if v, ok := a["n"]; ok && v.N > 0 {
		n = v.N
	}
	msgs, err := d.Store.LatestMessages(ctx, n)
	if err != nil {
		return render.Result{}, fmt.Errorf("reflex: mail.latest: %w", err)
	}
	items := make([]render.Item, 0, len(msgs))
	tainted := false
	for _, m := range msgs {
		items = append(items, render.Item{"from": m.From, "subject": m.Subject, "at": m.SentAt.Local().Format("Mon 15:04")})
		if m.External {
			tainted = true
		}
	}
	return render.Result{
		Intent:         "mail.latest",
		Kind:           "read",
		Interpretation: fmt.Sprintf("last %d", n),
		Facts:          map[string]string{"n": fmt.Sprint(n)},
		Items:          items,
		Tainted:        tainted,
	}, nil
}

func unreadCountHandler(ctx context.Context, d Deps, a Args) (render.Result, error) {
	start, _ := dayBounds(d.now())
	n, err := d.Store.CountMessagesSince(ctx, start)
	if err != nil {
		return render.Result{}, fmt.Errorf("reflex: mail.unread_count: %w", err)
	}
	return render.Result{
		Intent:         "mail.unread_count",
		Kind:           "read",
		Interpretation: "unread tracking not yet synced",
		Facts:          map[string]string{"received_today": fmt.Sprint(n)},
		Text:           "I don't track read/unread state yet. Received today: " + fmt.Sprint(n) + ".",
	}, nil
}

func latestFromHandler(ctx context.Context, d Deps, a Args) (render.Result, error) {
	who := a["who"]
	msgs, err := d.Store.MessagesFrom(ctx, who.Person.Email, 1)
	if err != nil {
		return render.Result{}, fmt.Errorf("reflex: mail.latest_from: %w", err)
	}
	r := render.Result{
		Intent:         "mail.latest_from",
		Kind:           "read",
		Interpretation: who.Spoken,
		Facts:          map[string]string{"who_spoken": who.Spoken},
	}
	if len(msgs) == 0 {
		return r, nil
	}
	m := msgs[0]
	r.Items = []render.Item{{"subject": m.Subject, "at": m.SentAt.Local().Format("Mon 15:04")}}
	r.Tainted = m.External
	return r, nil
}

func cachedBriefHandler(ctx context.Context, d Deps, a Args) (render.Result, error) {
	if d.Brief == nil {
		// A caller that never wires Deps.Brief (the eval harness has no
		// real brief cache to query) reads as an honest cache miss, not a
		// nil-call panic — the same fail-safe posture d.Tasks/d.Manifest/
		// d.Health already get in this file.
		return render.Result{}, ErrBriefCacheMiss
	}
	day := d.now().Format("2006-01-02")
	text, tainted, ok, err := d.Brief(ctx, day)
	if err != nil {
		return render.Result{}, fmt.Errorf("reflex: brief.today: %w", err)
	}
	if !ok {
		return render.Result{}, ErrBriefCacheMiss
	}
	return render.Result{
		Intent:  "brief.today",
		Kind:    "read",
		Text:    text,
		Tainted: tainted,
	}, nil
}

// pendingApprovals is a nil-safe wrapper over Deps.Approvals.Pending: unlike
// a turn's Deps (always built from a non-nil runtime.Env.Approvals),
// Quick()'s Deps comes from Config.Approvals, which a bare test Config may
// leave unset. Nil reads as "nothing pending" — the conservative direction,
// since it can only ever make approvals.respond escalate rather than bind.
func pendingApprovals(ctx context.Context, d Deps) ([]approvals.Envelope, error) {
	if d.Approvals == nil {
		return nil, nil
	}
	return d.Approvals.Pending(ctx)
}

func approvalsListHandler(ctx context.Context, d Deps, a Args) (render.Result, error) {
	pending, err := pendingApprovals(ctx, d)
	if err != nil {
		return render.Result{}, fmt.Errorf("reflex: approvals.list: %w", err)
	}
	items := make([]render.Item, 0, len(pending))
	for _, e := range pending {
		items = append(items, render.Item{"action": e.Action, "risk": e.Risk})
	}
	return render.Result{
		Intent: "approvals.list",
		Kind:   "read",
		Facts:  map[string]string{"count": fmt.Sprint(len(pending))},
		Items:  items,
		Text:   approvals.Menu(pending),
	}, nil
}

// bindPendingHandler never calls Queue.Decide: it only reads Pending() and
// either surfaces the single pending envelope's id for the facade to bind
// a later yes/no reply to, or returns a clarification when 0 or 2+ are
// pending. Deciding always happens on the existing hash-bound
// approvals.Decide path, outside this package.
func bindPendingHandler(ctx context.Context, d Deps, a Args) (render.Result, error) {
	pending, err := d.Approvals.Pending(ctx)
	if err != nil {
		return render.Result{}, fmt.Errorf("reflex: approvals.respond: %w", err)
	}
	switch len(pending) {
	case 0:
		return render.Result{
			Intent: "approvals.respond",
			Kind:   "clarify",
			Text:   "Nothing is waiting for approval.",
		}, nil
	case 1:
		e := pending[0]
		return render.Result{
			Intent:     "approvals.respond",
			Kind:       "decision",
			ApprovalID: e.ID,
			Facts:      map[string]string{"action": e.Action, "risk": e.Risk},
		}, nil
	default:
		return render.Result{
			Intent: "approvals.respond",
			Kind:   "clarify",
			NeedsClarification: &render.Clarification{
				Question: "Which one?",
				Options:  approvalOptions(pending),
			},
			Text: approvals.Menu(pending),
		}, nil
	}
}

func approvalOptions(pending []approvals.Envelope) []string {
	opts := make([]string, 0, len(pending))
	for _, e := range pending {
		opts = append(opts, e.ID)
	}
	return opts
}

func cancelTasksHandler(ctx context.Context, d Deps, a Args) (render.Result, error) {
	n := 0
	if d.Tasks != nil {
		n = d.Tasks.CancelAllExcept(d.TaskID)
	}
	return render.Result{
		Intent: "control.stop",
		Kind:   "read",
		Facts:  map[string]string{"n": fmt.Sprint(n)},
		Text:   fmt.Sprintf("Cancelled %d task(s).", n),
	}, nil
}

// connectorCursorKeys lists the store.sync_cursors keys internal/sync
// actually writes for each connector this twin currently syncs, so
// status.overview can report a real "last synced" time rather than
// guessing at a naming convention.
var connectorCursorKeys = map[string]string{
	"gmail": "gmail:history_id",
	"gcal":  "gcal:primary:sync_token",
}

func statusOverviewHandler(ctx context.Context, d Deps, a Args) (render.Result, error) {
	items := make([]render.Item, 0)

	running := 0
	if d.Tasks != nil {
		running = d.Tasks.Running()
	}

	fnCount := 0
	if d.Manifest != nil {
		fnCount = len(d.Manifest.FunctionIDs())
	}

	if d.Manifest != nil {
		for _, c := range d.Manifest.Connectors {
			key, known := connectorCursorKeys[c.Name]
			item := render.Item{"connector": c.Name}
			if known {
				if t, ok, err := d.Store.CursorUpdatedAt(ctx, key); err == nil && ok {
					item["last_sync"] = t.Local().Format("Mon 15:04")
				} else {
					item["last_sync"] = "never"
				}
			} else {
				item["last_sync"] = "unknown"
			}
			items = append(items, item)
		}
	}

	facts := map[string]string{
		"running_tasks":      fmt.Sprint(running),
		"manifest_functions": fmt.Sprint(fnCount),
	}
	if d.Health != nil {
		for _, h := range d.Health() {
			facts["tier_"+h.Tier] = h.State
		}
	}

	return render.Result{
		Intent: "status.overview",
		Kind:   "read",
		Facts:  facts,
		Items:  items,
	}, nil
}

func helpIntentsHandler(ctx context.Context, d Deps, a Args) (render.Result, error) {
	items := make([]render.Item, 0, len(d.Registry))
	for _, s := range d.Registry {
		items = append(items, render.Item{"id": s.ID, "description": s.Description})
	}
	return render.Result{
		Intent: "help.intents",
		Kind:   "read",
		Items:  items,
	}, nil
}

// Table is the one handler table: the complete, fixed set of functions a
// quick tier may ever propose. Adding a function here means adding it to
// an intent file and, if it should be reachable from the main agent, to
// QuickTool below.
func Table() map[string]Handler {
	return map[string]Handler{
		"store.calendar_events": {
			Spec: intents.FunctionSpec{
				ID: "store.calendar_events", Args: map[string]slots.Type{"when": slots.TypeDateRange},
				Class: intents.ClassLookup, ReadOnly: true, Deterministic: true, QuickTool: "quick.calendar",
			},
			Run: scheduleHandler,
		},
		"store.next_event": {
			Spec: intents.FunctionSpec{
				ID: "store.next_event", Args: map[string]slots.Type{},
				Class: intents.ClassLookup, ReadOnly: true, Deterministic: true, QuickTool: "quick.next_event",
			},
			Run: nextEventHandler,
		},
		// No QuickTool: §7's table names quick.free_time, but
		// QuickEligible() (R-6) requires Class == lookup, and free_slots
		// is correctly ClassCompute (it derives gaps from events, it
		// doesn't just fetch them) — loosening QuickEligible to admit
		// compute functions would weaken the "the model gets raw lookups,
		// never derived judgment calls" line QuickEligible is meant to
		// draw. The main agent can already call quick.calendar for the
		// raw events and compute gaps itself if it needs to reason about
		// them. Tier 0/1 can still match this intent directly; only the
		// main-agent tool exposure is narrowed.
		"store.free_slots": {
			Spec: intents.FunctionSpec{
				ID: "store.free_slots", Args: map[string]slots.Type{"when": slots.TypeDate, "part": slots.TypePartOfDay},
				Required: []string{}, Class: intents.ClassCompute, ReadOnly: true, Deterministic: true,
			},
			Run: freeSlotsHandler,
		},
		"store.latest_messages": {
			Spec: intents.FunctionSpec{
				ID: "store.latest_messages", Args: map[string]slots.Type{"n": slots.TypeCount},
				Class: intents.ClassLookup, ReadOnly: true, Deterministic: true, QuickTool: "quick.latest_mail",
			},
			Run: latestMailHandler,
		},
		"store.unread_count": {
			Spec: intents.FunctionSpec{
				ID: "store.unread_count", Args: map[string]slots.Type{},
				Class: intents.ClassLookup, ReadOnly: true, Deterministic: true,
			},
			Run: unreadCountHandler,
		},
		"store.latest_from": {
			Spec: intents.FunctionSpec{
				ID: "store.latest_from", Args: map[string]slots.Type{"who": slots.TypePerson}, Required: []string{"who"},
				Class: intents.ClassLookup, ReadOnly: true, Deterministic: true, QuickTool: "quick.mail_from",
			},
			Run: latestFromHandler,
		},
		"store.cached_brief": {
			Spec: intents.FunctionSpec{
				ID: "store.cached_brief", Args: map[string]slots.Type{},
				Class: intents.ClassLookup, ReadOnly: true, Deterministic: true, QuickTool: "quick.cached_brief",
			},
			Run: cachedBriefHandler,
		},
		"approvals.pending": {
			Spec: intents.FunctionSpec{
				ID: "approvals.pending", Args: map[string]slots.Type{},
				Class: intents.ClassLookup, ReadOnly: true, Deterministic: true, QuickTool: "quick.pending_approvals",
			},
			Run: approvalsListHandler,
		},
		"approvals.bind_pending": {
			Spec: intents.FunctionSpec{
				ID: "approvals.bind_pending", Args: map[string]slots.Type{},
				Class: intents.ClassControl, ReadOnly: true, Deterministic: true,
				SideEffects: []string{"approval_surface"},
			},
			Run: bindPendingHandler,
		},
		"control.cancel_tasks": {
			Spec: intents.FunctionSpec{
				ID: "control.cancel_tasks", Args: map[string]slots.Type{},
				Class: intents.ClassControl, ReadOnly: true, Deterministic: false,
				SideEffects: []string{"process_control"},
			},
			Run: cancelTasksHandler,
		},
		"status.overview": {
			Spec: intents.FunctionSpec{
				ID: "status.overview", Args: map[string]slots.Type{},
				Class: intents.ClassFormat, ReadOnly: true, Deterministic: false,
			},
			Run: statusOverviewHandler,
		},
		"help.intents": {
			Spec: intents.FunctionSpec{
				ID: "help.intents", Args: map[string]slots.Type{},
				Class: intents.ClassFormat, ReadOnly: true, Deterministic: true,
			},
			Run: helpIntentsHandler,
		},
	}
}

// Specs projects Table() to just the FunctionSpecs, for
// intents.LoadRegistry's Functions.Read.
func Specs() map[string]intents.FunctionSpec {
	t := Table()
	out := make(map[string]intents.FunctionSpec, len(t))
	for id, h := range t {
		out[id] = h.Spec
	}
	return out
}
