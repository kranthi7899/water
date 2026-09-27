package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"water/internal/approvals"
	"water/internal/connectors"
	"water/internal/connectors/google/gapi"
	"water/internal/gate"
	"water/internal/nervous"
	"water/internal/runtime"
	"water/internal/twinlink"
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

// approvedExecTimeout bounds one approved action's execution, which runs
// detached from the deciding request's context (decideAndExecute below).
const approvedExecTimeout = 2 * time.Minute

// decideStageError distinguishes decideAndExecute's three outright-failure
// stages, so handleDecideApproval's thin wrapper can map each to the exact
// same HTTP status the pre-extraction handler used, without decideAndExecute
// itself needing to know anything about HTTP.
type decideStageError struct {
	err   error
	stage string // "not_found" | "hash_mismatch" | "internal"
}

func (e *decideStageError) Error() string { return e.err.Error() }
func (e *decideStageError) Unwrap() error { return e.err }

// decideOutcome is decideAndExecute's full result: richer than the
// Approver-facing nervous.DecisionOutcome (Status/Executed/Error only)
// because handleDecideApproval's existing JSON contract (DecisionResult)
// also needs the envelope, the echoed answer, the raw output and whether the
// outcome is unknown -- rebuilding those a second time in the HTTP wrapper
// would defeat the point of extracting this function at all.
type decideOutcome struct {
	Envelope       approvals.Envelope
	Answer         approvals.Answer
	Executed       bool
	Output         json.RawMessage
	Err            error
	OutcomeUnknown bool
}

// decideAndExecute is the single path from a hash-checked yes/no reply to an
// approved envelope actually running: extracted verbatim from the
// pre-R-21 handleDecideApproval (hash check, then Queue.Decide, then a
// detached Gate.Invoke, then Abandon on refusal) so the HTTP endpoint and a
// bound voice decision (DecideBound, below) share one implementation.
// internal/nervous never calls this directly -- it reaches it only through
// the narrower Approver interface DecideBound implements.
func (d *Daemon) decideAndExecute(ctx context.Context, id, payloadHash, reply string) (decideOutcome, error) {
	current, err := d.cfg.Approvals.Get(ctx, id)
	if err != nil {
		return decideOutcome{}, &decideStageError{err: err, stage: "not_found"}
	}
	if payloadHash == "" || payloadHash != current.PayloadHash {
		return decideOutcome{}, &decideStageError{
			err:   errors.New("payload hash does not match the current envelope; re-fetch and re-confirm"),
			stage: "hash_mismatch",
		}
	}
	answer := approvals.Match(reply)
	e, err := d.cfg.Approvals.Decide(ctx, id, answer)
	if err != nil {
		if e.ID == "" {
			// A store or audit failure: there is no envelope state to report.
			return decideOutcome{}, &decideStageError{err: err, stage: "internal"}
		}
		// Not pending any more, expired, or another decider won the race:
		// this answer was not applied, and the envelope shows what did.
		return decideOutcome{Envelope: e, Answer: answer, Err: err}, nil
	}
	if e.Status != approvals.Approved {
		return decideOutcome{Envelope: e, Answer: answer}, nil
	}
	// Approved: run it now, exactly once. Origin comes from the envelope
	// itself (set when the model or scheduler proposed it). The payload may
	// derive from external content (an S envelope is queued only because it
	// was tainted), so it is presented as Tainted; the gate claims any
	// presented envelope against its action and payload hash regardless.
	//
	// The execution is detached from the caller's context: once the gate
	// claims the envelope it is single-use, so a caller that disconnects
	// (an HTTP client's Ctrl-C or timeout) must not cancel the connector
	// call partway through. It still has its own bound.
	execCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), approvedExecTimeout)
	defer cancel()
	res, ierr := d.cfg.Gate.Invoke(execCtx, gate.Call{
		Function: e.Action, Args: e.Payload, Origin: gate.Origin(e.Origin), Taint: gate.Tainted, EnvelopeID: e.ID,
	})
	if ierr != nil {
		// Refused before the claim (rate cap, missing credential, audit
		// failure): the envelope is still Approved, and nothing else ever
		// executes an Approved envelope, so the yes would silently sit there
		// until it expired. End it as denied, with the reason, on the record.
		if cur, gerr := d.cfg.Approvals.Get(execCtx, id); gerr == nil && cur.Status == approvals.Approved {
			_, _ = d.cfg.Approvals.Abandon(execCtx, id, "execution refused: "+ierr.Error())
		}
	} else {
		// Executed: the derived trail's "sent" stage (approvals.Envelope.
		// Trail, docs/slices/UI.md Phase 1b) reads sent_at, and a Gmail send
		// also gets its thread_ref, so a later inbound reply on that thread
		// can be matched back to this approval (store.MarkApprovalReplied,
		// wired into sync's mail tick and agentmail's watcher). Best-effort:
		// this is trail bookkeeping, not the execution itself, which already
		// succeeded and must be reported as such either way.
		threadRef := gmailThreadRefFromOutput(e.Action, res.Output)
		_ = d.cfg.Store.MarkApprovalSent(execCtx, e.ID, threadRef, d.cfg.Approvals.Now())
	}
	latest, gerr := d.cfg.Approvals.Get(execCtx, id)
	if gerr != nil {
		latest = e
	}
	if ierr != nil {
		// Output set means the action ran and only indexing its result
		// failed; either that or an unknown outcome must never read as
		// "not executed", which would invite a second, duplicate request.
		return decideOutcome{
			Envelope: latest, Answer: answer, Executed: res.Output != nil, Output: res.Output, Err: ierr,
			OutcomeUnknown: errors.Is(ierr, gapi.ErrSendOutcomeUnknown) || errors.Is(ierr, twinlink.ErrOutcomeUnknown),
		}, nil
	}
	return decideOutcome{Envelope: latest, Answer: answer, Executed: true, Output: res.Output}, nil
}

// DecideBound implements nervous.Approver: a voice yes/no already bound to
// id/payloadHash (internal/nervous's own Readbacks, never Queue.Decide
// directly) decides it through the exact same decideAndExecute path POST
// /v1/approvals/{id}/decision uses. The returned error is non-nil only for
// decideAndExecute's outright-failure stages (not found, hash mismatch, an
// internal store/audit failure) -- a "not approved" or "expired" answer, or
// an execution failure, comes back as a normal DecisionOutcome with Error
// set, exactly like the HTTP endpoint reports it.
func (d *Daemon) DecideBound(ctx context.Context, id, payloadHash, reply string) (nervous.DecisionOutcome, error) {
	out, err := d.decideAndExecute(ctx, id, payloadHash, reply)
	if err != nil {
		return nervous.DecisionOutcome{}, err
	}
	errText := ""
	if out.Err != nil {
		errText = out.Err.Error()
	}
	return nervous.DecisionOutcome{Status: string(out.Envelope.Status), Executed: out.Executed, Error: errText}, nil
}

// gmailThreadRefFromOutput reads the Gmail thread id out of a successful
// gmail.send_message execution's output (gmail's own writeMessageOutput
// type, unexported, but its JSON shape includes "thread_id" -- the same
// independent-decode approach internal/agentmail's rawMessage already takes
// against gmail's list_messages output), so decideAndExecute can persist it
// onto the approval as thread_ref (docs/slices/UI.md Phase 1b). Every other
// action's envelope keeps thread_ref "" (the column's own default).
func gmailThreadRefFromOutput(action string, output json.RawMessage) string {
	if action != "gmail.send_message" || len(output) == 0 {
		return ""
	}
	var out struct {
		ThreadID string `json:"thread_id"`
	}
	if err := json.Unmarshal(output, &out); err != nil {
		return ""
	}
	return out.ThreadID
}
