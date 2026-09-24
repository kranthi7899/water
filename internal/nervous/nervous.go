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
	"water/internal/nervous/tmpl"
	"water/internal/nervous/turn"
	"water/internal/runtime"
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
	}
}

// Nervous is the front door: every channel calls Handle, which tries Tier 0
// first and only then hands off to the head chef (the main path, over
// runtime.ModelTurn). Exactly one owner ever answers a turn (internal/nervous/turn
// enforces this).
type Nervous struct {
	cfg   Config
	turns *turn.Table
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
	return &Nervous{cfg: cfg, turns: cfg.Turns}, nil
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
// caller's stream always ends with either "done" or "error".
func (n *Nervous) Handle(ctx context.Context, env runtime.Env, t Turn, emit func(runtime.Event)) {
	id := t.TaskID
	emitRouter := n.turns.Emitter(id, turn.OwnerRouter, emit)

	if _, err := n.turns.Final("", id, t.Channel); err != nil {
		// A duplicate or otherwise invalid task id: nothing this package
		// can recover from sensibly, and the caller (the daemon, later)
		// is responsible for task-id uniqueness. Answer conservatively.
		emit(runtime.Event{Kind: runtime.EventAck})
		emit(runtime.Event{Kind: runtime.EventError, Error: "nervous: " + err.Error()})
		return
	}
	emitRouter(runtime.Event{Kind: runtime.EventAck})

	var ackOnce sync.Once
	fireAck := func() { ackOnce.Do(func() { n.emitHandoff(t.Channel, emitRouter) }) }
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

	at := now(env)
	reg := n.cfg.Registry()
	u := tmpl.Normalize(t.Text, wordSet(reg.Shared().SkipWords))
	pending := pendingCount(ctx, env)
	deps := n.deps(env, reg, id, at)
	ents := n.entities(ctx, env, at)

	eligible, elReason := Eligible(u, reg.Shared())

	var result *render.Result
	escReason := elReason
	var briefCacheMiss bool

	if eligible && n.cfg.Tier0Enabled {
		var err error
		result, escReason, err = TryTier0(ctx, reg, deps, u, pending, at, ents)
		if err != nil {
			if errors.Is(err, reflex.ErrBriefCacheMiss) {
				// The one deliberate exception to "the quick tiers never
				// call the model": Tier 0 only ever serves an ALREADY
				// cached brief. A miss isn't a normal escalation to a
				// conversational answer — the main path must compute and
				// cache one instead (Design §11.4 step 7 / Risk 7).
				briefCacheMiss = true
				escReason = "brief_cache_miss"
			} else {
				escReason = "handler_error"
			}
		}
	}

	if result != nil {
		n.answerQuick(id, t, *result, env, emit)
		return
	}

	n.answerMain(ctx, id, t, env, emit, fireAck, ackTimer, briefCacheMiss)
	_ = escReason // recorded by a later task's route log (R-14); nothing to do with it yet in R-12
}

// answerQuick delivers a Tier 0 (or, once a later task adds it, Tier 1)
// answer: it owns the turn from here, renders through the twin's one style,
// escalates taint, and surfaces an approval request if the intent staged
// one (a write intent, wired by a later task — Tier 0 as built in R-12
// never sets ApprovalID itself, but the delivery path already handles it so
// that task needs no change here).
func (n *Nervous) answerQuick(id string, t Turn, result render.Result, env runtime.Env, emit func(runtime.Event)) {
	if err := n.turns.Route(id, turn.OwnerQuick); err != nil {
		emit(runtime.Event{Kind: runtime.EventError, Error: "nervous: " + err.Error()})
		return
	}
	emitQuick := n.turns.Emitter(id, turn.OwnerQuick, emit)

	text := n.cfg.Style.Render(result, string(t.Channel))
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
