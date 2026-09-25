package nervous

import (
	"context"
	"fmt"
	"time"

	"water/internal/approvals"
	"water/internal/nervous/intents"
	"water/internal/nervous/propose"
	"water/internal/nervous/reflex"
	"water/internal/nervous/render"
	"water/internal/nervous/speak"
	"water/internal/nervous/turn"
	"water/internal/runtime"
)

// Approver lets a voice yes/no directly decide the one pending envelope a
// read-back is bound to (Design §13, router.voice_approve.enabled), through
// the exact same hash-checked decide-and-execute path POST
// /v1/approvals/{id}/decision uses, without this package ever importing
// internal/gate or internal/approvals.Queue's Decide (Design §2's
// dependency-direction rule: nervous never decides directly). The gateway's
// *Daemon implements it, over its own decideAndExecute.
type Approver interface {
	DecideBound(ctx context.Context, id, payloadHash, reply string) (DecisionOutcome, error)
}

// DecisionOutcome is DecideBound's result: enough for a voice turn's own
// rendering, deliberately narrower than the HTTP API's DecisionResult (no
// envelope, no raw output) since a spoken confirmation never reads either
// back.
type DecisionOutcome struct {
	Status   string
	Executed bool
	Error    string
}

// ActionSink lets a write intent's built proposal become a pending approval
// envelope, through the exact same code path a model-initiated tool call
// uses (internal/gateway/actions.go's proposeEnvelope), without this
// package ever importing internal/gate or internal/connectors (Design §2's
// dependency-direction rule: nervous never imports gate or connectors --
// the gateway supplies this as a callback). internal/gateway's *Daemon
// implements it.
type ActionSink interface {
	ProposeAction(ctx context.Context, fn string, payload map[string]any, ch runtime.Channel) (approvals.Envelope, error)
}

// writeHandler builds a matched write intent's answer: either a queued
// approval (level A) or a level-D draft delivered directly. Tier 0 and
// Tier 1 both take one (tier0.go's runTier0Match, tier1.go's TryTier1) so
// every existing caller that never matches a write intent -- which is every
// test written before this task, and any Config with no ActionSink -- can
// pass nil safely: it is only ever invoked after the matched candidate's
// Kind is confirmed to be write.
type writeHandler func(ctx context.Context, it intents.Intent, args reflex.Args) (*render.Result, string, error)

// tryWriteIntent builds a matched write intent's proposal and turns it into
// a render.Result exactly like a read intent's handler would (Design §12):
//
//   - Unresolved/Ambiguous: the proposer could not pin an entity down (an
//     event to move, a message to reply to) safely enough to propose
//     anything -- the sous chef never guesses. The caller escalates
//     ("entity_unresolved"/"entity_ambiguous") to the main path exactly
//     like any other quick-tier miss.
//   - RequiresApproval (level A): the proposal is queued through
//     cfg.Actions.ProposeAction -- the same envelope-creation path a
//     model-initiated tool call uses -- and the result carries the
//     read-back plus ApprovalID, so the facade emits approval_required
//     alongside it (Handle's existing answerQuick already does this for
//     any Result with a non-empty ApprovalID). Nothing executes: that only
//     ever happens through a later approved decision (R-21).
//   - Not RequiresApproval (level D, "nothing leaves"): there is no
//     envelope and no gate call at all. The draft IS the answer, delivered
//     directly (docs/slices/R.md Risk item 24, resolved by this task).
func (n *Nervous) tryWriteIntent(ctx context.Context, env runtime.Env, it intents.Intent, args reflex.Args, ch runtime.Channel, at time.Time) (*render.Result, string, error) {
	prop, ok := propose.Table()[it.Proposer]
	if !ok {
		// LoadRegistry already checks it.Proposer is in propose.Specs() at
		// load time (Design §5.3); reaching here would mean the table and
		// the registry have drifted apart.
		return nil, "no_match", nil
	}

	pdeps := propose.Deps{Store: n.proposeStore(env), Now: func() time.Time { return at }}
	p, outcome, err := prop.Build(ctx, pdeps, args)
	if err != nil {
		return nil, "", err
	}
	switch outcome {
	case propose.Unresolved:
		return nil, "entity_unresolved", nil
	case propose.Ambiguous:
		return nil, "entity_ambiguous", nil
	}

	if !it.RequiresApproval {
		return &render.Result{
			Intent:         it.ID,
			Kind:           "read",
			Interpretation: p.Summary,
			Text:           p.Summary,
			Tainted:        p.Tainted,
		}, "", nil
	}

	if n.cfg.Actions == nil {
		return nil, "", fmt.Errorf("nervous: %s needs approval but no ActionSink is configured", it.ID)
	}
	envelope, err := n.cfg.Actions.ProposeAction(ctx, it.Action, p.Payload, ch)
	if err != nil {
		return nil, "", err
	}
	if ch == runtime.ChannelVoice {
		// This is where the read-back is actually emitted on a voice turn's
		// stream (answerQuick's own ApprovalID check below, delivering
		// approvals.ReadBack(envelope) as this Result's Text): record it so
		// a later bare yes/no on this channel can bind to it (Design §13).
		n.readbacks.Record(Readback{Channel: ch, EnvelopeID: envelope.ID, PayloadHash: envelope.PayloadHash, At: at})
	}
	return &render.Result{
		Intent:         it.ID,
		Kind:           "decision",
		Interpretation: p.Summary,
		Text:           approvals.ReadBack(envelope),
		ApprovalID:     envelope.ID,
		Tainted:        p.Tainted,
	}, "", nil
}

// proposeStore is the read-only store a proposer reads through: the same
// ReadStore-falling-back-to-env.Store choice n.deps and n.entities make.
// reflex.NewStoreView's return type (reflex.StoreView) is assignable to
// propose.StoreView directly -- their method sets overlap exactly on
// EventsInRange/MessagesFrom -- so no adapter type is needed here.
func (n *Nervous) proposeStore(env runtime.Env) propose.StoreView {
	st := n.cfg.ReadStore
	if st == nil {
		st = env.Store
	}
	return reflex.NewStoreView(st)
}

// voiceApproveActive reports whether a matched result should be handled by
// the voice approval binding (answerVoiceApprove) instead of the ordinary
// bind-and-surface delivery (answerQuick): the flag is on, an Approver is
// wired (a flag on with no Approver falls back to the safe, pre-R-21
// direction rather than panicking on a nil interface call), the channel is
// voice, and the matched intent is approvals.respond. Any other combination
// -- including the flag off -- leaves bindPendingHandler's result delivered
// exactly as it was before this task existed.
func (n *Nervous) voiceApproveActive(ch runtime.Channel, result render.Result) bool {
	return n.cfg.VoiceApprove.Enabled && n.cfg.Approver != nil && ch == runtime.ChannelVoice && result.Intent == "approvals.respond"
}

// voiceErrorPhrase looks up one of style.yaml's required voice.errors
// phrases, falling back to the bare key if it is somehow missing (it never
// is for a Style that passed validateStyleFile, but this keeps a bad style
// file from ever silently killing the turn instead of the render.Style
// package's own load-time check).
func (n *Nervous) voiceErrorPhrase(key string) string {
	if errs := n.cfg.Style.Voice().Errors; errs != nil {
		if s := errs[key]; s != "" {
			return s
		}
	}
	return key
}

// answerVoiceApprove is approvals.respond's voice-channel decision
// procedure when router.voice_approve.enabled is on (Design §13). It is
// only ever reached when voiceApproveActive returned true, so it owns the
// turn exactly like answerQuick does; it never falls back to answerQuick's
// own delivery, since every branch below both renders and finishes the turn.
//
// result is bindPendingHandler's own Result, unmodified: Kind "clarify" for
// 0 or 2+ pending (no binding is ever consulted, matching Design §13 step 2
// exactly), Kind "decision" with ApprovalID set for exactly 1 pending.
func (n *Nervous) answerVoiceApprove(ctx context.Context, id string, t Turn, result render.Result, env runtime.Env, emit func(runtime.Event), rec *routeRecorder, tier string, at time.Time) {
	rec.owner = "quick"
	rec.answeredBy = tier
	rec.intent = result.Intent
	rec.intentKind = "read"

	if err := n.turns.Route(id, turn.OwnerQuick); err != nil {
		rec.outcome = "error"
		emit(runtime.Event{Kind: runtime.EventError, Error: "nervous: " + err.Error()})
		return
	}
	emitQuick := n.turns.Emitter(id, turn.OwnerQuick, emit)

	deliver := func(outcome, text string) {
		rec.outcome = outcome
		text = speak.Speakable(text, speak.Options{
			MaxListItems: n.cfg.Style.MaxListItems(string(runtime.ChannelVoice)),
			MaxChars:     n.cfg.Style.MaxChars(string(runtime.ChannelVoice)),
		})
		runtime.DeliverText(t.Channel, text, emitQuick)
		emitQuick(runtime.Event{Kind: runtime.EventDone, Text: text})
		_ = n.turns.Done(id, turn.StateDone)
	}

	if result.Kind != "decision" {
		// 0 pending ("clarify", Text already "Nothing is waiting for
		// approval.") or 2+ pending ("clarify" with a menu): bindPendingHandler
		// already built the right words, and Design §13 step 2 says there is
		// never a voice decision here, so no binding is even consulted.
		deliver(quickOutcome(result), n.cfg.Style.Render(result, string(runtime.ChannelVoice)))
		return
	}

	// Exactly one pending, per bindPendingHandler: re-read it rather than
	// trust result.ApprovalID alone, so a race between bindPendingHandler's
	// own read and this turn (another decider, an edit) is caught here too.
	pend, err := env.Approvals.Pending(ctx)
	if err != nil || len(pend) != 1 || pend[0].ID != result.ApprovalID {
		deliver("error", n.voiceErrorPhrase("generic"))
		return
	}
	envel := pend[0]

	resurface := func() {
		n.readbacks.Record(Readback{Channel: t.Channel, EnvelopeID: envel.ID, PayloadHash: envel.PayloadHash, At: at})
		deliver("proposed", n.voiceErrorPhrase("readback_stale")+" "+approvals.ReadBack(envel))
	}

	bound, ok := n.readbacks.Bound(t.Channel, at, n.cfg.VoiceApprove.Window)
	if !ok || bound.EnvelopeID != envel.ID || bound.PayloadHash != envel.PayloadHash {
		// No binding, an expired one, or one that no longer names the
		// current envelope (a decision or an Edit raced in): stale. Void
		// whatever is there, re-surface the current read-back, and decide
		// nothing on this turn (Design §13 step 3).
		n.readbacks.Void(envel.ID)
		resurface()
		return
	}

	answer := approvals.Match(t.Text)
	if answer != approvals.Yes {
		// A spoken "no" (Ambiguous is always treated as No, exactly like
		// every other approvals path in this codebase) is safe to apply at
		// every risk tier: denying is never the wrong direction.
		n.readbacks.Void(envel.ID)
		out, derr := n.cfg.Approver.DecideBound(ctx, envel.ID, envel.PayloadHash, "no")
		if derr != nil {
			deliver("error", n.voiceErrorPhrase("generic"))
			return
		}
		text := "Denied."
		if out.Error != "" {
			text = n.voiceErrorPhrase("generic")
		}
		deliver("decided", text)
		return
	}

	if vtier, _ := VoiceApprovalTier(envel, n.cfg.VoiceApprove.InternalDomains); vtier == TapRequired {
		// A spoken "yes" never decides this envelope: re-emit
		// approval_required so a client shows its tap affordance, and leave
		// the binding exactly as it was (nothing about the envelope's own
		// risk tier will change before it is decided some other way).
		emitQuick(runtime.Event{Kind: runtime.EventApprovalRequired, ApprovalID: envel.ID})
		deliver("tap_required", n.voiceErrorPhrase("tap_required"))
		return
	}

	n.readbacks.Void(envel.ID)
	out, derr := n.cfg.Approver.DecideBound(ctx, envel.ID, envel.PayloadHash, "yes")
	if derr != nil {
		deliver("error", n.voiceErrorPhrase("generic"))
		return
	}
	text := n.cfg.Style.Confirmation()
	if out.Error != "" {
		text = n.voiceErrorPhrase("generic")
	}
	deliver("decided", text)
}
