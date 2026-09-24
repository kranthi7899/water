package sync

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode"

	"water/internal/gate"
	"water/internal/store"
)

// DefaultPrefetchLeadTime is how far ahead of a calendar event's start
// Config.PrefetchLeadTime defaults to: an event starting within this window
// is prefetched (Slice M, section 2). It lines up with DefaultInterval so
// the events tick that refreshes the calendar also reliably catches events
// as they enter the window, rather than needing its own faster ticker.
const DefaultPrefetchLeadTime = 10 * time.Minute

func defaultPrefetchMailArgs(ev store.Event) map[string]any {
	if len(ev.Attendees) == 0 {
		return nil
	}
	parts := make([]string, 0, len(ev.Attendees))
	for _, a := range ev.Attendees {
		if a = strings.TrimSpace(a); a != "" {
			parts = append(parts, "from:"+a)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return map[string]any{"query": strings.Join(parts, " OR ")}
}

// defaultPrefetchDocsArgs searches Drive by the event title's plain words.
// The title is written by whoever sent the invite, so quotes, operators and
// other punctuation are dropped: gdrive.search_files passes anything that
// looks like Drive query syntax through raw, and without quotes or
// comparison operators no such clause can be valid.
func defaultPrefetchDocsArgs(ev store.Event) map[string]any {
	words := strings.FieldsFunc(ev.Title, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(words) == 0 {
		return nil
	}
	return map[string]any{"query": strings.Join(words, " ")}
}

// prefetchState is the events-tick-local bookkeeping maybePrefetch needs: it
// is not part of Config because it holds per-run state (which events have
// already been prefetched this process's life), not configuration.
type prefetchState struct {
	mu   sync.Mutex
	done map[string]bool
}

func newPrefetchState() *prefetchState { return &prefetchState{done: map[string]bool{}} }

// claim reports whether id has already been prefetched, marking it done
// either way — so a crash mid-prefetch never retries the same event forever,
// and a normal daemon life prefetches each event exactly once.
func (p *prefetchState) claim(id string) (already bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done[id] {
		return true
	}
	p.done[id] = true
	return false
}

// maybePrefetch looks in the store for calendar events already known to
// start within PrefetchLeadTime, and for each one not yet prefetched, pulls
// its attendees' recent mail and related Drive files into the store ahead
// of the meeting. This is a synchronous, CEO-adjacent read (an imminent
// meeting, not background drafting), so it runs at gate origin P1 with
// Clean taint, through the same gcal/gmail/gdrive read functions the sync
// loop already calls at level R — no new gate behavior (docs/slices/M.md
// section 2). It reads the calendar from the local store rather than
// calling gcal.list_events again: the events tick that calls maybePrefetch
// already just refreshed it.
func (r *Refresher) maybePrefetch(ctx context.Context) {
	if r.cfg.Store == nil {
		return
	}
	now := r.cfg.Now()
	evs, err := store.EventsInRange(ctx, r.cfg.Store, now, now.Add(r.cfg.PrefetchLeadTime))
	if err != nil {
		r.cfg.Logf("sync: prefetch: listing upcoming events: %v", err)
		return
	}
	for _, ev := range evs {
		if strings.EqualFold(ev.Status, "cancelled") {
			continue
		}
		r.prefetchEvent(ctx, ev)
	}
}

func (r *Refresher) prefetchEvent(ctx context.Context, ev store.Event) {
	if ev.SourceID == "" || r.prefetch.claim(ev.Source+":"+ev.SourceID) {
		return
	}
	if fn := r.cfg.PrefetchMailFunction; fn != "" {
		if args := r.cfg.PrefetchMailArgs(ev); args != nil {
			r.prefetchCall(ctx, fn, args)
		}
	}
	if fn := r.cfg.PrefetchDocsFunction; fn != "" {
		if args := r.cfg.PrefetchDocsArgs(ev); args != nil {
			r.prefetchCall(ctx, fn, args)
		}
	}
}

func (r *Refresher) prefetchCall(ctx context.Context, fn string, args map[string]any) {
	if _, err := r.cfg.Gate.Invoke(ctx, gate.Call{Function: fn, Args: args, Origin: gate.P1, Taint: gate.Clean}); err != nil {
		r.cfg.Logf("sync: prefetch: %s: %v", fn, err)
	}
}
