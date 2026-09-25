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
	"water/internal/runtime"
)

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
