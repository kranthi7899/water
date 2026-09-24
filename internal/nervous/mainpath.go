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
func (n *Nervous) answerMain(ctx context.Context, id string, t Turn, env runtime.Env, emit func(runtime.Event), fireAck func(), ackTimer Timer, briefCacheMiss bool) {
	if err := n.turns.Route(id, turn.OwnerMain); err != nil {
		emit(runtime.Event{Kind: runtime.EventError, Error: "nervous: " + err.Error()})
		return
	}
	// The handoff acknowledgement must be visible before any main-path
	// output: stop the timer (it may already have fired) and fire it now
	// if it hasn't, via the same sync.Once so it's never emitted twice.
	ackTimer.Stop()
	fireAck()
	emitMain := speakableFilter(t.Channel, n.turns.Emitter(id, turn.OwnerMain, emit))

	if briefCacheMiss {
		text, err := runtime.ComputeAndCacheBrief(ctx, env)
		if err != nil {
			emitMain(runtime.Event{Kind: runtime.EventError, Error: err.Error()})
			_ = n.turns.Done(id, turn.StateDone)
			return
		}
		runtime.DeliverText(t.Channel, text, emitMain)
		n.lintVoiceReply(t.Channel, text)
		emitMain(runtime.Event{Kind: runtime.EventDone, Text: text})
		_ = n.turns.Done(id, turn.StateDone)
		return
	}

	if !n.cfg.MainEnabled {
		emitMain(runtime.Event{Kind: runtime.EventError, Error: "no tier is available to answer that"})
		_ = n.turns.Done(id, turn.StateDone)
		return
	}

	resp, err := runtime.ModelTurn(ctx, env, runtime.Turn{Channel: t.Channel, Prompt: t.Context + t.Text}, emitMain)
	if err != nil {
		emitMain(runtime.Event{Kind: runtime.EventError, Error: err.Error()})
		_ = n.turns.Done(id, turn.StateDone)
		return
	}
	n.lintVoiceReply(t.Channel, resp.Text)
	emitMain(runtime.Event{Kind: runtime.EventDone, Text: resp.Text})
	_ = n.turns.Done(id, turn.StateDone)
}

// lintVoiceReply runs speak.Lint over a completed main-path reply's full raw
// text, on the voice channel only, and hands any warnings to
// Config.OnVoiceLint. The reply itself is never truncated here (Design
// §8.4) — Lint only reports, via the "overlength" tag, when it ran long.
// Storing warnings in a route_log row is R-14's job; this just makes them
// available.
func (n *Nervous) lintVoiceReply(ch runtime.Channel, raw string) {
	if ch != runtime.ChannelVoice || n.cfg.OnVoiceLint == nil {
		return
	}
	warnings := speak.Lint(raw, n.cfg.Style.Voice(), n.cfg.Style.MaxChars(string(runtime.ChannelVoice)))
	if len(warnings) > 0 {
		n.cfg.OnVoiceLint(warnings)
	}
}
