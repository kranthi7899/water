package nervous

import (
	"context"
	"errors"
	"sync"
	"time"

	"water/internal/nervous/intents"
	"water/internal/nervous/reflex"
	"water/internal/nervous/render"
	"water/internal/nervous/slots"
	"water/internal/nervous/speak"
	"water/internal/nervous/tmpl"
	"water/internal/nervous/turn"
	"water/internal/runtime"
	"water/internal/store"
)

// Turn is one channel's request into the front door. It carries only the
// raw utterance Tier 0 matches (Text) and, separately, any untrusted
// meeting-context prefix a later task adds ahead of the model's own prompt
// (Context) — Tier 0 must never see that prefix, since matching untrusted
// text against a template could be steered by whoever is speaking in the
// meeting, not the CEO.
type Turn struct {
	Channel runtime.Channel
	Text    string
	Context string
	TaskID  string
}

// Config wires the front door to its dependencies. It intentionally holds
// only what this task (R-12: registry + Tier 0 + the main-path escalation)
// needs; later tasks (quick tools, Tier 1, write intents, voice approval,
// the promotion loop) extend it as they land, following the plan's flags
// table (Design §17) — nothing here anticipates fields those tasks haven't
// been built yet.
type Config struct {
	// Registry returns the current intent registry. It's a function, not a
	// bare pointer, so a later task (R-22/23's atomic reload) can swap it
	// without this package's callers needing to change.
	Registry func() *intents.Registry
	Style    *render.Style
	// Turns is the shared turn-state table. New creates one with
	// turn.NewTable(time.Now) if this is nil.
	Turns *turn.Table
	// Tasks lets control.stop reach the daemon's in-flight-turn bookkeeping.
	// Nil is fine (reflex's cancelTasksHandler treats it as "0 running").
	Tasks reflex.TaskControl

	// OnVoiceLint receives the main path's speak.Lint warnings for a voice
	// turn, if any. R-14 both reads these into the route_log row AND still
	// calls this hook if set, so a later task (e.g. a live status view) can
	// observe them without going through the store.
	OnVoiceLint func(warnings []string)

	Tier0Enabled bool
	MainEnabled  bool

	// AckAfter bounds how long an unrouted turn waits before Handle emits a
	// handoff acknowledgement on its own (Design §17: default 250ms).
	AckAfter time.Duration
	// Clock is realClock{} unless a test supplies a fake one.
	Clock Clock

	// SenderWindow/SenderLimit bound the person-entity list Handle builds
	// per turn from recent message senders (Design §4/"Person entities come
	// from message senders", Risk 14: 180 days, kept local).
	SenderWindow time.Duration
	SenderLimit  int

	// Store is the writer *store.Store route_log/intent_state rows go
	// through. Nil is accepted (route logging is then a no-op) so existing
	// tests that don't care about it keep working unchanged.
	Store *store.Store
	// Breaker configures Tier 0's circuit breaker (Design §11.4/§17).
	Breaker BreakerConfig
	// MissWindow bounds the possible-miss detection window (default 60s)
	// and doubles as the lookback ListRoutes uses to find "the previous row
	// on this channel".
	MissWindow time.Duration
	// Retention bounds how long route_log rows are kept before
	// pruneRoutesOncePerDay deletes them (default 90 days).
	Retention time.Duration
}

// DefaultConfig returns Config with every flag/timing at its documented
// default (Design §17). Registry, Style and Turns are left nil/zero — the
// caller always supplies those.
func DefaultConfig() Config {
	return Config{
		Tier0Enabled: true,
		MainEnabled:  true,
		AckAfter:     DefaultAckAfter,
		Clock:        realClock{},
		SenderWindow: 180 * 24 * time.Hour,
		SenderLimit:  500,
		Breaker:      DefaultBreakerConfig(),
		MissWindow:   DefaultMissWindow,
		Retention:    90 * 24 * time.Hour,
	}
}

// Nervous is the front door: every channel calls Handle, which tries Tier 0
// first and only then hands off to the head chef (the main path, over
// runtime.ModelTurn). Exactly one owner ever answers a turn (internal/nervous/turn
// enforces this).
type Nervous struct {
	cfg   Config
	turns *turn.Table

	ring         *reflexRing
	tier0Breaker *Breaker
	toolTracer   *ToolTracer

	pruneMu      sync.Mutex
	lastPruneDay string
}

// New builds a Nervous. Registry and Style are required; everything else
// falls back to a sensible default (Turns to a fresh table, Clock to the
// real one, AckAfter to DefaultAckAfter if zero).
func New(cfg Config) (*Nervous, error) {
	if cfg.Registry == nil {
		return nil, errors.New("nervous: Config.Registry is required")
	}
	if cfg.Style == nil {
		return nil, errors.New("nervous: Config.Style is required")
	}
	if cfg.Turns == nil {
		cfg.Turns = turn.NewTable(time.Now)
	}
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.AckAfter <= 0 {
		cfg.AckAfter = DefaultAckAfter
	}
	if cfg.SenderLimit <= 0 {
		cfg.SenderLimit = 500
	}
	if cfg.Breaker == (BreakerConfig{}) {
		cfg.Breaker = DefaultBreakerConfig()
	}
	if cfg.MissWindow <= 0 {
		cfg.MissWindow = DefaultMissWindow
	}
	n := &Nervous{
		cfg:        cfg,
		turns:      cfg.Turns,
		ring:       newReflexRing(),
		toolTracer: NewToolTracer(),
	}
	n.tier0Breaker = NewBreaker(cfg.Breaker, cfg.Clock)
	return n, nil
}

// Tier0Breaker exposes Tier 0's breaker state for a later task's health
// endpoint (Design §11.2's RouterHealth). Not used by anything in this
// task's own tests beyond confirming it's reachable.
func (n *Nervous) Tier0Breaker() (BreakerState, string, time.Time) {
	return n.tier0Breaker.State()
}

// ToolTracer exposes the tool-call attribution tracer so a later task
// (R-16's quick-tool call site, R-15's main-path tool bridge) can record
// uses without this package needing to change again.
func (n *Nervous) ToolTracer() *ToolTracer {
	return n.toolTracer
}

func now(env runtime.Env) time.Time {
	if env.Now != nil {
		return env.Now()
	}
	return time.Now()
}

// entities builds the person list Validate/slots.Resolve needs for a
// person-typed slot, from recent message senders. A read error yields an
// empty list (an unresolved person slot escalates, which is the safe
// direction — it never treats a store error as "no such person exists").
func (n *Nervous) entities(ctx context.Context, env runtime.Env, at time.Time) slots.Entities {
	if env.Store == nil {
		return slots.Entities{}
	}
	people, err := env.Store.Senders(ctx, at.Add(-n.cfg.SenderWindow), n.cfg.SenderLimit)
	if err != nil {
		return slots.Entities{}
	}
	return slots.Entities{People: people}
}

func intentSummaries(reg *intents.Registry) []reflex.IntentSummary {
	cand := reg.Candidates()
	out := make([]reflex.IntentSummary, 0, len(cand))
	for _, it := range cand {
		out = append(out, reflex.IntentSummary{ID: it.ID, Description: it.Description})
	}
	return out
}

func (n *Nervous) deps(env runtime.Env, reg *intents.Registry, taskID string, at time.Time) reflex.Deps {
	return reflex.Deps{
		Store:     reflex.NewStoreView(env.Store),
		Approvals: env.Approvals,
		Brief: func(ctx context.Context, day string) (string, bool, bool, error) {
			return runtime.CachedBrief(ctx, env, day)
		},
		Tasks:    n.cfg.Tasks,
		Manifest: env.Manifest,
		Registry: intentSummaries(reg),
		Now:      func() time.Time { return at },
		TaskID:   taskID,
	}
}

// pendingCount reads the approval queue once per turn, for the
// requires_pending_approval eligibility check (Design §11.4 step 3ii). A
// read error is treated as "nothing pending" — the conservative direction
// for admitting approvals.respond as a candidate is to require a positive
// count, not assume one.
func pendingCount(ctx context.Context, env runtime.Env) int {
	if env.Approvals == nil {
		return 0
	}
	pend, err := env.Approvals.Pending(ctx)
	if err != nil {
		return 0
	}
	return len(pend)
}

// Handle is the one entry point every channel calls (Design §11.4). It
// never returns an error: every failure becomes an EventError so the
// caller's stream always ends with either "done" or "error". Exactly one
// route_log row is written per call, from rec.finish, deferred first so it
// runs last — after Done() has settled the turn's final state, and even if
// this frame later panics (the recover directly below re-panics once the
// row is written, rather than swallowing the bug).
func (n *Nervous) Handle(ctx context.Context, env runtime.Env, t Turn, emit func(runtime.Event)) {
	id := t.TaskID
	at := now(env)
	rec := n.newRouteRecorder(id, t.Channel, t.Text, at)
	defer rec.finish(ctx)
	defer func() {
		if p := recover(); p != nil {
			rec.outcome = "error"
			rec.escalationReason = "panic"
			panic(p)
		}
	}()

	emitRouter := n.turns.Emitter(id, turn.OwnerRouter, emit)

	if _, err := n.turns.Final("", id, t.Channel); err != nil {
		// A duplicate or otherwise invalid task id: nothing this package
		// can recover from sensibly, and the caller (the daemon, later)
		// is responsible for task-id uniqueness. Answer conservatively.
		rec.outcome = "error"
		rec.escalationReason = "duplicate_task_id"
		emit(runtime.Event{Kind: runtime.EventAck})
		emit(runtime.Event{Kind: runtime.EventError, Error: "nervous: " + err.Error()})
		return
	}
	emitRouter(runtime.Event{Kind: runtime.EventAck})

	var ackOnce sync.Once
	fireAck := func() {
		ackOnce.Do(func() {
			rec.recordAck(n.cfg.Clock.Now())
			n.emitHandoff(t.Channel, emitRouter)
		})
	}
	ackTimer := n.cfg.Clock.AfterFunc(n.cfg.AckAfter, fireAck)
	defer ackTimer.Stop()

	defer func() {
		// Best-effort: a turn that answered from Tier 0 already called Done
		// itself; this is a safety net for any path that returns early
		// without doing so (e.g. the eligibility/Tier-0 error branches
		// below reach the main path instead, which calls Done itself too —
		// this recovers only a genuinely missed case, and Table.Done on an
		// already-terminal turn is a harmless no-op error).
		_ = n.turns.Done(id, doneOrCancelled(ctx))
	}()

	reg := n.cfg.Registry()
	u := tmpl.Normalize(t.Text, wordSet(reg.Shared().SkipWords))
	pending := pendingCount(ctx, env)
	deps := n.deps(env, reg, id, at)
	ents := n.entities(ctx, env, at)

	eligible, elReason := Eligible(u, reg.Shared())

	var result *render.Result
	escReason := elReason
	var briefCacheMiss bool

	if eligible && n.cfg.Tier0Enabled && n.tier0Breaker.Allow() {
		rec.beginTier("t0")
		var err error
		result, escReason, err = TryTier0(ctx, reg, deps, u, pending, at, ents)
		rec.endTier("t0")
		if err != nil {
			if errors.Is(err, reflex.ErrBriefCacheMiss) {
				// The one deliberate exception to "the quick tiers never
				// call the model": Tier 0 only ever serves an ALREADY
				// cached brief. A miss isn't a normal escalation to a
				// conversational answer — the main path must compute and
				// cache one instead (Design §11.4 step 7 / Risk 7).
				briefCacheMiss = true
				escReason = "brief_cache_miss"
				n.tier0Breaker.RecordSuccess() // a cache miss is not a Tier 0 malfunction
			} else {
				escReason = "handler_error"
				n.tier0Breaker.RecordFailure(escReason)
			}
		} else if result != nil {
			n.tier0Breaker.RecordSuccess()
		}
		// A clean "escalate, no match" outcome (no_match/ambiguous_match/
		// slot_unresolved/action_word) is not itself a Tier 0 failure —
		// only an actual handler error counts against the breaker.
	} else if eligible && n.cfg.Tier0Enabled {
		escReason = "breaker_open"
	}

	rec.escalationReason = escReason

	if result != nil {
		n.answerQuick(id, t, *result, env, emit, rec)
		return
	}

	n.answerMain(ctx, id, t, env, emit, fireAck, ackTimer, briefCacheMiss, rec)
}

// answerQuick delivers a Tier 0 (or, once a later task adds it, Tier 1)
// answer: it owns the turn from here, renders through the twin's one style,
// escalates taint, and surfaces an approval request if the intent staged
// one (a write intent, wired by a later task — Tier 0 as built in R-12
// never sets ApprovalID itself, but the delivery path already handles it so
// that task needs no change here).
func (n *Nervous) answerQuick(id string, t Turn, result render.Result, env runtime.Env, emit func(runtime.Event), rec *routeRecorder) {
	rec.owner = "quick"
	rec.answeredBy = "t0" // Tier 1 (R-18) will pass its own tier id once it exists
	rec.intent = result.Intent
	// Tier 0 as built (R-11's TryTier0) only ever runs a read intent's
	// handler — write intents route through a separate proposal path a
	// later task builds (Design §12) — so every quick answer reaching here
	// today is intent_kind "read". IntentOrigin ("embedded" vs "learned")
	// has no meaning yet either: the promotion loop that creates learned
	// intents doesn't exist until R-22/23.
	rec.intentKind = "read"
	rec.outcome = quickOutcome(result)

	if err := n.turns.Route(id, turn.OwnerQuick); err != nil {
		rec.outcome = "error"
		emit(runtime.Event{Kind: runtime.EventError, Error: "nervous: " + err.Error()})
		return
	}
	emitQuick := n.turns.Emitter(id, turn.OwnerQuick, emit)

	text := n.cfg.Style.Render(result, string(t.Channel))
	if t.Channel == runtime.ChannelVoice {
		// The one-voice contract (Design §8.4): a quick answer's rendered
		// text is capped and normalized exactly like the main path's, using
		// the same style-declared voice caps, before it ever reaches the
		// sentence splitter.
		text = speak.Speakable(text, speak.Options{
			MaxListItems: n.cfg.Style.MaxListItems(string(runtime.ChannelVoice)),
			MaxChars:     n.cfg.Style.MaxChars(string(runtime.ChannelVoice)),
		})
	}
	runtime.DeliverText(t.Channel, text, emitQuick)
	if env.OnTaint != nil {
		env.OnTaint(result.Tainted)
	}
	if result.ApprovalID != "" {
		emitQuick(runtime.Event{Kind: runtime.EventApprovalRequired, ApprovalID: result.ApprovalID})
	}
	emitQuick(runtime.Event{Kind: runtime.EventDone, Text: text})
	_ = n.turns.Done(id, turn.StateDone)
}

// quickOutcome maps a quick answer's render.Result.Kind to the route_log
// outcome enum (Design §14). "decision" is bindPendingHandler's shape when
// exactly one approval is pending (Design §5.2/reflex.go): it has surfaced
// that envelope for a later binding decision, not decided anything itself
// yet (deciding is R-21's job), so it's recorded as "proposed" rather than
// "decided" — the closest existing enum value to "something is now staged
// for approval," not a claim that anything was approved.
func quickOutcome(result render.Result) string {
	switch result.Kind {
	case "clarify":
		return "clarified"
	case "decision":
		return "proposed"
	default:
		return "answered"
	}
}

func doneOrCancelled(ctx context.Context) turn.State {
	if ctx.Err() != nil {
		return turn.StateCancelled
	}
	return turn.StateDone
}

// emitHandoff sends a short handoff acknowledgement as the router. On the
// voice channel it's a sentence event (so it's actually spoken); on other
// channels runtime.EventKind has no dedicated "handoff" kind yet (that's a
// later task's client-facing addition, Design §11.4 step 6) so this uses a
// zero-text ack-shaped delta a client can already render as a typing/status
// indicator without breaking on an unknown event kind.
func (n *Nervous) emitHandoff(ch runtime.Channel, emit func(runtime.Event)) {
	phrase := "One moment."
	if v := n.cfg.Style.Voice(); len(v.Handoff) > 0 {
		phrase = v.Handoff[0]
	}
	if ch == runtime.ChannelVoice {
		emit(runtime.Event{Kind: runtime.EventSentence, Text: phrase})
		return
	}
	emit(runtime.Event{Kind: runtime.EventAck})
}
