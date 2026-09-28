package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"water/internal/approvals"
	"water/internal/decisions"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
)

// decisionActionStageRequest is POST
// /v1/decisions/{id}/actions/{action_id}/stage's body: Payload overrides
// the suggestion's own payload when given (the CEO edited it before
// review, the same shape decisionStageRequest.Payload already allows); it
// may be omitted only when the suggestion itself already carries a
// payload.
type decisionActionStageRequest struct {
	Payload map[string]any `json:"payload"`
}

// decisionActionStageResponse mirrors decisionStageResponse (see
// workspace_decisions.go's handleStageDecision), keyed by ActionID instead
// of by function name: this is Phase 3b's card_action_states-backed
// sibling of the function-keyed POST /v1/decisions/{id}/stage, which stays
// exactly as it is for one release (docs/slices/UI.md Phase 3b's own New
// endpoints table).
type decisionActionStageResponse struct {
	Status     string       `json:"status"` // "queued" | "already_staged"
	CardID     string       `json:"card_id"`
	ActionID   string       `json:"action_id"`
	ApprovalID string       `json:"approval_id"`
	Envelope   ApprovalView `json:"envelope"`
}

// findSuggestion returns card's own ActionSuggestion named id, or ok=false
// when the card names no such suggestion (a stale id, a different card's
// id, or a typo).
func findSuggestion(card *decisions.Card, id string) (decisions.Suggestion, bool) {
	for _, s := range card.ActionSuggestions {
		if s.ID == id {
			return s, true
		}
	}
	return decisions.Suggestion{}, false
}

// handleStageDecisionAction is handleStageDecision's per-action sibling
// (docs/slices/UI.md Phase 3b): it stages exactly one of a card's
// ActionSuggestions into its own PENDING approval envelope, recorded in
// store.CardActionState keyed by (card_id, action_id) rather than the
// single card_states row handleStageDecision uses. Two actions on the same
// card are staged completely independently: staging one never reads or
// writes the other action's row on the same card, which is the entire
// reason card_action_states exists (docs/slices/UI.md Phase 1c) instead of
// this endpoint reusing card_states as handleStageDecision does.
//
// A card-level dismiss (store.CardState, from POST
// /v1/decisions/{id}/dismiss) still governs every action on it, exactly as
// it does for the function-keyed route: card_action_states has no
// dismissed status of its own, so that check reads card_states first.
//
// Refusals mirror handleStageDecision's: 404 for a card that isn't open or
// an action_id the card doesn't name; 409 for a dismissed card, or for an
// action already acted on (approved or executed); 422 for an action the
// manifest doesn't grant at level A (yet); 400 for a missing or
// schema-invalid payload. Staging the same action twice while its envelope
// is still pending answers "already_staged" with that same envelope,
// exactly like the function-keyed route.
func (d *Daemon) handleStageDecisionAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	actionID := r.PathValue("action_id")
	var body decisionActionStageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkspaceBody)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	card, err := d.findOpenCard(ctx, id)
	if err != nil {
		cardLookupError(w, id, err)
		return
	}
	switch cs, err := d.cfg.Store.GetCardState(ctx, id); {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	case cs.Status == "dismissed":
		http.Error(w, "decision card "+id+" was dismissed", http.StatusConflict)
		return
	}

	sug, ok := findSuggestion(card, actionID)
	if !ok {
		http.Error(w, actionID+" is not an action of decision card "+id, http.StatusNotFound)
		return
	}
	if !sug.Actionable {
		http.Error(w, actionID+" is not granted at level A by the manifest", http.StatusUnprocessableEntity)
		return
	}

	prev, err := d.cfg.Store.GetCardActionState(ctx, id, actionID)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	case prev.Status == "staged" && prev.ApprovalID != "":
		// Staging twice must not queue a second envelope for the same
		// action while the first still awaits an answer: hand back that
		// one, exactly like handleStageDecision.
		env, gerr := d.cfg.Approvals.Get(ctx, prev.ApprovalID)
		if gerr == nil && env.Status == approvals.Pending && env.ExpiresAt.After(time.Now()) {
			writeJSON(w, http.StatusOK, decisionActionStageResponse{Status: "already_staged", CardID: id, ActionID: actionID, ApprovalID: env.ID, Envelope: viewOf(env)})
			return
		}
		if gerr == nil && (env.Status == approvals.Approved || env.Status == approvals.Executed) {
			http.Error(w, "action "+actionID+" on decision card "+id+" was already acted on ("+string(env.Status)+" "+env.ID+"); reject the card instead of staging again", http.StatusConflict)
			return
		}
	}

	// Re-check the grant at staging time rather than trusting the
	// suggestion's own Actionable flag alone (the manifest is the
	// authority; see handleStageDecision's identical comment).
	if f, ok := d.cfg.Manifest.Function(sug.Function); !ok || f.Level != twins.A {
		http.Error(w, sug.Function+" is not granted at level A by the manifest", http.StatusUnprocessableEntity)
		return
	}
	_, spec, ok := d.cfg.Registry.Lookup(sug.Function)
	if !ok {
		http.Error(w, sug.Function+" is not available on this daemon", http.StatusUnprocessableEntity)
		return
	}
	payload := body.Payload
	if len(payload) == 0 {
		payload = sug.Payload
	}
	if len(payload) == 0 {
		http.Error(w, "payload is required: this action does not prepare "+sug.Function+"'s arguments itself", http.StatusBadRequest)
		return
	}
	if err := spec.Schema.Validate(payload); err != nil {
		http.Error(w, "payload does not fit "+sug.Function+": "+err.Error(), http.StatusBadRequest)
		return
	}

	// Deterministic links (docs/slices/V.md D6) go first, after every
	// refusal above and before anything is queued, exactly as
	// handleStageDecision does.
	if err := d.linkDecision(ctx, card); err != nil {
		http.Error(w, "linking decision card "+id+" failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// docs/slices/BRAND.md task 9 (see handleStageDecision's identical
	// comment and internal/gateway/brand_payload.go).
	if sug.Function == "gmail.send_message" {
		var err error
		payload, err = addBrandPayloadFieldsForTwin(d.cfg.Manifest.ID, payload)
		if err != nil {
			http.Error(w, "preparing the email's brand fields failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	env, err := d.cfg.Approvals.Propose(ctx, approvals.Envelope{
		Action: sug.Function, Payload: payload, Origin: string(gate.P0),
		Risk:         string(functionRisk(d.cfg.Registry, sug.Function)),
		EvidenceRefs: card.SourceItemIDs,
		SourceCardID: id,
	})
	if err != nil {
		http.Error(w, "staging refused: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := d.cfg.Store.SetCardActionState(ctx, store.CardActionState{CardID: id, ActionID: actionID, Status: "staged", ApprovalID: env.ID}); err != nil {
		// The envelope exists and is the source of truth for what may run;
		// failing to remember this action's state only risks a duplicate
		// staging later, so report it rather than pretend nothing happened
		// (the same posture handleStageDecision's own comment documents).
		http.Error(w, "staged "+env.ID+", but recording the action's state failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, decisionActionStageResponse{Status: "queued", CardID: id, ActionID: actionID, ApprovalID: env.ID, Envelope: viewOf(env)})
}
