package nervous

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"water/internal/nervous/intents"
	"water/internal/nervous/reflex"
	"water/internal/nervous/render"
	"water/internal/nervous/slots"
	"water/internal/nervous/speak"
	"water/internal/nervous/t1"
	"water/internal/nervous/tmpl"
	"water/internal/nervous/turn"
	"water/internal/runtime"
	"water/internal/store"
	"water/internal/tools"
	"water/internal/twins"
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
	// ClientID is the optional turn_id a client streamed partial
	// transcripts under (POST /v1/turns/{id}/partial) before posting this
	// final turn. Empty means no partials preceded this turn — the existing
	// behavior, unchanged. When set, Handle takes over that turn's
	// bookkeeping (turn.Table.Final's clientID parameter) so any cached
	// speculative work reaches this turn (R-19).
	ClientID string
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

	// Manifest and Approvals let Quick()'s handlers (status.overview,
	// approvals.pending) reach the twin's manifest and approval queue
	// outside of any particular turn's runtime.Env — a quick.* tool call,
	// unlike Handle, has no turn of its own to carry one.
	Manifest  *twins.Manifest
	Approvals reflex.PendingLister

	// OnVoiceLint receives the main path's speak.Lint warnings for a voice
	// turn, if any. R-14 both reads these into the route_log row AND still
	// calls this hook if set, so a later task (e.g. a live status view) can
	// observe them without going through the store.
	OnVoiceLint func(warnings []string)

	Tier0Enabled bool
	MainEnabled  bool

	// Tier1Enabled and T1 gate Tier 1 (R-18): both must be set (T1 non-nil)
	// for Handle to ever attempt it. T1 is nil in every test and daemon
	// build until something has actually passed the live eval gate and
	// built a real sidecar client — there is no default here, unlike
	// Tier0Enabled/MainEnabled, because a present-but-untrusted T1 client
	// must never be attempted implicitly.
	Tier1Enabled bool
	T1           t1.Client

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
	// ReadStore is the read-only pool reflex handlers query through (Design
	// §1(e)'s defense in depth: opened with store.OpenReadOnly, so a write
	// attempt fails at the SQLite level). Nil falls back to Store, which
	// keeps every pre-R-15 test (none of which set this) working unchanged.
	ReadStore *store.Store
	// Breaker configures Tier 0's circuit breaker (Design §11.4/§17).
	Breaker BreakerConfig
	// MissWindow bounds the possible-miss detection window (default 60s)
	// and doubles as the lookback ListRoutes uses to find "the previous row
	// on this channel".
	MissWindow time.Duration
	// Retention bounds how long route_log rows are kept before
	// pruneRoutesOncePerDay deletes them (default 90 days).
	Retention time.Duration

	// Speculation gates R-19's partial-transcript prefetch (Design §11.5,
	// flag router.speculation.enabled, code default on). Off makes Partial
	// a pure turn.Table bookkeeping call: it still tracks partial
	// counts/timing for the route_log row, it just never runs speculate().
	Speculation bool
	// Prewarm tries to warm the backend's subprocess ahead of a real turn,
	// without ever sending it one (internal/backend.WarmSession.Prewarm,
	// wrapped by the daemon's own wiring so it always warms with the exact
	// request — system prompt, model, tool policy — a real main-path turn
	// would use). Nil is accepted: speculation then always reports "skipped"
	// for the intents that would otherwise have prewarmed.
	Prewarm func(ctx context.Context) (state string, err error)

	// Actions lets a matched write intent's proposal reach the approval
	// queue (Design §12). Nil is accepted: a write intent whose action
	// needs approval then fails with a clear error instead of silently
	// doing nothing, but a twin with no write intents (or one where none
	// have been granted yet) needs no Actions at all.
	Actions ActionSink

	// Approver lets a bound voice yes/no directly decide a pending envelope
	// (Design §13, R-21). Nil is accepted: VoiceApprove.Enabled then has no
	// effect (voiceApproveActive requires both), which is the safe fallback.
	Approver Approver
	// VoiceApprove gates and configures the voice channel's yes/no binding.
	// Code default off (router.voice_approve.enabled); Approver must also
	// be set for Enabled to take effect.
	VoiceApprove VoiceApproveConfig

	// Promotion configures the learned-intent growth loop's automatic
	// demotion thresholds (Design §16 item 5, R-23). It has no effect on
	// whether the learned overlay is ever loaded in the first place (that
	// is router.promotion.enabled, applied where the registry is built,
	// outside this package) — a twin with no learned intents active simply
	// never has an IntentOrigin == "learned" row to demote.
	Promotion PromotionConfig
	// Logf receives one line per background error this package cannot
	// surface to any turn (currently: automatic-demotion bookkeeping
	// failures only, since that work runs after the turn it was triggered
	// by has already finished). Nil is replaced with a no-op in New.
	Logf func(format string, args ...any)
}

// PromotionConfig configures the growth loop's automatic-demotion
// thresholds (Design §16 item 5 / §17: router.promotion.demote_min_samples,
// router.promotion.demote_miss_rate_pct).
type PromotionConfig struct {
	// DemoteMinSamples and DemoteMissRatePct feed
	// LearnedIntentShouldDemote (breaker.go) directly; <= 0 falls back to
	// DefaultDemoteMinSamples/DefaultDemoteMissRatePct.
	DemoteMinSamples  int
	DemoteMissRatePct int
}

// DefaultDemoteMinSamples and DefaultDemoteMissRatePct are Design §17's
// documented defaults for the automatic-demotion breaker.
const (
	DefaultDemoteMinSamples  = 10
	DefaultDemoteMissRatePct = 20
)

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
		Speculation:  true,
		VoiceApprove: VoiceApproveConfig{Window: DefaultVoiceApproveWindow},
	}
}

// Nervous is the front door: every channel calls Handle, which tries Tier 0
// first and only then hands off to the head chef (the main path, over
// runtime.ModelTurn). Exactly one owner ever answers a turn (internal/nervous/turn
// enforces this).
type Nervous struct {
	cfg   Config
	turns *turn.Table

	// registryPtr is the live intent registry: an atomic.Pointer so a
	// concurrent Handle always observes either the pre- or post-Reload
	// registry in full, never a torn read. Seeded from Config.Registry() at
	// construction; Reload (R-23: POST /v1/intents/reload, and the
	// automatic-demotion hook in routelog.go) swaps it afterward.
	registryPtr atomic.Pointer[intents.Registry]

	ring         *reflexRing
	tier0Breaker *Breaker
	tier1Breaker *Breaker
	toolTracer   *ToolTracer
	readbacks    *Readbacks

	quickOnce sync.Once
	quick     *reflex.QuickService

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
	if cfg.VoiceApprove.Window <= 0 {
		cfg.VoiceApprove.Window = DefaultVoiceApproveWindow
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	n := &Nervous{
		cfg:        cfg,
		turns:      cfg.Turns,
		ring:       newReflexRing(),
		toolTracer: NewToolTracer(),
		readbacks:  NewReadbacks(),
	}
	n.registryPtr.Store(cfg.Registry())
	n.tier0Breaker = NewBreaker(cfg.Breaker, cfg.Clock)
	// Tier 1 gets its own breaker instance (same config shape, independent
	// state): a run of Tier 1 handler errors must not also silence Tier 0,
	// and vice versa.
	n.tier1Breaker = NewBreaker(cfg.Breaker, cfg.Clock)
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

// RecordToolUse attributes one tool call (twin or quick.*) to whichever
// main-path turn(s) are currently in flight. A thin, named wrapper around
// ToolTracer().RecordUse so a gateway call site reads as "tell Nervous a
// tool was used," not "reach into its tracer" — the mechanism itself is
// R-14's.
func (n *Nervous) RecordToolUse(tool string) {
	n.toolTracer.RecordUse(tool)
}

// Quick lazily builds and returns the sous chef's quick.* tool service: the
// fixed subset of reflex.Table() an intent has explicitly opted into
// exposing to the main agent (FunctionSpec.QuickTool). Built once, since
// the underlying handler table is fixed for the process's life.
func (n *Nervous) Quick() *reflex.QuickService {
	n.quickOnce.Do(func() {
		n.quick = reflex.NewQuickService(func() reflex.Deps {
			st := n.cfg.ReadStore
			if st == nil {
				st = n.cfg.Store
			}
			return reflex.Deps{
				Store:     reflex.NewStoreView(st),
				Approvals: n.cfg.Approvals,
				Manifest:  n.cfg.Manifest,
				Now:       time.Now,
			}
		})
	})
	return n.quick
}

// QuickFunctions is Quick().Functions(), for TwinToolPolicy (R-15) to
// declare alongside the manifest's connector functions.
func (n *Nervous) QuickFunctions() []tools.QuickFunction {
	return n.Quick().Functions()
}

// registry returns the live intent registry: whatever Reload last set, or
// the one Config.Registry supplied at construction if Reload has never been
// called. Every internal call site in this package reads through here (not
// n.cfg.Registry() directly) so a reload actually takes effect immediately.
func (n *Nervous) registry() *intents.Registry {
	return n.registryPtr.Load()
}

// Reload atomically swaps in a freshly-built registry (POST
// /v1/intents/reload, and the automatic-demotion hook in routelog.go,
// R-23). A concurrent Handle always sees either the old or the new registry
// in full, never a partially-updated one. reg == nil is ignored (a caller
// mistake, not a valid "clear the registry" request — this package always
// requires a non-nil registry, from New onward).
func (n *Nervous) Reload(reg *intents.Registry) {
	if reg == nil {
		return
	}
	n.registryPtr.Store(reg)
}

// Registry exposes the current intent registry snapshot, for a health
// endpoint (GET /v1/router, R-15) to report inactive write intents and any
// skipped learned-overlay files.
func (n *Nervous) Registry() *intents.Registry {
	return n.registry()
}

// VoiceProfile exposes the loaded style's voice section, for GET
// /v1/voice/profile (R-15): every client's TTS settings come from here, so
// the twin's voice never differs by which tier answered.
func (n *Nervous) VoiceProfile() render.VoiceStyle {
	return n.cfg.Style.Voice()
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
	st := n.cfg.ReadStore
	if st == nil {
		st = env.Store
	}
	if st == nil {
		return slots.Entities{}
	}
	people, err := st.Senders(ctx, at.Add(-n.cfg.SenderWindow), n.cfg.SenderLimit)
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
	st := n.cfg.ReadStore
	if st == nil {
		st = env.Store
	}
	return reflex.Deps{
		Store:     reflex.NewStoreView(st),
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

	if _, err := n.turns.Final(t.ClientID, id, t.Channel); err != nil {
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

	reg := n.registry()
	u := tmpl.Normalize(t.Text, wordSet(reg.Shared().SkipWords))
	pending := pendingCount(ctx, env)
	deps := n.deps(env, reg, id, at)
	ents := n.entities(ctx, env, at)

	eligible, elReason := Eligible(u, reg.Shared())

	wh := func(wctx context.Context, it intents.Intent, args reflex.Args) (*render.Result, string, error) {
		return n.tryWriteIntent(wctx, env, it, args, t.Channel, at)
	}

	var result *render.Result
	escReason := elReason
	var briefCacheMiss bool
	answeredTier := ""

	if eligible && n.cfg.Tier0Enabled && n.tier0Breaker.Allow() {
		rec.beginTier("t0")
		var err error
		if reused, ok := n.trySpeculationReuse(id, reg, u, pending, at, ents); ok {
			// A cached speculative dry-match/read from this turn's own
			// partials still agrees with the FINAL match (Design §11.5):
			// reuse its already-rendered result instead of re-running the
			// handler. This is exactly a Tier 0 hit for every bookkeeping
			// purpose below (breaker, route log, ring) — only the handler
			// call itself was skipped.
			result, escReason = reused, ""
		} else {
			result, escReason, err = TryTier0(ctx, reg, deps, u, pending, at, ents, wh)
		}
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
			answeredTier = "t0"
		}
		// A clean "escalate, no match" outcome (no_match/ambiguous_match/
		// slot_unresolved/action_word) is not itself a Tier 0 failure —
		// only an actual handler error counts against the breaker.
	} else if eligible && n.cfg.Tier0Enabled {
		escReason = "breaker_open"
	}

	// Tier 1 (R-18) is tried only when Tier 0 itself came back with a clean
	// "no_match": never for an eligibility rejection (escalate_word,
	// multi_clause, too_long, empty), never for breaker_open, and never for
	// any of Tier 0's own other escalation reasons (action_word,
	// slot_unresolved, ambiguous_match, handler_error, brief_cache_miss) —
	// those all mean a quick tier already recognized something about the
	// utterance that Tier 1 guessing again would not safely resolve.
	if result == nil && escReason == "no_match" && n.cfg.Tier1Enabled && n.cfg.T1 != nil {
		if n.tier1Breaker.Allow() {
			rec.beginTier("t1")
			var err error
			result, escReason, err = TryTier1(ctx, reg, deps, n.cfg.T1, u, at, ents, wh)
			rec.endTier("t1")
			if err != nil {
				escReason = "handler_error"
				n.tier1Breaker.RecordFailure(escReason)
			} else if result != nil {
				n.tier1Breaker.RecordSuccess()
				answeredTier = "t1"
			}
			// A clean Tier 1 escalation (t1_no_call/t1_multi_call/t1_text/
			// t1_unknown_intent/t1_ungrounded/action_word/slot_unresolved/
			// ambiguous_match) is not itself a Tier 1 failure, exactly like
			// Tier 0's own clean escalations above.
		} else {
			escReason = "t1_breaker_open"
		}
	}

	rec.escalationReason = escReason

	if result != nil {
		if n.voiceApproveActive(t.Channel, *result) {
			n.answerVoiceApprove(ctx, id, t, *result, env, emit, rec, answeredTier, at)
			return
		}
		n.answerQuick(id, t, *result, env, emit, rec, answeredTier)
		return
	}

	n.answerMain(ctx, id, t, env, emit, fireAck, ackTimer, briefCacheMiss, rec)
}

// answerQuick delivers a Tier 0 or Tier 1 answer: it owns the turn from
// here, renders through the twin's one style, escalates taint, and
// surfaces an approval request if the intent staged one (a write intent,
// wired by this task — TryTier0/TryTier1 hand a matched write intent to
// tryWriteIntent, which sets ApprovalID for a level-A proposal; the
// delivery path below needed no change for that). tier is "t0" or "t1",
// whichever answered.
func (n *Nervous) answerQuick(id string, t Turn, result render.Result, env runtime.Env, emit func(runtime.Event), rec *routeRecorder, tier string) {
	rec.owner = "quick"
	rec.answeredBy = tier
	rec.intent = result.Intent
	rec.intentKind = "read"
	rec.intentOrigin = "embedded"
	if it, ok := n.registry().Lookup(result.Intent); ok {
		if it.Kind == intents.KindWrite {
			rec.intentKind = "write"
		}
		if it.Origin == "learned" {
			rec.intentOrigin = "learned"
		}
	}
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

// trySpeculationReuse reports whether a cached speculative read answer for
// id may stand in for a real Tier 0 run (Design §11.5): the cached
// Speculation must exist, have actually hit (DryMatch AND RunRead both
// succeeded), be no older than specReuseWindow, and — checked here against
// the exact same matchOnly Tier 0 itself uses, never the cached dry-match's
// own say-so — the FINAL utterance's winning match must name the same
// intent and the same resolved slot labels. Anything else is left alone: the
// caller falls through to a normal TryTier0 run, and the speculative work
// was simply wasted, which Design §11.5 accepts as fine.
func (n *Nervous) trySpeculationReuse(id string, reg *intents.Registry, u tmpl.Utterance, pending int, now time.Time, ents slots.Entities) (*render.Result, bool) {
	specAny, ok := n.turns.GetSpec(id)
	if !ok || specAny == nil {
		return nil, false
	}
	spec, ok := specAny.(*Speculation)
	if !ok || spec == nil || !spec.hit {
		return nil, false
	}
	if now.Sub(spec.computedAt) > specReuseWindow {
		return nil, false
	}
	top, _, ok := matchOnly(reg, u, pending, now, ents)
	if !ok || top.intent.ID != spec.intent || !sameLabels(top.labels, spec.labels) {
		return nil, false
	}
	spec.reused = true
	res := spec.result
	return &res, true
}

// specStore is the read-only store speculation reads through: ReadStore,
// falling back to Store, exactly like n.deps and n.entities do.
func (n *Nervous) specStore() *store.Store {
	if n.cfg.ReadStore != nil {
		return n.cfg.ReadStore
	}
	return n.cfg.Store
}

// pendingCountConfig is pendingCount's Config-only twin: Partial has no
// per-turn runtime.Env to read env.Approvals from (a partial transcript
// isn't a full turn), so it reads Config.Approvals directly instead. A nil
// Config.Approvals or a read error is treated as "nothing pending" — same
// conservative direction as pendingCount.
func (n *Nervous) pendingCountConfig(ctx context.Context) int {
	if n.cfg.Approvals == nil {
		return 0
	}
	pend, err := n.cfg.Approvals.Pending(ctx)
	if err != nil {
		return 0
	}
	return len(pend)
}

// specDeps builds the SpecDeps a real Partial call speculates against: pure
// store reads (Summary, and the reflex.Deps DryMatch/RunRead run their
// handler against) plus Config.Prewarm — never a backend or Tier 1 field,
// so this package's own speculate() can never reach either one no matter
// what reg/pending/now/ents are (Design §1's "enforced by construction").
func (n *Nervous) specDeps(reg *intents.Registry, pending int, now time.Time, ents slots.Entities) SpecDeps {
	rdeps := reflex.Deps{
		Store:     reflex.NewStoreView(n.specStore()),
		Approvals: n.cfg.Approvals,
		Manifest:  n.cfg.Manifest,
		Now:       func() time.Time { return now },
		// brief.today's cached-brief handler calls Deps.Brief directly with
		// no nil check (it is always built from a real runtime.Env in every
		// other caller); speculation has no runtime.Env of its own to build
		// CachedBrief from, so it supplies a safe stub that always reports
		// a cache miss. A dry-matched brief.today therefore always defers
		// its handler run to the main path exactly as an ordinary Tier 0
		// cache miss does (reflex.ErrBriefCacheMiss) — speculation never
		// computes or caches a brief itself.
		Brief: func(context.Context, string) (string, bool, bool, error) {
			return "", false, false, nil
		},
	}
	return SpecDeps{
		ReadStore: n.specStore(),
		Summary: func(ctx context.Context) (string, bool, error) {
			// runtime.Env.Approvals is a concrete *approvals.Queue, while
			// Config.Approvals is the narrower reflex.PendingLister this
			// package actually depends on; omitting it here only drops the
			// "pending approvals" line from a summary text nothing reads
			// (speculate() only cares whether this precompute succeeded at
			// all, for the route_log row's "summary" flag), not the
			// store-read behavior this step exists to warm.
			s, tainted := runtime.StateSummary(ctx, runtime.Env{Store: n.specStore(), Now: func() time.Time { return now }})
			return s, tainted, nil
		},
		DryMatch: func(u tmpl.Utterance) (string, map[string]string, bool) {
			top, _, ok := matchOnly(reg, u, pending, now, ents)
			if !ok {
				return "", nil, false
			}
			return top.intent.ID, top.labels, true
		},
		RunRead: func(ctx context.Context, intent string, labels map[string]string) (render.Result, error) {
			it, ok := candidateByID(reg, intent)
			if !ok {
				return render.Result{}, errors.New("nervous: speculate: unknown intent " + intent)
			}
			captures := make([]tmpl.Capture, 0, len(labels))
			for name, val := range labels {
				nt := tmpl.Normalize(val, nil)
				captures = append(captures, tmpl.Capture{Slot: name, Tokens: nt.Tokens, Raw: val})
			}
			v, _, ok := Validate(reg, Proposal{Intent: intent, Captures: captures}, now, ents)
			if !ok {
				return render.Result{}, errors.New("nervous: speculate: could not revalidate " + intent)
			}
			handler, ok := reflex.Table()[it.Function]
			if !ok {
				return render.Result{}, errors.New("nervous: speculate: no handler for " + it.Function)
			}
			return handler.Run(ctx, rdeps, v.Args)
		},
		Prewarm: n.cfg.Prewarm,
	}
}

// Partial records one partial transcript for clientID (turn.Table.Partial:
// creates a new listening turn, or extends one already listening — the
// partial text itself is passed to nothing but this call's own in-memory
// speculative work below, and is never stored or logged anywhere; see
// Design §11.5) and, when Config.Speculation is on, runs at most one
// debounced round of speculative prefetch: a state-summary read, a dry
// match against Tier 0's own registry and matching logic, and — only on a
// dry-match hit — the matched read handler, cached for later reuse by
// Handle. It never answers the turn and never emits an event; Handle is the
// only path that can do either.
func (n *Nervous) Partial(ctx context.Context, clientID string, ch runtime.Channel, text string, seq int) (turn.State, error) {
	tn, err := n.turns.Partial(clientID, ch, seq)
	if err != nil {
		return "", err
	}
	if !n.cfg.Speculation {
		return tn.State, nil
	}

	reg := n.registry()
	u := tmpl.Normalize(text, wordSet(reg.Shared().SkipWords))
	now := n.cfg.Clock.Now()

	var prev *Speculation
	if prevAny, ok := n.turns.GetSpec(clientID); ok {
		prev, _ = prevAny.(*Speculation)
	}
	if !shouldSpeculate(prev, u.Tokens, now) {
		return tn.State, nil
	}

	pending := n.pendingCountConfig(ctx)
	ents := n.entities(ctx, runtime.Env{}, now)
	spec := speculate(ctx, n.specDeps(reg, pending, now, ents), u)
	spec.computedAt = now
	n.turns.SetSpec(clientID, spec)
	return tn.State, nil
}
