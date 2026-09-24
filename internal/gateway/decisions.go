package gateway

import (
	"encoding/json"
	"net/http"
	"time"

	"water/internal/approvals"
	"water/internal/decisions"
	"water/internal/gate"
	"water/internal/twins"
)

// handleListDecisions runs the classification-trigger orchestration over
// today's candidate items and returns every card it built, ranked by
// severity then deadline (decisions.Rank). A nil Decisions (no registry
// configured) reports an empty list rather than an error, matching how a
// twin with no decision types simply has nothing to show.
func (d *Daemon) handleListDecisions(w http.ResponseWriter, r *http.Request) {
	if d.cfg.Decisions == nil {
		writeJSON(w, http.StatusOK, []*decisions.Card{})
		return
	}
	cards, err := d.cfg.Decisions.Run(r.Context(), time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ranked := decisions.Rank(cards)
	if ranked == nil {
		// No cards is always `[]` on the wire, never `null`: a typed client
		// decoding an array (Swift's JSONDecoder) rejects null.
		ranked = []*decisions.Card{}
	}
	writeJSON(w, http.StatusOK, ranked)
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
