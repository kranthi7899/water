// Package sync is the daemon's small background refresher: periodically it
// pulls the CEO's near-term calendar and yesterday's mail into the store
// through the gate, at background-work origin P2, so a conversation's fast
// path already has fresh data instead of only whatever a prior chat happened
// to read. It never touches a credential directly (the gate resolves that
// per call), but it checks whether one is stored before calling, so an
// unconnected twin logs one quiet line instead of a stream of denials every
// interval.
package sync

import (
	"context"
	"time"

	"water/internal/gate"
	"water/internal/vault"
)

// DefaultInterval is how often Run refreshes when Config.Interval is unset.
const DefaultInterval = 10 * time.Minute

const (
	defaultEventsFunction = "gcal.list_events"
	defaultMailFunction   = "gmail.list_messages"
	// eventsWindow is how far ahead list_events looks, from the start of
	// today local time.
	eventsWindow = 7 * 24 * time.Hour
	mailQuery    = "newer_than:1d"
)

// Config configures a Refresher. Gate and Vault are required; Service and
// Account name the credential Refresher checks for before syncing (in
// production, gapi.Service/gapi.DefaultAccount — the one credential all
// three Google connectors share). Everything else has a default; see New.
type Config struct {
	Gate  *gate.Gate
	Vault vault.Vault

	Service, Account string
	Interval         time.Duration
	Now              func() time.Time
	// Logf receives one line per skip or per failed sync call. It defaults
	// to a no-op; the daemon passes something that writes to stderr.
	Logf func(format string, args ...any)
	// EventsFunction and MailFunction default to "gcal.list_events" and
	// "gmail.list_messages"; tests point them at the fake_* connectors
	// instead so no network is involved.
	EventsFunction, MailFunction string
	// EventsArgs and MailArgs build each call's arguments from the current
	// time. They default to gcal's and gmail's real shape (a time_min/
	// time_max window and a Gmail search query); tests overriding
	// EventsFunction/MailFunction with a different schema override these too.
	EventsArgs, MailArgs func(now time.Time) map[string]any
}

func (c *Config) setDefaults() {
	if c.Interval <= 0 {
		c.Interval = DefaultInterval
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
}

func defaultEventsArgs(now time.Time) map[string]any {
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := start.Add(eventsWindow)
	return map[string]any{"time_min": start.Format(time.RFC3339), "time_max": end.Format(time.RFC3339)}
}

func defaultMailArgs(time.Time) map[string]any {
	return map[string]any{"query": mailQuery}
}

// Refresher runs Config's periodic sync.
type Refresher struct{ cfg Config }

// New builds a Refresher, filling in defaults.
func New(cfg Config) *Refresher {
	cfg.setDefaults()
	return &Refresher{cfg: cfg}
}

// Run syncs once immediately, then every Config.Interval, until ctx is
// cancelled — the daemon ties ctx to its own shutdown signal, so this stops
// with the rest of the process.
func (r *Refresher) Run(ctx context.Context) {
	r.RunOnce(ctx)
	t := time.NewTicker(r.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.RunOnce(ctx)
		}
	}
}

// RunOnce runs a single sync pass: today through +7 days of calendar events,
// and mail from the last day. It is exported so tests don't have to wait out
// a real ticker.
func (r *Refresher) RunOnce(ctx context.Context) {
	if _, err := r.cfg.Vault.Get(r.cfg.Service, r.cfg.Account); err != nil {
		r.cfg.Logf("sync: skipping, google is not connected (%v)", err)
		return
	}
	now := r.cfg.Now()
	if _, err := r.cfg.Gate.Invoke(ctx, gate.Call{
		Function: r.cfg.EventsFunction,
		Args:     r.cfg.EventsArgs(now),
		Origin:   gate.P2,
		Taint:    gate.Clean,
	}); err != nil {
		r.cfg.Logf("sync: %s: %v", r.cfg.EventsFunction, err)
	}
	if _, err := r.cfg.Gate.Invoke(ctx, gate.Call{
		Function: r.cfg.MailFunction,
		Args:     r.cfg.MailArgs(now),
		Origin:   gate.P2,
		Taint:    gate.Clean,
	}); err != nil {
		r.cfg.Logf("sync: %s: %v", r.cfg.MailFunction, err)
	}
}
