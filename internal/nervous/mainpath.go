package nervous

import (
	"context"
	"sync"

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
	// Mark this turn in flight for tool-call attribution (tooltrace.go,
	// R-14) the moment it actually owns the turn, and always resolve it on
	// every exit path below (success, error, brief-cache-miss, disabled):
	// EndMain hands back whichever quick.* tools this turn's own calls to
	// gateway/quick.go's handleQuickInvoke recorded, plus whether
	// attribution stayed unambiguous the whole time. Nothing called
	// BeginMain/EndMain before this task (R-14/R-16 built the mechanism and
	// its one real call site into RecordToolUse, but never registered a
	// turn as in flight for it to attribute to), so tools_used/
	// tools_attributed/quick_only/tool_signature never reached a real
	// route_log row until now.
	n.toolTracer.BeginMain(id)
	defer func() {
		used, attributed := n.toolTracer.EndMain(id)
		rec.toolsUsed = used
		rec.toolsAttributed = attributed
		rec.quickOnly, rec.toolSignature = QuickOnlySignature(used, quickToolPrefix)
	}()
	// The handoff acknowledgement must be visible before any main-path
	// output: stop the timer (it may already have fired) and fire it now
	// if it hasn't, via the same sync.Once so it's never emitted twice. It
	// is a silent ack on every channel (Slice W, D3), so this never speaks.
	ackTimer.Stop()
	fireAck()
	n.tier0Breaker.RecordSuccess() // no-op unless a half-open trial is in flight; reaching main is not itself a t0 failure
	rec.beginTier("main")

	// outMu guards mainOutput/firstSentenceOnce, which the model's stream
	// callback and the opt-in filler timer (another goroutine) both touch,
	// and serializes their emissions so the filler can never land after
	// the model's first delta or after the turn's done/error.
	var outMu sync.Mutex
	firstSentenceOnce := false
	mainOutput := false
	mainEmitter := n.turns.Emitter(id, turn.OwnerMain, emit)
	emitMain := speakableFilter(t.Channel, func(ev runtime.Event) {
		outMu.Lock()
		defer outMu.Unlock()
		switch ev.Kind {
		case runtime.EventDelta, runtime.EventSentence, runtime.EventDone, runtime.EventError:
			mainOutput = true
		}
		if !firstSentenceOnce && (ev.Kind == runtime.EventDelta || ev.Kind == runtime.EventSentence) {
			firstSentenceOnce = true
			rec.recordFirstSentence(n.cfg.Clock.Now())
		}
		mainEmitter(ev)
	})

	// The opt-in voice filler (router.voice_filler_ms, default off): one
	// short phrase, spoken only if the model has produced nothing by then.
	// It goes through main's own emitter (the router may no longer emit
	// once main owns the turn) and is never counted as the first sentence.
	if t.Channel == runtime.ChannelVoice && n.cfg.VoiceFiller > 0 {
		phrase := speak.Speakable(n.fillerPhrase(), speak.Options{})
		fillerTimer := n.cfg.Clock.AfterFunc(n.cfg.VoiceFiller, func() {
			outMu.Lock()
			defer outMu.Unlock()
			if mainOutput {
				return
			}
			mainOutput = true // at most once
			mainEmitter(runtime.Event{Kind: runtime.EventSentence, Text: phrase})
		})
		defer func() {
			fillerTimer.Stop()
			outMu.Lock()
			mainOutput = true // a timer already running must not emit after return
			outMu.Unlock()
		}()
	}

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
	resp, err := runtime.ModelTurn(ctx, env, runtime.Turn{Channel: t.Channel, Prompt: prompt, Utterance: t.Text}, emitMain)
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
