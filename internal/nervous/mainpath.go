package nervous

import (
	"context"

	"water/internal/nervous/speak"
	"water/internal/nervous/turn"
	"water/internal/runtime"
)

// speakableFilter wraps emit so that, on the voice channel only, every
// EventSentence's text passes through speak.Speakable before delivery
// (Design §8.4: "every sentence event is passed through Speakable... no cap
// per sentence" — the reply itself is never truncated here, only Lint,
// called separately once the full reply is known, can flag it as
// overlength). Every other event kind (delta, done, error, ...) passes
// through unchanged, since deltas are for on-screen display, not speech.
func speakableFilter(ch runtime.Channel, emit func(runtime.Event)) func(runtime.Event) {
	if ch != runtime.ChannelVoice {
		return emit
	}
	return func(ev runtime.Event) {
		if ev.Kind == runtime.EventSentence {
			ev.Text = speak.Speakable(ev.Text, speak.Options{})
		}
		emit(ev)
	}
}

// answerMain hands the turn to the head chef: the existing warm-session
// model path (runtime.ModelTurn), or, on a brief cache miss, the one
// deliberate model call the quick tiers themselves never make (Design
// §11.4 step 7). It owns the turn (turn.OwnerMain) from the moment it
// routes successfully, and always leaves the turn StateDone before
// returning.
func (n *Nervous) answerMain(ctx context.Context, id string, t Turn, env runtime.Env, emit func(runtime.Event), fireAck func(), ackTimer Timer, briefCacheMiss bool, rec *routeRecorder) {
	rec.owner = "main"
	rec.answeredBy = "main"

	if err := n.turns.Route(id, turn.OwnerMain); err != nil {
		rec.outcome = "error"
		emit(runtime.Event{Kind: runtime.EventError, Error: "nervous: " + err.Error()})
		return
	}
	// The handoff acknowledgement must be visible before any main-path
	// output: stop the timer (it may already have fired) and fire it now
	// if it hasn't, via the same sync.Once so it's never emitted twice.
	ackTimer.Stop()
	fireAck()
	n.tier0Breaker.RecordSuccess() // no-op unless a half-open trial is in flight; reaching main is not itself a t0 failure
	rec.beginTier("main")
	firstSentenceOnce := false
	emitMain := speakableFilter(t.Channel, n.turns.Emitter(id, turn.OwnerMain, func(ev runtime.Event) {
		if !firstSentenceOnce && (ev.Kind == runtime.EventDelta || ev.Kind == runtime.EventSentence) {
			firstSentenceOnce = true
			rec.recordFirstSentence(n.cfg.Clock.Now())
		}
		emit(ev)
	}))

	if briefCacheMiss {
		text, err := runtime.ComputeAndCacheBrief(ctx, env)
		rec.endTier("main")
		if err != nil {
			rec.outcome = "error"
			emitMain(runtime.Event{Kind: runtime.EventError, Error: err.Error()})
			_ = n.turns.Done(id, turn.StateDone)
			return
		}
		rec.outcome = successOutcome(ctx)
		runtime.DeliverText(t.Channel, text, emitMain)
		n.lintVoiceReply(t.Channel, text, rec)
		emitMain(runtime.Event{Kind: runtime.EventDone, Text: text})
		_ = n.turns.Done(id, turn.StateDone)
		return
	}

	if !n.cfg.MainEnabled {
		rec.endTier("main")
		rec.outcome = "error"
		emitMain(runtime.Event{Kind: runtime.EventError, Error: "no tier is available to answer that"})
		_ = n.turns.Done(id, turn.StateDone)
		return
	}

	prompt := t.Context + t.Text
	if ring := n.ring.prompt(n.cfg.Clock.Now()); ring != "" {
		prompt += "\n\n" + ring
	}
	resp, err := runtime.ModelTurn(ctx, env, runtime.Turn{Channel: t.Channel, Prompt: prompt}, emitMain)
	rec.endTier("main")
	if err != nil {
		rec.outcome = "error"
		emitMain(runtime.Event{Kind: runtime.EventError, Error: err.Error()})
		_ = n.turns.Done(id, turn.StateDone)
		return
	}
	rec.outcome = successOutcome(ctx)
	n.lintVoiceReply(t.Channel, resp.Text, rec)
	emitMain(runtime.Event{Kind: runtime.EventDone, Text: resp.Text})
	_ = n.turns.Done(id, turn.StateDone)
}

// successOutcome reports "cancelled" instead of "answered" when ctx was
// already done by the time a call returned successfully. A fake or very
// fast real backend can still hand back a reply after its context was
// cancelled (nothing forces it to check), but the turn's own protocol-level
// truth is that it was cancelled, and the route_log row should say so
// rather than claiming a clean answer.
func successOutcome(ctx context.Context) string {
	if ctx.Err() != nil {
		return "cancelled"
	}
	return "answered"
}

// lintVoiceReply runs speak.Lint over a completed main-path reply's full raw
// text, on the voice channel only, records any warnings on rec (so they
// reach the route_log row), and — unchanged from R-13 — still hands them to
// Config.OnVoiceLint if set. The reply itself is never truncated here
// (Design §8.4) — Lint only reports, via the "overlength" tag, when it ran
// long.
func (n *Nervous) lintVoiceReply(ch runtime.Channel, raw string, rec *routeRecorder) {
	if ch != runtime.ChannelVoice {
		return
	}
	warnings := speak.Lint(raw, n.cfg.Style.Voice(), n.cfg.Style.MaxChars(string(runtime.ChannelVoice)))
	if len(warnings) == 0 {
		return
	}
	rec.addWarnings(warnings)
	if n.cfg.OnVoiceLint != nil {
		n.cfg.OnVoiceLint(warnings)
	}
}
