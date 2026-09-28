package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"water/internal/approvals"
	"water/internal/gate"
	"water/internal/store"
)

// draftView is a store.Draft as the daemon API returns it (docs/slices/UI.md
// Phase 3c): the row's own fields plus TemplateLabel, the code-built display
// string ("Reply"/"Delegation"/"Investor update section") so the client
// never has to know the enum's raw values.
type draftView struct {
	ID            string    `json:"id"`
	Template      string    `json:"template"`
	TemplateLabel string    `json:"template_label"`
	To            string    `json:"to"`
	Subject       string    `json:"subject"`
	Body          string    `json:"body"`
	SourceCardID  string    `json:"source_card_id,omitempty"`
	Provenance    string    `json:"provenance,omitempty"`
	Simulated     bool      `json:"simulated,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func draftViewOf(d store.Draft) draftView {
	return draftView{
		ID: d.ID, Template: d.Template, TemplateLabel: store.DraftTemplateLabel(d.Template),
		To: d.To, Subject: d.Subject, Body: d.Body, SourceCardID: d.SourceCardID,
		Provenance: d.Provenance, Simulated: d.Simulated, UpdatedAt: d.UpdatedAt,
	}
}

func draftLookupError(w http.ResponseWriter, id string, err error) {
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "draft "+id+" was not found", http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// handleListDrafts serves GET /v1/drafts: every draft, most-recently-updated
// first (store.ListDrafts's own order -- see its comment).
func (d *Daemon) handleListDrafts(w http.ResponseWriter, r *http.Request) {
	drafts, err := d.cfg.Store.ListDrafts(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]draftView, 0, len(drafts))
	for _, dr := range drafts {
		out = append(out, draftViewOf(dr))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetDraft serves GET /v1/drafts/{id}.
func (d *Daemon) handleGetDraft(w http.ResponseWriter, r *http.Request) {
	dr, err := d.cfg.Store.GetDraft(r.Context(), r.PathValue("id"))
	if err != nil {
		draftLookupError(w, r.PathValue("id"), err)
		return
	}
	writeJSON(w, http.StatusOK, draftViewOf(dr))
}

// draftSaveRequest is POST /v1/drafts/{id}'s body ("Save"): a full
// replacement of To/Subject/Body, not a partial patch -- the same
// full-replace convention approvalEditRequest.Payload uses for an approval
// edit. No field is required: a draft is allowed to be incomplete (that's
// the point of a draft), so saving an empty subject or body is not an error.
type draftSaveRequest struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// handleSaveDraft serves POST /v1/drafts/{id} ("Save"): persists exactly
// To/Subject/Body from the request body onto the row and bumps UpdatedAt.
// It creates no envelope and touches nothing outside this one row.
func (d *Daemon) handleSaveDraft(w http.ResponseWriter, r *http.Request) {
	var body draftSaveRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkspaceBody)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	dr, err := d.cfg.Store.SaveDraft(r.Context(), r.PathValue("id"), body.To, body.Subject, body.Body)
	if err != nil {
		draftLookupError(w, r.PathValue("id"), err)
		return
	}
	writeJSON(w, http.StatusOK, draftViewOf(dr))
}

// draftSubmitRequest is POST /v1/drafts/{id}/submit's body ("Send for
// approval"): To/Subject/Body, all required.
//
// Unlike decisionActionStageRequest.Payload (which may be omitted when the
// suggestion already carries one), this body is never optional and Submit
// never falls back to reading the draft's own stored row for these three
// fields. That is the deliberate answer to the plan's own open question
// ("decide and document whether Send-for-approval implicitly saves first or
// requires a prior Save"): Submit does neither -- it is not coupled to Save
// at all. The client always sends the editor's current, possibly-unsaved
// values in this same request; the daemon proposes exactly those values and
// never re-reads drafts.to_address/subject/body from storage, so a CEO who
// edits and immediately hits "Send for approval" (without ever clicking
// Save) always gets exactly what's on screen proposed, never a stale
// previously-saved version. Save and Submit are two fully independent
// mutations of two different things (a drafts row; an approvals envelope):
// Submit never writes to the drafts table, on success or on refusal, so
// there is no two-step save-then-propose sequence that could desync, and a
// recipient-check refusal leaves the draft row byte-for-byte unchanged by
// construction, not by a rollback.
type draftSubmitRequest struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// draftSubmitResponse mirrors decisionActionStageResponse's shape (Phase
// 3b), keyed by DraftID instead of CardID/ActionID.
type draftSubmitResponse struct {
	Status     string       `json:"status"` // always "queued": a draft has no staged/dedupe state of its own (migration 0021's own note)
	DraftID    string       `json:"draft_id"`
	ApprovalID string       `json:"approval_id"`
	Envelope   ApprovalView `json:"envelope"`
}

// handleSubmitDraft serves POST /v1/drafts/{id}/submit ("Send for
// approval"): proposes a gmail.send_message envelope carrying exactly the
// request body's To/Subject/Body (see draftSubmitRequest's comment for why
// never the stored row), with SourceCardID carried onto the envelope from
// the draft when it has one (docs/slices/UI.md Phase 1b/3a's
// "From <decision>" origin line).
//
// This goes through approvals.Queue.Propose unchanged, so a gmail.send_message
// whose recipient fails Slice W's recipient checks (an invalid address, or an
// unconfirmed near-miss domain) is refused there exactly as it would be for
// any other caller -- not bypassed for drafts. That refusal (wrapping
// approvals.ErrRecipient) is surfaced here as 422, matching this codebase's
// existing convention for "the request is well-formed but this specific
// content is refused" (see decision_actions.go/workspace_decisions.go's own
// 422s for a staged action the manifest doesn't grant). No envelope is
// created on that path, and -- since Submit never writes the drafts table at
// all -- the draft row is left exactly as it was.
//
// Submit never executes anything: Propose only ever queues a PENDING
// envelope for the CEO's own later yes/no, through the gate's normal
// approval path, exactly like every other outward-action proposal in this
// codebase.
func (d *Daemon) handleSubmitDraft(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body draftSubmitRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkspaceBody)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	to, subject, msg := strings.TrimSpace(body.To), strings.TrimSpace(body.Subject), strings.TrimSpace(body.Body)
	if to == "" || subject == "" || msg == "" {
		http.Error(w, "to, subject and body are all required to send for approval", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	dr, err := d.cfg.Store.GetDraft(ctx, id)
	if err != nil {
		draftLookupError(w, id, err)
		return
	}
	payload := map[string]any{"to": []string{to}, "subject": subject, "body": msg}
	// docs/slices/BRAND.md task 9: cover the brand template's inputs in this
	// envelope's PayloadHash, same as every other gmail.send_message propose
	// site (internal/gateway/brand_payload.go).
	payload, err = addBrandPayloadFieldsForTwin(d.cfg.Manifest.ID, payload)
	if err != nil {
		http.Error(w, "preparing the email's brand fields failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	env, err := d.cfg.Approvals.Propose(ctx, approvals.Envelope{
		Action: "gmail.send_message", Payload: payload, Origin: string(gate.P0),
		Risk:         string(functionRisk(d.cfg.Registry, "gmail.send_message")),
		SourceCardID: dr.SourceCardID,
	})
	if err != nil {
		if errors.Is(err, approvals.ErrRecipient) {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		http.Error(w, "proposing the draft failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, draftSubmitResponse{Status: "queued", DraftID: id, ApprovalID: env.ID, Envelope: d.approvalView(ctx, env)})
}
