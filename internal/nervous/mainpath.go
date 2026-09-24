package nervous

import (
	"context"

	"water/internal/nervous/turn"
	"water/internal/runtime"
)

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
	emitMain := n.turns.Emitter(id, turn.OwnerMain, emit)

	if briefCacheMiss {
		text, err := runtime.ComputeAndCacheBrief(ctx, env)
		if err != nil {
			emitMain(runtime.Event{Kind: runtime.EventError, Error: err.Error()})
			_ = n.turns.Done(id, turn.StateDone)
			return
		}
		runtime.DeliverText(t.Channel, text, emitMain)
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
	emitMain(runtime.Event{Kind: runtime.EventDone, Text: resp.Text})
	_ = n.turns.Done(id, turn.StateDone)
}
