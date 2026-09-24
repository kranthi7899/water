// Package sync is the daemon's small background refresher: it keeps the
// CEO's near-term calendar and recent mail fresh in the store through the
// gate, at background-work origin P2, so a conversation's fast path already
// has current data instead of only whatever a prior chat happened to read.
// It never touches a credential directly (the gate resolves that per call),
// but it checks whether one is stored before calling, so an unconnected twin
// logs one quiet line instead of a stream of denials every interval.
//
// Mail and calendar refresh on independent tickers (mail far more often: a
// personal calendar changes rarely, but new mail is worth checking inside a
// minute) and each call is incremental when the connector has a cursor to
// resume from (Gmail's history_id, gcal's syncToken), falling back to a full
// fetch — and a fresh cursor — whenever the connector reports its cursor has
// expired.
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"water/internal/connectors/google/gcal"
	"water/internal/connectors/google/gmail"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/vault"
)

// DefaultInterval is how often events refresh when Config.EventsInterval (or
// the deprecated Config.Interval) is unset.
const DefaultInterval = 10 * time.Minute

// DefaultMailInterval is how often mail refreshes when Config.MailInterval is
// unset. Gmail's per-user quota (6000 units/min; history.list costs about 2)
// makes this trivially cheap.
const DefaultMailInterval = 60 * time.Second

// DefaultBriefReadyAfter is the local time of day the background precompute
// starts trying to have a morning brief cached, when Config.BriefReadyAfter
// is unset.
const DefaultBriefReadyAfter = "07:00"

const (
	defaultEventsFunction = "gcal.list_events"
	defaultMailFunction   = "gmail.list_messages"
	// eventsWindow is how far ahead a full (cursor-less) list_events call
	// looks, from the start of today local time.
	eventsWindow = 7 * 24 * time.Hour
	mailQuery    = "newer_than:1d"

	defaultEventsCursorKey   = "gcal:primary:sync_token"
	defaultMailCursorKey     = "gmail:history_id"
	defaultEventsCursorField = "next_sync_token"
	defaultMailCursorField   = "history_id"
)

// Config configures a Refresher. Gate and Vault are required; Service and
// Account name the credential Refresher checks for before syncing (in
// production, gapi.Service/gapi.DefaultAccount — the one credential all
// three Google connectors share). Everything else has a default; see New.
type Config struct {
	Gate  *gate.Gate
	Vault vault.Vault
	// Store, when set, backs incremental-sync cursors and the morning
	// brief's background precompute. Both features are skipped (falling
	// back to whatever EventsArgs/MailArgs do with an empty cursor) when
	// it is nil, which existing tests that don't care about either rely on.
	Store *store.Store

	Service, Account string

	// Interval is a deprecated alias for EventsInterval, kept so an existing
	// sync.interval_minutes config value still governs the calendar cadence
	// after mail split onto its own, faster ticker; it is ignored once
	// EventsInterval is set directly.
	Interval                     time.Duration
	MailInterval, EventsInterval time.Duration

	Now func() time.Time
	// Logf receives one line per skip, per failed sync call, or per cursor
	// event (expired/cleared). It defaults to a no-op; the daemon passes
	// something that writes to stderr.
	Logf func(format string, args ...any)

	// EventsFunction and MailFunction default to "gcal.list_events" and
	// "gmail.list_messages"; tests point them at fake connectors instead so
	// no network is involved.
	EventsFunction, MailFunction string
	// EventsArgs and MailArgs build each call's arguments from the current
	// time and the cursor last persisted for that call (""  when there is
	// none, meaning "do a full fetch"). They default to gcal's and gmail's
	// real shape.
	EventsArgs, MailArgs func(now time.Time, cursor string) map[string]any

	// EventsCursorKey/MailCursorKey are the store.sync_cursors keys each
	// call's cursor is kept under; EventsCursorField/MailCursorField name
	// the JSON field in that call's successful raw output carrying the next
	// cursor to persist. Defaults match gcal/gmail's real output shape
	// ("next_sync_token" / "history_id"); tests override both to match
	// whatever fake connector they point EventsFunction/MailFunction at.
	EventsCursorKey, MailCursorKey     string
	EventsCursorField, MailCursorField string
	// EventsExpiredErr/MailExpiredErr identify the sentinel error each call
	// returns when its cursor is no longer valid server-side (gcal's 410,
	// Gmail's 404 on a too-old historyId); on a match (via errors.Is) the
	// stored cursor is deleted so the next tick does a full fetch and
	// re-seeds a fresh one. Defaults are gcal.ErrSyncTokenExpired and
	// gmail.ErrHistoryTooOld.
	EventsExpiredErr, MailExpiredErr error

	// Brief, when set, is called from the events tick once local time is
	// past BriefReadyAfter and no brief is cached yet for today — the
	// background half of the morning brief's compute-once path
	// (internal/runtime/brief.go). This package holds no brief logic of its
	// own; it only decides when to ask.
	Brief func(ctx context.Context) error
	// BriefReadyAfter is "HH:MM" local time; default "07:00".
	BriefReadyAfter string

	// PrefetchLeadTime is how far ahead of a calendar event's start
	// maybePrefetch looks; default DefaultPrefetchLeadTime. Skipped
	// entirely (no prefetch call ever made) when Store is nil.
	PrefetchLeadTime time.Duration
	// PrefetchMailFunction/PrefetchDocsFunction default to
	// "gmail.list_messages"/"gdrive.search_files"; tests point them at fake
	// connectors instead. Either may be left "" to skip that half of the
	// prefetch.
	PrefetchMailFunction, PrefetchDocsFunction string
	// PrefetchMailArgs/PrefetchDocsArgs build one call's arguments from the
	// event being prefetched; a nil return skips that call (e.g. an event
	// with no attendees). Defaults build a Gmail "from:a OR from:b" query
	// from attendees, and a Drive keyword query from the event title.
	PrefetchMailArgs, PrefetchDocsArgs func(store.Event) map[string]any

	// AgentMail, when set, is called on its own independent ticker, a third
	// loop next to events and mail: internal/agentmail's inbound-triage
	// watcher polling the agent's own second mailbox. Left nil (the
	// default), Run behaves exactly as before this existed -- two loops,
	// not three -- so every test and caller that doesn't set it is
	// unaffected.
	AgentMail func(ctx context.Context)
	// AgentMailInterval defaults to MailInterval (or DefaultMailInterval)
	// when AgentMail is set and this is unset.
	AgentMailInterval time.Duration
}

func (c *Config) setDefaults() {
	if c.EventsInterval <= 0 {
		if c.Interval > 0 {
			c.EventsInterval = c.Interval
		} else {
			c.EventsInterval = DefaultInterval
		}
	}
	if c.MailInterval <= 0 {
		c.MailInterval = DefaultMailInterval
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logf == nil {
		c.Logf = func(string, ...any) {}
	}
	if c.EventsFunction == "" {
		c.EventsFunction = defaultEventsFunction
	}
	if c.MailFunction == "" {
		c.MailFunction = defaultMailFunction
	}
	if c.EventsArgs == nil {
		c.EventsArgs = defaultEventsArgs
	}
	if c.MailArgs == nil {
		c.MailArgs = defaultMailArgs
	}
	if c.EventsCursorKey == "" {
		c.EventsCursorKey = defaultEventsCursorKey
	}
	if c.MailCursorKey == "" {
		c.MailCursorKey = defaultMailCursorKey
	}
	if c.EventsCursorField == "" {
		c.EventsCursorField = defaultEventsCursorField
	}
	if c.MailCursorField == "" {
		c.MailCursorField = defaultMailCursorField
	}
	if c.EventsExpiredErr == nil {
		c.EventsExpiredErr = gcal.ErrSyncTokenExpired
	}
	if c.MailExpiredErr == nil {
		c.MailExpiredErr = gmail.ErrHistoryTooOld
	}
	if c.BriefReadyAfter == "" {
		c.BriefReadyAfter = DefaultBriefReadyAfter
	}
	if c.PrefetchLeadTime <= 0 {
		c.PrefetchLeadTime = DefaultPrefetchLeadTime
	}
	if c.PrefetchMailFunction == "" {
		c.PrefetchMailFunction = defaultMailFunction
	}
	if c.PrefetchDocsFunction == "" {
		c.PrefetchDocsFunction = "gdrive.search_files"
	}
	if c.PrefetchMailArgs == nil {
		c.PrefetchMailArgs = defaultPrefetchMailArgs
	}
	if c.PrefetchDocsArgs == nil {
		c.PrefetchDocsArgs = defaultPrefetchDocsArgs
	}
	if c.AgentMailInterval <= 0 {
		c.AgentMailInterval = c.MailInterval
	}
}

func defaultEventsArgs(now time.Time, cursor string) map[string]any {
	if cursor != "" {
		return map[string]any{"sync_token": cursor}
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := start.Add(eventsWindow)
	return map[string]any{"time_min": start.Format(time.RFC3339), "time_max": end.Format(time.RFC3339)}
}

func defaultMailArgs(_ time.Time, cursor string) map[string]any {
	if cursor != "" {
		return map[string]any{"since_history_id": cursor}
	}
	return map[string]any{"query": mailQuery}
}

// Refresher runs Config's periodic sync.
type Refresher struct {
	cfg      Config
	prefetch *prefetchState
}

// New builds a Refresher, filling in defaults.
func New(cfg Config) *Refresher {
	cfg.setDefaults()
	return &Refresher{cfg: cfg, prefetch: newPrefetchState()}
}

// Run syncs immediately, then keeps mail and events refreshing on their own
// independent tickers, until ctx is cancelled — the daemon ties ctx to its
// own shutdown signal, so this stops with the rest of the process. The two
// loops never block each other: a slow or failing calendar call never delays
// the next mail check, and vice versa.
func (r *Refresher) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); r.loop(ctx, r.cfg.MailInterval, r.RunOnceMail) }()
	go func() { defer wg.Done(); r.loop(ctx, r.cfg.EventsInterval, r.RunOnceEvents) }()
	if r.cfg.AgentMail != nil {
		wg.Add(1)
		go func() { defer wg.Done(); r.loop(ctx, r.cfg.AgentMailInterval, r.cfg.AgentMail) }()
	}
	wg.Wait()
}

func (r *Refresher) loop(ctx context.Context, interval time.Duration, tick func(context.Context)) {
	tick(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick(ctx)
		}
	}
}

// RunOnce runs one events pass and one mail pass immediately, without
// waiting for either ticker. It is exported so tests (and a manual trigger)
// don't have to wait one out.
func (r *Refresher) RunOnce(ctx context.Context) {
	r.RunOnceEvents(ctx)
	r.RunOnceMail(ctx)
}

// RunOnceEvents runs one calendar sync pass, then — since this is the slower
// of the two ticks — checks whether the morning brief's background
// precompute is due.
func (r *Refresher) RunOnceEvents(ctx context.Context) {
	if !r.checkConnected(r.cfg.EventsFunction) {
		return
	}
	r.tick(ctx, r.cfg.EventsFunction, r.cfg.EventsCursorKey, r.cfg.EventsCursorField, r.cfg.EventsExpiredErr, r.cfg.EventsArgs)
	r.maybePrecomputeBrief(ctx)
	r.maybePrefetch(ctx)
}

// RunOnceMail runs one mail sync pass.
func (r *Refresher) RunOnceMail(ctx context.Context) {
	if !r.checkConnected(r.cfg.MailFunction) {
		return
	}
	r.tick(ctx, r.cfg.MailFunction, r.cfg.MailCursorKey, r.cfg.MailCursorField, r.cfg.MailExpiredErr, r.cfg.MailArgs)
}

func (r *Refresher) checkConnected(fn string) bool {
	if _, err := r.cfg.Vault.Get(r.cfg.Service, r.cfg.Account); err != nil {
		r.cfg.Logf("sync: skipping %s, google is not connected (%v)", fn, err)
		return false
	}
	return true
}

// tick loads fn's stored cursor (if any), calls it through the gate, and on
// success persists the cursor field from the raw output. On the connector's
// own "cursor expired" sentinel (checked with errors.Is through the gate's
// returned error) it deletes the stale cursor and logs it; the next tick
// then naturally falls back to a full fetch (no cursor set) and re-seeds a
// fresh one from that response, mirroring exactly what list_events/
// list_messages already do unmodified when called with no cursor at all.
func (r *Refresher) tick(ctx context.Context, fn, cursorKey, cursorField string, expiredErr error, argsFn func(time.Time, string) map[string]any) {
	now := r.cfg.Now()
	cursor := r.loadCursor(ctx, fn, cursorKey)

	res, err := r.cfg.Gate.Invoke(ctx, gate.Call{
		Function: fn,
		Args:     argsFn(now, cursor),
		Origin:   gate.P2,
		Taint:    gate.Clean,
	})
	if err != nil {
		if cursor != "" && expiredErr != nil && errors.Is(err, expiredErr) {
			r.cfg.Logf("sync: %s: cursor expired, clearing %s for a full resync", fn, cursorKey)
			if derr := r.cfg.Store.DeleteCursor(ctx, cursorKey); derr != nil {
				r.cfg.Logf("sync: %s: clearing cursor %s: %v", fn, cursorKey, derr)
			}
			return
		}
		r.cfg.Logf("sync: %s: %v", fn, err)
		return
	}
	if truncated(res.Output) {
		// The connector cut its output short and withheld a cursor that
		// would skip what it cut (gcal's windowed seed past max events).
		r.cfg.Logf("sync: %s: output truncated, cursor %s not advanced", fn, cursorKey)
	}
	if r.cfg.Store == nil || cursorField == "" {
		return
	}
	if next, ok := stringField(res.Output, cursorField); ok && next != "" {
		if err := r.cfg.Store.SetCursor(ctx, cursorKey, next); err != nil {
			r.cfg.Logf("sync: %s: persisting cursor %s: %v", fn, cursorKey, err)
		}
	}
}

func (r *Refresher) loadCursor(ctx context.Context, fn, key string) string {
	if r.cfg.Store == nil {
		return ""
	}
	v, ok, err := r.cfg.Store.GetCursor(ctx, key)
	if err != nil {
		r.cfg.Logf("sync: %s: reading cursor %s: %v", fn, key, err)
		return ""
	}
	if !ok {
		return ""
	}
	return v
}

// truncated reports whether a connector output sets a top-level
// "truncated": true.
func truncated(raw json.RawMessage) bool {
	var m struct {
		Truncated bool `json:"truncated"`
	}
	return json.Unmarshal(raw, &m) == nil && m.Truncated
}

// stringField reads one top-level string field out of a JSON object, for
// pulling a connector's next-cursor field out of its raw call output.
func stringField(raw json.RawMessage, field string) (string, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", false
	}
	v, ok := m[field]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", false
	}
	return s, true
}

// maybePrecomputeBrief checks, once per events tick, whether it is past
// local BriefReadyAfter and today's brief is not yet cached — if so, it
// computes and caches it via the same Config.Brief function the on-demand
// fast-path uses, so the CEO usually finds it already sitting there.
func (r *Refresher) maybePrecomputeBrief(ctx context.Context) {
	if r.cfg.Brief == nil || r.cfg.Store == nil {
		return
	}
	now := r.cfg.Now()
	ready, err := readyTime(r.cfg.BriefReadyAfter, now)
	if err != nil {
		r.cfg.Logf("sync: brief: ready_after %q: %v", r.cfg.BriefReadyAfter, err)
		return
	}
	if now.Before(ready) {
		return
	}
	day := now.Format("2006-01-02")
	if _, ok, err := r.cfg.Store.GetBrief(ctx, day); err != nil {
		r.cfg.Logf("sync: brief: reading cache: %v", err)
		return
	} else if ok {
		return
	}
	if err := r.cfg.Brief(ctx); err != nil {
		r.cfg.Logf("sync: brief: %v", err)
	}
}

func readyTime(hhmm string, now time.Time) (time.Time, error) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, err
	}
	y, m, d := now.Date()
	return time.Date(y, m, d, t.Hour(), t.Minute(), 0, 0, now.Location()), nil
}
