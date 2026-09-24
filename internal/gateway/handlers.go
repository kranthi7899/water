package gateway

import (
	"context"
	"encoding/json"
	"net/http"
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
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
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

	// Taint is computed from the state the context assembler pulls in, before
	// the turn runs, and escalates the twin's one tool-proxy token so every
	// tool call this and every later turn makes is tainted together (Part A2
	// spec: "every tool call in that turn is tainted" — escalated, since the
	// warm session's MCP bridge child serves many turns with one token; see
	// Daemon.escalateTaint).
	_, tainted := runtime.AssembleSystem(ctx, d.baseEnv())
	d.escalateTaint(tainted)
	env := d.turnEnv()

	runtime.RunTurn(ctx, env, runtime.Turn{Channel: ch, Prompt: body.Prompt}, func(e runtime.Event) {
		_ = enc.Encode(e)
		if flusher != nil {
			flusher.Flush()
		}
	})
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
	// itself (set when the model or scheduler proposed it); Taint does not
	// gate an already-approved envelope (level A always requires one
	// regardless of taint), so Clean here just satisfies the call shape.
	res, ierr := d.cfg.Gate.Invoke(r.Context(), gate.Call{
		Function: e.Action, Args: e.Payload, Origin: gate.Origin(e.Origin), Taint: gate.Clean, EnvelopeID: e.ID,
	})
	latest, gerr := d.cfg.Approvals.Get(r.Context(), id)
	if gerr != nil {
		latest = e
	}
	if ierr != nil {
		writeJSON(w, http.StatusOK, DecisionResult{Envelope: latest, Error: ierr.Error()})
		return
	}
	writeJSON(w, http.StatusOK, DecisionResult{Envelope: latest, Executed: true, Output: res.Output})
}

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
