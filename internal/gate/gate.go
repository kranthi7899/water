// Package gate is the single permission check every connector call passes.
//
// It is default-deny against the twin manifest, enforces access levels
// R/D/A/S/B, refuses to let content derived from external sources drive an
// action without an approved envelope, applies rate and usage caps, and
// writes every decision to the audit log before anything runs. It is also
// the only code that can mint the permit a connector needs to execute, so
// there is no path to a connector that skips it.
package gate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/canon"
	"water/internal/connectors"
	"water/internal/gate/internal/mint"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

// Origin is where a call came from.
type Origin string

const (
	P0 Origin = "p0" // the CEO's immediate request
	P1 Origin = "p1" // a task the CEO approved or scheduled
	P2 Origin = "p2" // auto-mode background work
)

func (o Origin) valid() bool { return o == P0 || o == P1 || o == P2 }

// Taint says whether any argument derives from external content (email,
// chat, docs, web). The zero value is TaintUnknown, which is treated as
// tainted: a caller that forgets to say is not trusted.
type Taint int

const (
	TaintUnknown Taint = iota
	Clean
	Tainted
)

func (t Taint) String() string {
	switch t {
	case Clean:
		return "clean"
	case Tainted:
		return "tainted"
	}
	return "unknown"
}

// Call is one requested connector invocation.
type Call struct {
	Function   string // "connector.function"
	Args       map[string]any
	Origin     Origin
	Taint      Taint
	EnvelopeID string
}

// Result is what the caller gets back. Untrusted marks output that carries
// external content; anything derived from it must be passed back in as
// Tainted.
type Result struct {
	Output    json.RawMessage
	Records   []store.Record
	Untrusted bool
	Draft     bool
}

var ErrDenied = errors.New("denied by gate")

// DenyError carries the reason a call was refused.
type DenyError struct{ Reason string }

func (e *DenyError) Error() string        { return "denied by gate: " + e.Reason }
func (e *DenyError) Is(target error) bool { return target == ErrDenied }

func deny(format string, a ...any) error { return &DenyError{fmt.Sprintf(format, a...)} }

type Config struct {
	Manifest  *twins.Manifest
	Registry  *connectors.Registry
	Approvals *approvals.Queue
	Audit     *audit.Log
	Vault     vault.Vault
	Store     *store.Store // optional: normalized records are upserted here
	Now       func() time.Time
	// Subscription reports the last observed subscription window; auto-mode
	// model calls stop while it is not "allowed".
	Subscription func() *backend.RateLimit
}

type Gate struct {
	cfg   Config
	mu    sync.Mutex
	rates map[string][]time.Time
	// pruned is when each store-backed key's expired rate_hits rows were
	// last deleted. Guarded by mu.
	pruned map[string]time.Time
}

// pruneEvery throttles store-backed rate_hits pruning per key.
const pruneEvery = time.Hour

// pruneStoreLocked deletes key's rate_hits rows that have left its window
// (at or before since, the same boundary the count uses), at most once per
// pruneEvery. Each key is always counted over one window (a function's own
// RateCap, or the manifest's usage window for model keys), so this never
// drops a hit the window still counts. Callers hold g.mu.
func (g *Gate) pruneStoreLocked(ctx context.Context, key string, since, now time.Time) {
	if last, ok := g.pruned[key]; ok && now.Sub(last) < pruneEvery {
		return
	}
	if g.pruned == nil {
		g.pruned = map[string]time.Time{}
	}
	g.pruned[key] = now
	_ = g.cfg.Store.PruneKeyHitsBefore(ctx, key, since)
}

// compatible reports whether a manifest may grant level to a function whose
// connector declares declared. Grants may tighten to A or B, never loosen.
func compatible(declared, granted twins.Level) bool {
	if granted == twins.B || granted == declared {
		return true
	}
	return granted == twins.A && declared != twins.B
}

func New(cfg Config) (*Gate, error) {
	if cfg.Manifest == nil || cfg.Registry == nil || cfg.Approvals == nil || cfg.Audit == nil || cfg.Vault == nil {
		return nil, errors.New("gate: manifest, registry, approvals, audit and vault are required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	for _, id := range cfg.Manifest.FunctionIDs() {
		f, _ := cfg.Manifest.Function(id)
		_, spec, ok := cfg.Registry.Lookup(id)
		if !ok {
			if f.Level == twins.B {
				continue
			}
			return nil, fmt.Errorf("gate: manifest lists %s but no connector provides it", id)
		}
		if !compatible(spec.Level, f.Level) {
			return nil, fmt.Errorf("gate: manifest grants %s level %s but its connector declares %s", id, f.Level, spec.Level)
		}
	}
	return &Gate{cfg: cfg, rates: map[string][]time.Time{}}, nil
}

func (g *Gate) record(r audit.Record) error {
	if _, err := g.cfg.Audit.Append(r); err != nil {
		return fmt.Errorf("gate: audit unavailable, refusing to act: %w", err)
	}
	return nil
}

// Invoke authorizes and, if allowed, executes one call.
func (g *Gate) Invoke(ctx context.Context, c Call) (Result, error) {
	if c.Args == nil {
		c.Args = map[string]any{}
	}
	argsHash, hashErr := canon.Hash(c.Args)
	if hashErr != nil {
		argsHash = "unhashable"
	}
	base := audit.Record{Function: c.Function, EnvelopeID: c.EnvelopeID, Origin: string(c.Origin), ArgsHash: argsHash}
	rec := func(kind audit.Kind, allowed bool, reason string) error {
		r := base
		r.Kind, r.Allowed, r.Reason = kind, allowed, reason
		return g.record(r)
	}
	if err := rec(audit.KindCall, false, fmt.Sprintf("received taint=%s", c.Taint)); err != nil {
		return Result{}, err
	}
	refuse := func(e error) (Result, error) {
		if aerr := rec(audit.KindDenial, false, strings.TrimPrefix(e.Error(), "denied by gate: ")); aerr != nil {
			return Result{}, errors.Join(e, aerr)
		}
		return Result{}, e
	}
	if hashErr != nil {
		return refuse(deny("arguments are not canonical JSON"))
	}

	conn, spec, level, claim, err := g.authorize(c)
	if err != nil {
		return refuse(err)
	}
	f, _ := g.cfg.Manifest.Function(c.Function)
	if !g.takeRate(c.Function, f.Rate) {
		return refuse(deny("rate cap for %s reached (%d per %s)", c.Function, f.Rate.Max, time.Duration(f.Rate.Per)))
	}
	var cred vault.Secret
	if svc, acct := conn.Credential(); svc != "" {
		cred, err = g.cfg.Vault.Get(svc, acct)
		if err != nil {
			return refuse(deny("credential for %s is unavailable", conn.Name()))
		}
	}
	basis := "level " + string(level)
	if claim {
		if _, err := g.cfg.Approvals.Claim(ctx, c.EnvelopeID, c.Function, argsHash); err != nil {
			return refuse(deny("envelope %s: %v", c.EnvelopeID, err))
		}
		basis += " with approved envelope"
	}
	if err := rec(audit.KindDecision, true, basis); err != nil {
		return Result{}, err
	}

	_, short, _ := strings.Cut(c.Function, ".")
	p, err := mint.New(short, c.Args, cred)
	if err != nil {
		return Result{}, err
	}
	raw, ierr := conn.Invoke(ctx, p)
	if ierr == nil && !mint.Redeemed(p) {
		ierr = errors.New("connector returned without redeeming its permit")
	}
	if ierr != nil {
		// Scrub any credential text but keep the original error reachable
		// via Unwrap, so a connector's typed sentinel (gmail.ErrHistoryTooOld,
		// gcal.ErrSyncTokenExpired, ...) still matches errors.Is once this
		// error is itself wrapped below. scrubbedError.Error() only ever
		// returns the redacted text, never the original's, so the credential
		// never resurfaces through Unwrap's chain.
		ierr = &scrubbedError{msg: redact(ierr.Error(), cred), err: ierr}
		if aerr := rec(audit.KindExecute, false, "failed: "+ierr.Error()); aerr != nil {
			return Result{}, errors.Join(ierr, aerr)
		}
		return Result{}, fmt.Errorf("%s: %w", c.Function, ierr)
	}
	if !cred.IsZero() && strings.Contains(string(raw), cred.Reveal()) {
		ierr = errors.New("connector output contained its credential and was discarded")
		if aerr := rec(audit.KindExecute, false, ierr.Error()); aerr != nil {
			return Result{}, errors.Join(ierr, aerr)
		}
		return Result{}, fmt.Errorf("%s: %w", c.Function, ierr)
	}
	if err := rec(audit.KindExecute, true, "ok"); err != nil {
		return Result{}, err
	}

	res := Result{Output: raw, Untrusted: spec.External, Draft: level == twins.D}
	res.Records, err = conn.Normalize(short, raw)
	if err != nil {
		return res, fmt.Errorf("%s: normalize: %w", c.Function, err)
	}
	if g.cfg.Store != nil {
		for _, r := range res.Records {
			if err := g.cfg.Store.Upsert(ctx, r); err != nil {
				return res, fmt.Errorf("%s: store: %w", c.Function, err)
			}
		}
	}
	return res, nil
}

// authorize applies every static rule. It has no side effects. Its bool
// result says whether Invoke must claim c.EnvelopeID: always when the level
// and taint require an envelope, and also whenever the caller presents one at
// all. An approved envelope is consumed exactly once against its action and
// payload hash whatever taint the executing call carries (the daemon runs an
// approved S envelope that was queued only for its taint), so it moves to
// Executed and is never later expired as though it had not run.
func (g *Gate) authorize(c Call) (connectors.Connector, connectors.Function, twins.Level, bool, error) {
	var none connectors.Function
	if !c.Origin.valid() {
		return nil, none, "", false, deny("unknown origin %q", c.Origin)
	}
	f, ok := g.cfg.Manifest.Function(c.Function)
	if !ok {
		return nil, none, "", false, deny("%s is not in the %s manifest", c.Function, g.cfg.Manifest.ID)
	}
	if f.Level == twins.B {
		return nil, none, "", false, deny("%s is blocked (level B)", c.Function)
	}
	conn, spec, ok := g.cfg.Registry.Lookup(c.Function)
	if !ok {
		return nil, none, "", false, deny("no connector provides %s", c.Function)
	}
	if err := spec.Schema.Validate(c.Args); err != nil {
		return nil, none, "", false, deny("%s: %v", c.Function, err)
	}
	if c.Origin == P2 {
		if !g.cfg.Manifest.AutoAllowed(c.Function) {
			return nil, none, "", false, deny("auto mode may not call %s (not on the auto allowlist)", c.Function)
		}
		if (f.Level != twins.R && f.Level != twins.D) || spec.Level == twins.A {
			return nil, none, "", false, deny("auto mode never performs outward actions (%s is level %s)", c.Function, f.Level)
		}
	}
	needEnvelope := NeedsEnvelope(f.Level, c.Taint)
	if needEnvelope && c.EnvelopeID == "" {
		why := "level A requires an approved envelope"
		if f.Level == twins.S {
			why = "arguments derive from external content (or taint is unknown), so level S requires an approved envelope"
		}
		return nil, none, "", false, deny("%s: %s", c.Function, why)
	}
	return conn, spec, f.Level, needEnvelope || c.EnvelopeID != "", nil
}

// NeedsEnvelope reports whether a call at level with the given taint must go
// through an approved envelope rather than executing inline: every A-level
// call does, and an S-level call does whenever its arguments derive from
// external content (or taint is unknown). Callers that want to propose an
// envelope themselves before ever reaching Invoke (the daemon's model-tool
// bridge) use this to decide that without duplicating gate.authorize.
func NeedsEnvelope(level twins.Level, taint Taint) bool {
	return level == twins.A || (level == twins.S && taint != Clean)
}

func (g *Gate) takeRate(key string, rc *twins.RateCap) bool {
	if rc == nil {
		return true
	}
	return g.take(key, rc.Max, time.Duration(rc.Per))
}

// take records one use of key in a sliding window if the cap allows it. When
// a store is configured, the window is counted there instead of only in
// memory, so caps survive a daemon restart.
func (g *Gate) take(key string, max int, per time.Duration) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.cfg.Now()
	if g.cfg.Store != nil {
		return g.takeStoreLocked(key, max, per, now)
	}
	hits := g.live(key, now, per)
	if len(hits) >= max {
		return false
	}
	g.rates[key] = append(hits, now)
	return true
}

// takeStoreLocked is take's store-backed path. Callers hold g.mu, which
// serialises it against every other window check in this process; only one
// daemon process ever holds the store open (the lock file guarantees that),
// so this is enough to make the check-then-insert atomic in practice.
func (g *Gate) takeStoreLocked(key string, max int, per time.Duration, now time.Time) bool {
	ctx := context.Background()
	g.pruneStoreLocked(ctx, key, now.Add(-per), now)
	n, err := g.cfg.Store.CountHitsSince(ctx, key, now.Add(-per))
	if err != nil || n >= max {
		return false
	}
	return g.cfg.Store.RecordHit(ctx, key, now) == nil
}

// ModelCall charges one model call against the manifest's usage cap. P2
// calls also count against the auto share and stop while the subscription
// window is limited.
func (g *Gate) ModelCall(origin Origin) error {
	u := g.cfg.Manifest.Usage
	rec := func(allowed bool, reason string) error {
		return g.record(audit.Record{Kind: audit.KindDecision, Function: "model", Origin: string(origin), Allowed: allowed, Reason: reason})
	}
	refuse := func(e error) error {
		if aerr := rec(false, strings.TrimPrefix(e.Error(), "denied by gate: ")); aerr != nil {
			return errors.Join(e, aerr)
		}
		return e
	}
	if !origin.valid() {
		return refuse(deny("unknown origin %q", origin))
	}
	if origin == P2 && g.cfg.Subscription != nil {
		if rl := g.cfg.Subscription(); rl != nil && rl.Status != "" && rl.Status != "allowed" {
			return refuse(deny("auto mode paused: subscription window is %s", rl.Status))
		}
	}
	window := time.Duration(u.Window)
	if msg := g.takeModel(origin == P2, u, window); msg != "" {
		return refuse(deny("%s", msg))
	}
	return rec(true, "within usage cap")
}

// takeModel charges the shared cap and, for auto work, the auto share in
// one step so concurrent callers cannot overshoot either.
func (g *Gate) takeModel(auto bool, u twins.Usage, window time.Duration) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.cfg.Now()
	if g.cfg.Store != nil {
		return g.takeModelStoreLocked(auto, u, window, now)
	}
	all, autoHits := g.live("model", now, window), g.live("model:auto", now, window)
	if auto && len(autoHits) >= u.AutoModelCalls {
		return fmt.Sprintf("auto model-call cap reached (%d per %s)", u.AutoModelCalls, window)
	}
	if len(all) >= u.ModelCalls {
		return fmt.Sprintf("model-call cap reached (%d per %s)", u.ModelCalls, window)
	}
	g.rates["model"] = append(all, now)
	if auto {
		g.rates["model:auto"] = append(autoHits, now)
	}
	return ""
}

func (g *Gate) takeModelStoreLocked(auto bool, u twins.Usage, window time.Duration, now time.Time) string {
	ctx := context.Background()
	since := now.Add(-window)
	g.pruneStoreLocked(ctx, "model", since, now)
	g.pruneStoreLocked(ctx, "model:auto", since, now)
	if auto {
		n, err := g.cfg.Store.CountHitsSince(ctx, "model:auto", since)
		if err != nil {
			return "auto model-call cap unavailable"
		}
		if n >= u.AutoModelCalls {
			return fmt.Sprintf("auto model-call cap reached (%d per %s)", u.AutoModelCalls, window)
		}
	}
	n, err := g.cfg.Store.CountHitsSince(ctx, "model", since)
	if err != nil {
		return "model-call cap unavailable"
	}
	if n >= u.ModelCalls {
		return fmt.Sprintf("model-call cap reached (%d per %s)", u.ModelCalls, window)
	}
	if err := g.cfg.Store.RecordHit(ctx, "model", now); err != nil {
		return "model-call cap unavailable"
	}
	if auto {
		if err := g.cfg.Store.RecordHit(ctx, "model:auto", now); err != nil {
			return "auto model-call cap unavailable"
		}
	}
	return ""
}

// live drops hits older than per and returns the rest. Callers hold g.mu.
func (g *Gate) live(key string, now time.Time, per time.Duration) []time.Time {
	hits := g.rates[key][:0]
	for _, t := range g.rates[key] {
		if now.Sub(t) < per {
			hits = append(hits, t)
		}
	}
	g.rates[key] = hits
	return hits
}

// scrubbedError carries a credential-redacted message for display and
// auditing while preserving the original connector error's identity for
// errors.Is/errors.As, via Unwrap.
type scrubbedError struct {
	msg string
	err error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.err }

func redact(s string, cred vault.Secret) string {
	if cred.IsZero() {
		return s
	}
	return strings.ReplaceAll(s, cred.Reveal(), "[redacted]")
}
