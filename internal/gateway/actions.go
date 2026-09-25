package gateway

import (
	"context"
	"fmt"

	"water/internal/approvals"
	"water/internal/connectors"
	"water/internal/gate"
	"water/internal/runtime"
	"water/internal/twins"
)

// proposeEnvelope is the single path that turns a "connector.function" call
// into a pending approval envelope: extracted verbatim from
// handleToolInvoke's queued branch so a model-initiated tool call and a
// sous-chef write-intent proposal ((*Daemon).ProposeAction, below) create
// envelopes identically -- same Propose call, same risk lookup, same audit
// trail. It does not decide WHETHER an envelope is needed; callers do that:
// handleToolInvoke from the function's level and the session's taint,
// ProposeAction because a write intent's action is level A by construction
// (internal/nervous/intents.checkAction only ever marks a write intent
// RequiresApproval when its granted level is A).
func proposeEnvelope(ctx context.Context, reg *connectors.Registry, aq *approvals.Queue, fn string, payload map[string]any, origin gate.Origin) (approvals.Envelope, error) {
	return aq.Propose(ctx, approvals.Envelope{
		Action: fn, Payload: payload, Origin: string(origin), Risk: string(functionRisk(reg, fn)),
	})
}

// ProposeAction implements nervous.ActionSink: a write intent's proposer
// built a payload for what should always be a level-A connector function,
// and the sous chef needs an envelope queued through the exact same path a
// model-initiated tool call uses. This defensively re-checks the level
// itself rather than trusting the caller, so a registry/manifest drift
// denies here instead of silently queuing something that was never meant to
// need approval (docs/slices/R.md's R-20 task list calls this out
// explicitly as a regression test: an R-level target must be denied at
// proposal time even though checkAction should never let one reach here).
// The channel-specific read-back/approval_required emission happens in
// nervous itself (the turn's own emitter), not here.
func (d *Daemon) ProposeAction(ctx context.Context, fn string, payload map[string]any, ch runtime.Channel) (approvals.Envelope, error) {
	f, ok := d.cfg.Manifest.Function(fn)
	if !ok {
		return approvals.Envelope{}, fmt.Errorf("%s is not in the %s manifest", fn, d.cfg.Manifest.ID)
	}
	if f.Level != twins.A {
		return approvals.Envelope{}, fmt.Errorf("sous proposals must be level A, %s is level %s", fn, f.Level)
	}
	return proposeEnvelope(ctx, d.cfg.Registry, d.cfg.Approvals, fn, payload, gate.P0)
}
