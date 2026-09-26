package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"water/internal/approvals"
	"water/internal/decisions"
	"water/internal/gate"
	"water/internal/recordlinks"
	"water/internal/store"
	"water/internal/twins"
)

// maxWorkspaceBody bounds every workspace-UI request body (Slice V-ui): a
// thread message, a staged payload or an edited one — text, never files.
const maxWorkspaceBody = 64 << 10

// errNoOpenCard is findOpenCard's answer for an id that is not among the
// cards Trigger.Run builds right now (never built, or no longer open).
var errNoOpenCard = errors.New("no open decision card")

// findOpenCard rebuilds today's cards (decisions.Trigger.Run — the same
// call GET /v1/decisions and POST /v1/decisions/{id}/email make; cards are
// never persisted, so there is nothing else to look one up in) and returns
// the one with id.
func (d *Daemon) findOpenCard(ctx context.Context, id string) (*decisions.Card, error) {
	if d.cfg.Decisions == nil {
		return nil, errNoOpenCard
	}
	cards, err := d.cfg.Decisions.Run(ctx, time.Now())
	if err != nil {
		return nil, err
	}
	for _, c := range cards {
		if c != nil && c.ID == id {
			return c, nil
		}
	}
	return nil, errNoOpenCard
}

// cardLookupError maps findOpenCard's error to a response.
func cardLookupError(w http.ResponseWriter, id string, err error) {
	if errors.Is(err, errNoOpenCard) {
		http.Error(w, "no open decision card "+id, http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// linkDecision writes decision -> involves -> person for the card's source
// message sender when the roster resolves it by email
// (recordlinks.LinkDecision; docs/slices/V.md D6: deterministic links only,
// never guessed). Idempotent, and a no-op for a sender the roster doesn't
// know or a source item that isn't a stored message.
func (d *Daemon) linkDecision(ctx context.Context, card *decisions.Card) error {
	if card == nil || d.cfg.Store == nil {
		return nil
	}
	_, err := recordlinks.LinkDecision(ctx, d.cfg.Store, card.ID, card.SourceItemIDs)
	return err
}

// decisionStageRequest is POST /v1/decisions/{id}/stage's body. Function
// names which of the card's staged actions to prepare; it may be omitted
// when the card has exactly one actionable one. Payload is the action's
// arguments as the CEO reviewed/edited them in the UI; it may be omitted
// only when the card itself carries a payload for that action.
type decisionStageRequest struct {
	Function string         `json:"function"`
	Payload  map[string]any `json:"payload"`
}

// decisionStageResponse is POST /v1/decisions/{id}/stage's answer: the
// pending envelope staging created (or, when this card was already staged
// and that envelope is still pending, that same one — Status
// "already_staged"). Nothing has executed either way: confirming is POST
// /v1/approvals/{approval_id}/decision with envelope.payload_hash, exactly
// like every other approval.
type decisionStageResponse struct {
	Status     string       `json:"status"` // "queued" | "already_staged"
	CardID     string       `json:"card_id"`
	ApprovalID string       `json:"approval_id"`
	Envelope   ApprovalView `json:"envelope"`
}

// handleStageDecision is the first half of a decision card's
// stage-then-confirm "approve" (docs/slices/V.md §5): it turns one of the
// card's staged actions into a PENDING approval envelope through the same
// approvals.Queue.Propose every other outward action uses (the model's
// tool calls, a write intent, POST /v1/decisions/{id}/email), records the
// card as staged (store.CardState), and returns. It never executes
// anything: the second half is the existing POST
// /v1/approvals/{id}/decision, which hash-checks the CEO's answer and runs
// the gate. There is no side door here — the envelope is identical to one a
// model tool call would have queued.
//
// Refusals: 404 for a card that is not open; 409 for a dismissed card;
// 422 when the card has no stageable action at all, or none the manifest
// grants at level A yet, or the chosen function isn't one of the card's
// actionable ones; 400 for a missing or schema-invalid payload.
func (d *Daemon) handleStageDecision(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body decisionStageRequest
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
	prev, err := d.cfg.Store.GetCardState(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	case prev.Status == "dismissed":
		http.Error(w, "decision card "+id+" was dismissed", http.StatusConflict)
		return
	case prev.Status == "staged" && prev.ApprovalID != "":
		// Staging twice must not queue a second envelope for the same card
		// while the first still awaits an answer: hand back that one.
		env, gerr := d.cfg.Approvals.Get(ctx, prev.ApprovalID)
		if gerr == nil && env.Status == approvals.Pending && env.ExpiresAt.After(time.Now()) {
			writeJSON(w, http.StatusOK, decisionStageResponse{Status: "already_staged", CardID: id, ApprovalID: env.ID, Envelope: viewOf(env)})
			return
		}
		// Approved (execution in flight) or executed: a new envelope would be
		// a duplicate send one yes away. Only denied/expired may be re-staged.
		if gerr == nil && (env.Status == approvals.Approved || env.Status == approvals.Executed) {
			http.Error(w, "decision card "+id+" was already acted on ("+string(env.Status)+" "+env.ID+"); dismiss it instead of staging again", http.StatusConflict)
			return
		}
	}

	if len(card.StagedActions) == 0 {
		http.Error(w, "decision card "+id+" has no stageable action (its type names no staged_actions)", http.StatusUnprocessableEntity)
		return
	}
	var actionable []decisions.StagedAction
	for _, a := range card.StagedActions {
		if a.Actionable {
			actionable = append(actionable, a)
		}
	}
	if len(actionable) == 0 {
		http.Error(w, "decision card "+id+" has no stageable action: none of its staged actions is granted at level A yet", http.StatusUnprocessableEntity)
		return
	}
	fn := strings.TrimSpace(body.Function)
	var chosen *decisions.StagedAction
	if fn == "" {
		if len(actionable) != 1 {
			http.Error(w, "function is required: this card has more than one actionable staged action", http.StatusBadRequest)
			return
		}
		chosen = &actionable[0]
	} else {
		for i := range actionable {
			if actionable[i].Function == fn {
				chosen = &actionable[i]
				break
			}
		}
		if chosen == nil {
			http.Error(w, fn+" is not an actionable staged action of decision card "+id, http.StatusUnprocessableEntity)
			return
		}
	}

	// Re-check the grant at staging time rather than trusting the card's
	// Actionable flag alone (the manifest is the authority; the card was
	// built from it moments ago, but a stale or drifted registry must deny
	// here, not queue something the gate can never run).
	if f, ok := d.cfg.Manifest.Function(chosen.Function); !ok || f.Level != twins.A {
		http.Error(w, chosen.Function+" is not granted at level A by the manifest", http.StatusUnprocessableEntity)
		return
	}
	_, spec, ok := d.cfg.Registry.Lookup(chosen.Function)
	if !ok {
		http.Error(w, chosen.Function+" is not available on this daemon", http.StatusUnprocessableEntity)
		return
	}
	payload := body.Payload
	if len(payload) == 0 {
		payload = chosen.Payload
	}
	if len(payload) == 0 {
		http.Error(w, "payload is required: this card does not prepare "+chosen.Function+"'s arguments itself", http.StatusBadRequest)
		return
	}
	if err := spec.Schema.Validate(payload); err != nil {
		http.Error(w, "payload does not fit "+chosen.Function+": "+err.Error(), http.StatusBadRequest)
		return
	}

	// Deterministic links (docs/slices/V.md D6) go first, after every
	// refusal above and before anything is queued: a store failure here
	// stages nothing, so a retry starts clean.
	if err := d.linkDecision(ctx, card); err != nil {
		http.Error(w, "linking decision card "+id+" failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	env, err := d.cfg.Approvals.Propose(ctx, approvals.Envelope{
		Action: chosen.Function, Payload: payload, Origin: string(gate.P0),
		Risk:         string(functionRisk(d.cfg.Registry, chosen.Function)),
		EvidenceRefs: card.SourceItemIDs,
	})
	if err != nil {
		http.Error(w, "staging refused: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := d.cfg.Store.SetCardState(ctx, store.CardState{CardID: id, Status: "staged", ApprovalID: env.ID}); err != nil {
		// The envelope exists and is the source of truth for what may run;
		// failing to remember the card's state only risks a duplicate
		// staging later, so report it rather than pretend nothing happened.
		http.Error(w, "staged "+env.ID+", but recording the card state failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, decisionStageResponse{Status: "queued", CardID: id, ApprovalID: env.ID, Envelope: viewOf(env)})
}

// decisionDismissRequest is POST /v1/decisions/{id}/dismiss's body (all
// optional): why the CEO rejected or dismissed the card.
type decisionDismissRequest struct {
	Reason string `json:"reason"`
}

// decisionDismissResponse is POST /v1/decisions/{id}/dismiss's answer.
type decisionDismissResponse struct {
	CardID    string    `json:"card_id"`
	Status    string    `json:"status"` // always "dismissed"
	Reason    string    `json:"reason"`
	DecidedAt time.Time `json:"decided_at"`
}

// maxDismissReason bounds a dismissal's free-text reason.
const maxDismissReason = 1000

// handleDismissDecision records the CEO rejecting/dismissing a decision
// card (store.CardState, status "dismissed"). Cards are rebuilt fresh on
// every request and never persisted, so this row is the only memory of it:
// GET /v1/decisions and the "needs you" list both leave dismissed cards
// out. Dismissing never touches any approval envelope — one already staged
// from this card stays pending until it is answered or expires. The card
// must be currently open (404 otherwise), so an arbitrary id can't be
// written into the table.
func (d *Daemon) handleDismissDecision(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body decisionDismissRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkspaceBody)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if len(reason) > maxDismissReason {
		http.Error(w, "reason too long (max 1000 bytes)", http.StatusBadRequest)
		return
	}
	card, err := d.findOpenCard(r.Context(), id)
	if err != nil {
		cardLookupError(w, id, err)
		return
	}
	if err := d.linkDecision(r.Context(), card); err != nil {
		http.Error(w, "linking decision card "+id+" failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	cs := store.CardState{CardID: id, Status: "dismissed", Reason: reason, DecidedAt: time.Now().UTC()}
	if err := d.cfg.Store.SetCardState(r.Context(), cs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, decisionDismissResponse{CardID: id, Status: cs.Status, Reason: reason, DecidedAt: cs.DecidedAt})
}
