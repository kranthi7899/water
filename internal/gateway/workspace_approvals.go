package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"water/internal/approvals"
	"water/internal/gate"
	"water/internal/store"
)

// approvalStatusSets maps GET /v1/approvals?status= to the envelope
// statuses it covers. "decided" is every final or post-answer state.
var approvalStatusSets = map[string][]approvals.Status{
	"pending":  {approvals.Pending},
	"decided":  {approvals.Approved, approvals.Denied, approvals.Expired, approvals.Executed},
	"all":      {approvals.Pending, approvals.Approved, approvals.Denied, approvals.Expired, approvals.Executed},
	"approved": {approvals.Approved},
	"denied":   {approvals.Denied},
	"expired":  {approvals.Expired},
	"executed": {approvals.Executed},
}

const (
	defaultApprovalListLimit = 50
	maxApprovalListLimit     = 500
)

// listApprovalsFiltered serves GET /v1/approvals with any of:
//
//	status=pending|decided|all|approved|denied|expired|executed (default pending)
//	kind=<connector>|<connector.function>  (e.g. "gmail" or "gmail.send_message")
//	limit=<n>  (default 50, max 500)
//
// Pending envelopes come oldest first (the existing list's order: the
// longest-waiting answer first); any other set comes newest first (a
// history view). Stale envelopes are expired first, as Pending() does, so
// "pending" never shows one past its expiry. Every row goes through
// Queue.Get, i.e. is hash-verified exactly like GET /v1/approvals/{id}.
func (d *Daemon) listApprovalsFiltered(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	statusKey := strings.ToLower(strings.TrimSpace(q.Get("status")))
	if statusKey == "" {
		statusKey = "pending"
	}
	statuses, ok := approvalStatusSets[statusKey]
	if !ok {
		http.Error(w, "unknown status (want pending|decided|all|approved|denied|expired|executed)", http.StatusBadRequest)
		return
	}
	kind := strings.TrimSpace(q.Get("kind"))
	limit := defaultApprovalListLimit
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
			return
		}
		limit = min(n, maxApprovalListLimit)
	}

	ctx := r.Context()
	if err := d.cfg.Approvals.ExpireStale(ctx); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var envs []approvals.Envelope
	for _, st := range statuses {
		rows, err := d.cfg.Store.ListApprovals(ctx, string(st))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, row := range rows {
			if !approvalKindMatches(row.Action, kind) {
				continue
			}
			e, err := d.cfg.Approvals.Get(ctx, row.ID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			envs = append(envs, e)
		}
	}
	if statusKey == "pending" {
		sort.SliceStable(envs, func(i, j int) bool { return envs[i].CreatedAt.Before(envs[j].CreatedAt) })
	} else {
		sort.SliceStable(envs, func(i, j int) bool { return envs[i].CreatedAt.After(envs[j].CreatedAt) })
	}
	if len(envs) > limit {
		envs = envs[:limit]
	}
	writeJSON(w, http.StatusOK, d.approvalViews(ctx, envs))
}

// approvalKindMatches reports whether action ("connector.function") is
// covered by kind: empty matches everything, "gmail" matches every gmail.*
// action, "gmail.send_message" matches only itself.
func approvalKindMatches(action, kind string) bool {
	if kind == "" {
		return true
	}
	if strings.Contains(kind, ".") {
		return action == kind
	}
	connector, _, _ := strings.Cut(action, ".")
	return connector == kind
}

// approvalEditRequest is POST /v1/approvals/{id}/edit's body: the hash of
// the envelope the CEO was looking at when they edited (a stale one is a
// 409, exactly like the decision endpoint), and the full edited payload.
type approvalEditRequest struct {
	PayloadHash string         `json:"payload_hash"`
	Payload     map[string]any `json:"payload"`
}

// approvalEditResponse is POST /v1/approvals/{id}/edit's answer: the old
// envelope, now denied ("voided by edit"), and the new pending one that
// needs its own decision.
type approvalEditResponse struct {
	Voided   ApprovalView `json:"voided"`
	Envelope ApprovalView `json:"envelope"`
}

// handleEditApproval edits a pending (or approved-but-not-yet-run)
// envelope the only way the approvals invariant allows: approvals.Queue.Edit
// voids the old envelope (Denied, "voided by edit", on the audit record)
// and proposes the edited payload as a brand-new pending envelope with its
// own id and payload hash. Nothing is approved or executed by editing; the
// CEO must answer the new envelope through POST
// /v1/approvals/{new id}/decision like any other. The action itself can't
// be changed, and the new payload must fit the action's connector schema.
// A decision card staged into the old envelope is re-pointed at the new one
// (store.RepointCardState).
//
// 409 on a stale payload_hash or an envelope that is no longer editable
// (already decided, executed or expired); 404 on an unknown id; 400 on a
// payload that doesn't fit.
func (d *Daemon) handleEditApproval(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body approvalEditRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkspaceBody)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(body.Payload) == 0 {
		http.Error(w, "payload is required", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if err := d.cfg.Approvals.ExpireStale(ctx); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	current, err := d.cfg.Approvals.Get(ctx, id)
	if errors.Is(err, approvals.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if body.PayloadHash == "" || body.PayloadHash != current.PayloadHash {
		http.Error(w, "payload hash does not match the current envelope; re-fetch and re-edit", http.StatusConflict)
		return
	}
	if current.Status != approvals.Pending && current.Status != approvals.Approved {
		http.Error(w, "approval "+id+" is "+string(current.Status)+" and can no longer be edited", http.StatusConflict)
		return
	}
	if _, spec, ok := d.cfg.Registry.Lookup(current.Action); ok {
		if err := spec.Schema.Validate(body.Payload); err != nil {
			http.Error(w, "payload does not fit "+current.Action+": "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	next, err := d.cfg.Approvals.Edit(ctx, id, body.Payload)
	if err != nil {
		// Queue.Edit's own compare-and-swap lost a race (decided or claimed
		// in between) or the envelope wasn't editable: nothing was voided.
		if latest, gerr := d.cfg.Approvals.Get(ctx, id); gerr == nil && latest.Status != current.Status {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// A decision card staged into the old envelope now waits on the new one
	// (V-ui2): without this it would point at a voided envelope, read as
	// no longer staged, and invite a second staging.
	if _, err := d.cfg.Store.RepointCardState(ctx, id, next.ID); err != nil {
		http.Error(w, "edited "+id+" into "+next.ID+", but re-pointing its decision card failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	voided, err := d.cfg.Approvals.Get(ctx, id)
	if err != nil {
		voided = current
	}
	writeJSON(w, http.StatusOK, approvalEditResponse{Voided: viewOf(voided), Envelope: viewOf(next)})
}

// requestChangesRequest is POST /v1/approvals/{id}/request-changes's body.
type requestChangesRequest struct {
	Note string `json:"note"`
}

// requestChangesResponse answers with both envelopes the endpoint touched:
// the original, now denied, and the new reply proposed to the requester.
type requestChangesResponse struct {
	Denied   ApprovalView `json:"denied"`
	Envelope ApprovalView `json:"envelope"`
}

// handleRequestChanges is docs/slices/UI.md Phase 3a/U15's "Request
// changes" button on a person-request approval card: the CEO isn't ready to
// answer the request either way and wants to send the requester a note back
// instead.
//
// Ordering is check-then-mutate, never the reverse: the requester's roster
// address is resolved FIRST, and only once that succeeds does anything get
// denied or proposed. A person-request approval with no resolvable
// requester (RequestedBy is "" or names nobody in the roster) or a
// requester with no email on file answers 409 and changes nothing --
// exactly the failure mode a partial "denied the original, then couldn't
// tell them" would be worse than.
//
// Once the check passes: the original is denied with the fixed reason
// "changes requested" (approvals.Queue.Reject -- not Decide, since this is
// not the CEO answering the person's request either way), then a brand-new
// gmail.send_message envelope carrying the CEO's note is proposed to that
// address, gate-routed and approval-gated exactly like every other outward
// action. Nothing is sent by this endpoint itself.
func (d *Daemon) handleRequestChanges(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body requestChangesRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkspaceBody)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	note := strings.TrimSpace(body.Note)
	if note == "" {
		http.Error(w, "note is required", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if err := d.cfg.Approvals.ExpireStale(ctx); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	current, err := d.cfg.Approvals.Get(ctx, id)
	if errors.Is(err, approvals.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if current.Status != approvals.Pending {
		http.Error(w, "approval "+id+" is "+string(current.Status)+" and can no longer have changes requested", http.StatusConflict)
		return
	}
	// Fail closed, before anything is mutated: resolve the requester's
	// roster address first.
	if current.RequestedBy == "" {
		http.Error(w, "this approval has no requester to send changes back to", http.StatusConflict)
		return
	}
	person, err := store.Get[store.Person](ctx, d.cfg.Store, "seed", current.RequestedBy)
	if err != nil {
		http.Error(w, "the requester is not in the roster", http.StatusConflict)
		return
	}
	addr := person.Identity("email")
	if addr == "" {
		http.Error(w, person.Name+" has no email address on file", http.StatusConflict)
		return
	}
	denied, err := d.cfg.Approvals.Reject(ctx, id, "changes requested")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	payload := map[string]any{"to": []string{addr}, "subject": "Changes requested", "body": note}
	env, err := d.cfg.Approvals.Propose(ctx, approvals.Envelope{
		Action: "gmail.send_message", Payload: payload, Origin: string(gate.P0),
		Risk: string(functionRisk(d.cfg.Registry, "gmail.send_message")),
	})
	if err != nil {
		// The original is already denied; there is no undo for that (the
		// same posture handleEditApproval's own comment documents for its
		// void-then-propose step), so this surfaces the failure plainly
		// rather than pretending nothing happened.
		http.Error(w, "denied the original, but proposing the reply failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, requestChangesResponse{Denied: viewOf(denied), Envelope: d.approvalView(ctx, env)})
}
