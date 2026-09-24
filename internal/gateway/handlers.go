package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"water/internal/approvals"
	"water/internal/gate"
	"water/internal/runtime"
)

// DecisionResult is what POST /v1/approvals/{id}/decision returns: the
// envelope's final state, and — when the answer was yes — whether the
// action actually ran and what it returned. Approving is not itself
// executing (Claim is a separate, single-use step), so this endpoint is
// where that execution happens: it is the one place a "yes" from the CEO
// turns directly into the gate running the action, exactly once.
type DecisionResult struct {
	Envelope approvals.Envelope `json:"envelope"`
	Executed bool               `json:"executed"`
	Output   json.RawMessage    `json:"output,omitempty"`
	Error    string             `json:"error,omitempty"`
}

// handleTurn streams one turn as NDJSON: ack, delta*, sentence*,
// approval_required*, then done or error. channel selects cli | voice |
// text-bar delivery. The turn's origin is always P0 (the CEO's immediate
// request); taint is computed from what the assembled context pulled in.
func (d *Daemon) handleTurn(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Channel string `json:"channel"`
		Prompt  string `json:"prompt"`
		// Clear resets the warm session's context before this turn (the
		// chat client's /clear). A false zero value is always safe: a fresh
		// warm session's first turn already starts clean.
		Clear bool `json:"clear"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	clearOnly := body.Clear && strings.TrimSpace(body.Prompt) == ""
	if !body.Clear && strings.TrimSpace(body.Prompt) == "" {
		http.Error(w, "empty prompt", http.StatusBadRequest)
		return
	}
	if body.Clear && d.cfg.Warm != nil {
		d.cfg.Warm.Clear()
	}
	if clearOnly {
		// /clear alone: reset and end the stream. Running a model turn here
		// would cold-start a process just to send it a blank message.
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		enc := json.NewEncoder(w)
		_ = enc.Encode(runtime.Event{Kind: runtime.EventAck})
		_ = enc.Encode(runtime.Event{Kind: runtime.EventDone})
		return
	}
	ch := runtime.Channel(body.Channel)
	switch ch {
	case runtime.ChannelCLI, runtime.ChannelVoice, runtime.ChannelTextBar:
	default:
		ch = runtime.ChannelCLI
	}

	taskID := newID("task")
	ctx, cancel := context.WithCancel(r.Context())
	d.registerTask(taskID, cancel)
	defer func() { cancel(); d.unregisterTask(taskID) }()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Water-Task-Id", taskID)
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	// The sink serializes this turn's own events with approval_required
	// events a concurrent tool-invoke handler reports for it, and stops all
	// writes once done/error is out (and before this handler returns).
	sink := &turnSink{write: func(e runtime.Event) {
		_ = enc.Encode(e)
		if flusher != nil {
			flusher.Flush()
		}
	}}
	d.registerSink(taskID, sink)
	defer func() { d.unregisterSink(taskID); sink.close() }()

	// Taint is computed from the state the context assembler pulls in, before
	// the turn runs, and escalates the twin's one tool-proxy token so every
	// tool call this and every later turn makes is tainted together (Part A2
	// spec: "every tool call in that turn is tainted" — escalated, since the
	// warm session's MCP bridge child serves many turns with one token; see
	// Daemon.escalateTaint).
	_, tainted := runtime.StateSummary(ctx, d.baseEnv())
	d.escalateTaint(tainted)
	env := d.turnEnv()

	runtime.RunTurn(ctx, env, runtime.Turn{Channel: ch, Prompt: body.Prompt}, sink.emit)
}

func (d *Daemon) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	envs, err := d.cfg.Approvals.Pending(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, envs)
}

// handleDecideApproval requires the payload hash the client was shown, and
// refuses a decision made against a stale one — an edit or a race must never
// be answered blind.
func (d *Daemon) handleDecideApproval(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		PayloadHash string `json:"payload_hash"`
		Reply       string `json:"reply"` // free text ("yes"/"no"/...), matched deterministically
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	current, err := d.cfg.Approvals.Get(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if body.PayloadHash == "" || body.PayloadHash != current.PayloadHash {
		http.Error(w, "payload hash does not match the current envelope; re-fetch and re-confirm", http.StatusConflict)
		return
	}
	e, err := d.cfg.Approvals.Decide(r.Context(), id, approvals.Match(body.Reply))
	if err != nil {
		writeJSON(w, http.StatusOK, DecisionResult{Envelope: e, Error: err.Error()})
		return
	}
	if e.Status != approvals.Approved {
		writeJSON(w, http.StatusOK, DecisionResult{Envelope: e})
		return
	}
	// Approved: run it now, exactly once. Origin comes from the envelope
	// itself (set when the model or scheduler proposed it). The payload may
	// derive from external content (an S envelope is queued only because it
	// was tainted), so it is presented as Tainted; the gate claims any
	// presented envelope against its action and payload hash regardless.
	//
	// The execution is detached from the request: once the gate claims the
	// envelope it is single-use, so a client that disconnects (Ctrl-C, its
	// own timeout) must not cancel the connector call partway through. It
	// still has its own bound.
	execCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), approvedExecTimeout)
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
	}
	latest, gerr := d.cfg.Approvals.Get(execCtx, id)
	if gerr != nil {
		latest = e
	}
	if ierr != nil {
		writeJSON(w, http.StatusOK, DecisionResult{Envelope: latest, Error: ierr.Error()})
		return
	}
	writeJSON(w, http.StatusOK, DecisionResult{Envelope: latest, Executed: true, Output: res.Output})
}

// approvedExecTimeout bounds one approved action's execution, which runs
// detached from the deciding request's context.
const approvedExecTimeout = 2 * time.Minute

// handleState answers a compact snapshot: today's events and pending
// approvals, the same data the fast paths and system prompt use.
func (d *Daemon) handleState(w http.ResponseWriter, r *http.Request) {
	summary, _ := runtime.StateSummary(r.Context(), d.baseEnv())
	pending, _ := d.cfg.Approvals.Pending(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"twin":              d.cfg.Manifest.ID,
		"summary":           summary,
		"pending_approvals": len(pending),
		"observed_at":       time.Now().UTC(),
	})
}
