package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"water/internal/approvals"
	"water/internal/decisions"
	"water/internal/gate"
	"water/internal/twins"
)

// handleListDecisions runs the classification-trigger orchestration over
// today's candidate items, merges in any persisted decision_records and
// card_evidence_extra rows (decisions.Merge, U13 -- see mergeWithRecords),
// and returns every resulting card, ranked by severity then deadline
// (decisions.Rank). A nil Decisions (no registry configured) still shows
// persisted records: a demo-seeded or pinned card doesn't depend on the
// live classifier being configured at all.
func (d *Daemon) handleListDecisions(w http.ResponseWriter, r *http.Request) {
	var computed []*decisions.Card
	if d.cfg.Decisions != nil {
		var err error
		computed, err = d.cfg.Decisions.Run(r.Context(), time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	cards, err := d.mergeWithRecords(r.Context(), computed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// A card the CEO dismissed (POST /v1/decisions/{id}/dismiss) is left
	// out, exactly as the "needs you" list already leaves it out
	// (needsyou.Compute): cards are rebuilt on every request, so without
	// this a dismissed card would simply come back.
	dismissed, err := d.cfg.Store.DismissedCardIDs(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	open := cards[:0:0]
	for _, c := range cards {
		if c != nil && !dismissed[c.ID] {
			open = append(open, c)
		}
	}
	ranked := decisions.Rank(open)
	staged, err := d.stagedCardViews(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// No cards is always `[]` on the wire, never `null`: a typed client
	// decoding an array (Swift's JSONDecoder) rejects null.
	out := make([]decisionCardView, 0, len(ranked))
	for _, c := range ranked {
		v := decisionCardView{Card: c}
		if cs, ok := staged[c.ID]; ok {
			v.CardState = &cs
		}
		actionStates, err := d.cardActionStateViews(r.Context(), c.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		v.ActionStates = actionStates
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// decisionCardView is one card as GET /v1/decisions returns it: the card's
// own fields, unchanged (Go names, as before), plus card_state when the
// card was staged (V-ui2, docs/slices/V.md D3), so a client can show
// "staged, awaiting your yes" instead of offering to stage it again.
// Dismissed cards never appear, so card_state is only ever "staged".
type decisionCardView struct {
	*decisions.Card
	CardState *cardStateView `json:"card_state,omitempty"`
	// ActionStates is Phase 3b's per-suggestion sibling of CardState, keyed
	// by decisions.Suggestion.ID: it lets the Decisions view show a
	// suggestion row as already staged (and load its read-back) without
	// the CEO clicking Review again, exactly as CardState already does for
	// the whole, function-keyed card. Two entries on the same card are
	// entirely independent (store.CardActionStates, docs/slices/UI.md
	// Phase 1c): staging one never touches the other's row.
	ActionStates map[string]cardActionStateView `json:"action_states,omitempty"`
}

// cardStateView is a staged card's record: the envelope it waits on and
// that envelope's status now (pending until answered; an edit re-points the
// card at the new envelope, see handleEditApproval). approval_status is
// empty when the envelope can't be read.
type cardStateView struct {
	Status         string    `json:"status"`
	ApprovalID     string    `json:"approval_id"`
	ApprovalStatus string    `json:"approval_status,omitempty"`
	StagedAt       time.Time `json:"staged_at"`
}

// stagedCardViews reads every staged card's state and its envelope's
// current status (stale envelopes are expired first, as the approvals list
// does, so a lapsed one never reads as pending).
func (d *Daemon) stagedCardViews(ctx context.Context) (map[string]cardStateView, error) {
	staged, err := d.cfg.Store.StagedCardStates(ctx)
	if err != nil || len(staged) == 0 {
		return nil, err
	}
	if err := d.cfg.Approvals.ExpireStale(ctx); err != nil {
		return nil, err
	}
	out := make(map[string]cardStateView, len(staged))
	for id, cs := range staged {
		v := cardStateView{Status: cs.Status, ApprovalID: cs.ApprovalID, StagedAt: cs.DecidedAt}
		if cs.ApprovalID != "" {
			if env, err := d.cfg.Approvals.Get(ctx, cs.ApprovalID); err == nil {
				v.ApprovalStatus = string(env.Status)
			}
		}
		out[id] = v
	}
	return out, nil
}

// handleEmailDecisionReport renders one open decision card as a
// self-contained HTML report (internal/reports, via Card.HTMLReport) and
// stages it as a level-A gmail.send_message approval with the report as
// html_attachment: the concrete, reachable-end-to-end wiring the write
// increment needed for internal/reports (see docs/EVOLUTION_PLAN.md's dated
// entry). This proposes an envelope exactly the way handleToolInvoke does
// for any other A-level model call; nothing here executes anything by
// itself, and the normal approval flow still governs whether the mail
// actually goes out.
//
// The approval is proposed only when the manifest grants gmail.send_message
// (present, not level B) and the connector is registered — otherwise the
// CEO would be asked to approve something the gate can never run (403 and
// 404 respectively). The queued approval is returned in the response only;
// it is not announced on any open turn stream (see handleTurn).
func (d *Daemon) handleEmailDecisionReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		Body    string   `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(body.To) == 0 {
		http.Error(w, "to is required", http.StatusBadRequest)
		return
	}
	if f, ok := d.cfg.Manifest.Function("gmail.send_message"); !ok || f.Level == twins.B {
		http.Error(w, "gmail.send_message is not granted by the manifest", http.StatusForbidden)
		return
	}
	if _, _, ok := d.cfg.Registry.Lookup("gmail.send_message"); !ok {
		http.Error(w, "gmail.send_message is not available", http.StatusNotFound)
		return
	}
	if d.cfg.Decisions == nil {
		http.Error(w, "no open decision card "+id, http.StatusNotFound)
		return
	}
	cards, err := d.cfg.Decisions.Run(r.Context(), time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var card *decisions.Card
	for _, c := range cards {
		if c.ID == id {
			card = c
			break
		}
	}
	if card == nil {
		http.Error(w, "no open decision card "+id, http.StatusNotFound)
		return
	}
	subject := body.Subject
	if subject == "" {
		subject = card.Lead
	}
	textBody := body.Body
	if textBody == "" {
		textBody = card.Question
	}
	html, err := card.HTMLReport(subject)
	if err != nil {
		http.Error(w, "rendering report: "+err.Error(), http.StatusInternalServerError)
		return
	}
	payload := map[string]any{"to": body.To, "subject": subject, "body": textBody, "html_attachment": html}
	env, err := d.cfg.Approvals.Propose(r.Context(), approvals.Envelope{
		Action: "gmail.send_message", Payload: payload, Origin: string(gate.P0),
		Risk: string(functionRisk(d.cfg.Registry, "gmail.send_message")),
	})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "denied", "reason": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "queued", "approval_id": env.ID})
}

// cardActionStateView is one staged action's record, the per-action
// analogue of cardStateView: the envelope it waits on and that envelope's
// current status (pending until answered). approval_status is empty when
// the envelope can't be read.
type cardActionStateView struct {
	Status         string `json:"status"`
	ApprovalID     string `json:"approval_id"`
	ApprovalStatus string `json:"approval_status,omitempty"`
}

// cardActionStateViews reads every one of cardID's staged actions
// (store.CardActionStates) and each one's envelope status now (stale
// envelopes are expired first, exactly as stagedCardViews does for the
// single card_states row, so a lapsed one never reads as pending).
func (d *Daemon) cardActionStateViews(ctx context.Context, cardID string) (map[string]cardActionStateView, error) {
	states, err := d.cfg.Store.CardActionStates(ctx, cardID)
	if err != nil || len(states) == 0 {
		return nil, err
	}
	if err := d.cfg.Approvals.ExpireStale(ctx); err != nil {
		return nil, err
	}
	out := make(map[string]cardActionStateView, len(states))
	for _, s := range states {
		v := cardActionStateView{Status: s.Status, ApprovalID: s.ApprovalID}
		if s.ApprovalID != "" {
			if env, err := d.cfg.Approvals.Get(ctx, s.ApprovalID); err == nil {
				v.ApprovalStatus = string(env.Status)
			}
		}
		out[s.ActionID] = v
	}
	return out, nil
}

// mergeWithRecords is decisions.MergeFromStore over this daemon's store
// (docs/slices/UI.md Phase 1c, U13): a persisted decision_records row wins
// over a computed card sharing its id, and card_evidence_extra rows are
// appended to whichever version wins. needsyou.Compute merges the same way
// over the same store, so a persisted record overrides the live
// classifier's card in both places.
func (d *Daemon) mergeWithRecords(ctx context.Context, computed []*decisions.Card) ([]*decisions.Card, error) {
	return decisions.MergeFromStore(ctx, d.cfg.Store, computed)
}
