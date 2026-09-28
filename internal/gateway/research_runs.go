// This file implements docs/slices/UI.md Phase 5c's Research endpoints: GET
// /v1/research/runs, GET /v1/research/runs/{id} and POST
// /v1/research/runs/{id}/attach. The runner itself (the FIFO queue, the
// background worker, the five gated research.web steps) lives in
// research_runner.go; this file is the read/attach surface over the same
// store.ResearchRun/store.ResearchStep rows.
//
// THE INVARIANT (see ideas.go/workspace_detail.go's own top-of-file doc
// comments): no workspace control may ever create a decision. Attach below
// is the one deliberate, narrowly-scoped exception to "never write a
// decision-adjacent row" -- see its own doc comment.
package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"water/internal/store"
)

// researchRunView is a store.ResearchRun as GET /v1/research/runs(/{id})
// returns it, without its report text or steps (the list view -- a client
// wanting the report/steps calls GET /v1/research/runs/{id}, which embeds
// this same shape in researchRunDetailView below). AttachedCardID is
// omitted when unset, so the client's own "In <decision>" vs. "Attach"
// button logic is a simple presence check, matching draftView's own
// omitempty convention for SourceCardID.
type researchRunView struct {
	ID             string     `json:"id"`
	IdeaID         string     `json:"idea_id"`
	Topic          string     `json:"topic"`
	Status         string     `json:"status"`
	AttachedCardID string     `json:"attached_card_id,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}

func researchRunViewOf(r store.ResearchRun) researchRunView {
	v := researchRunView{ID: r.ID, IdeaID: r.IdeaID, Topic: r.Topic, Status: r.Status, AttachedCardID: r.AttachedCardID, CreatedAt: r.CreatedAt}
	if !r.FinishedAt.IsZero() {
		t := r.FinishedAt
		v.FinishedAt = &t
	}
	return v
}

// researchStepView is one store.ResearchStep as the API returns it.
type researchStepView struct {
	N           int    `json:"n"`
	Label       string `json:"label"`
	Status      string `json:"status"`
	SourceCount int    `json:"source_count,omitempty"`
}

func researchStepViewOf(s store.ResearchStep) researchStepView {
	return researchStepView{N: s.N, Label: s.Label, Status: s.Status, SourceCount: s.SourceCount}
}

// researchRunDetailView is GET /v1/research/runs/{id}'s body: the run plus
// its ordered steps (so a client polling sees "3 of 5" as a real count of
// steps whose Status is "done") and the finished report's text.
// ReportUntrusted mirrors ResearchRun.Untrusted, which store.ResearchRun's
// own doc comment pins as always true for every row in this table -- render
// ReportText as plain text only, never as markup, exactly like every other
// untrusted field this API already returns (meetingView.RecapText,
// threadView.AnchorContext with AnchorUntrusted set).
type researchRunDetailView struct {
	researchRunView
	Steps           []researchStepView `json:"steps"`
	ReportText      string             `json:"report_text,omitempty"`
	ReportUntrusted bool               `json:"report_untrusted"`
}

// handleListResearchRuns serves GET /v1/research/runs: every run, oldest
// first (store.ListResearchRuns's own order). The client (view_workspaces.js)
// buckets these into the Queued/Running/Finished columns itself from each
// run's own status field, the same "one list, client groups it" shape
// handleListIdeas uses for Raw/Explored.
func (d *Daemon) handleListResearchRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := d.cfg.Store.ListResearchRuns(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]researchRunView, 0, len(runs))
	for _, run := range runs {
		out = append(out, researchRunViewOf(run))
	}
	writeJSON(w, http.StatusOK, out)
}

func researchRunLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "no such research run", http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// handleGetResearchRun serves GET /v1/research/runs/{id}: the run plus its
// steps in step order.
func (d *Daemon) handleGetResearchRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	run, err := d.cfg.Store.GetResearchRun(ctx, r.PathValue("id"))
	if err != nil {
		researchRunLookupError(w, err)
		return
	}
	steps, err := d.cfg.Store.ListResearchSteps(ctx, run.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].N < steps[j].N })
	out := researchRunDetailView{researchRunView: researchRunViewOf(run), Steps: make([]researchStepView, 0, len(steps)), ReportText: run.ReportText, ReportUntrusted: run.Untrusted}
	for _, s := range steps {
		out.Steps = append(out.Steps, researchStepViewOf(s))
	}
	writeJSON(w, http.StatusOK, out)
}

// researchAttachRequest is POST /v1/research/runs/{id}/attach's body.
type researchAttachRequest struct {
	CardID string `json:"card_id"`
}

// handleAttachResearchRun serves POST /v1/research/runs/{id}/attach
// (docs/slices/UI.md Phase 5c): "a finished run shows 'In <decision>' ... or
// Attach, which inserts only into card_evidence_extra."
//
// THIS IS THE ONE SANCTIONED EXCEPTION to THE INVARIANT stated at the top of
// this file, ideas.go and workspace_detail.go ("no workspace control may
// ever create a decision"). It is deliberately narrow and isolated in this
// one small function:
//   - it writes exactly one row to card_evidence_extra
//     (store.AddCardEvidenceExtra, Phase 1c's own "storage only" table),
//     always with Untrusted: true (a research report's text is always
//     untrusted -- store.ResearchRun.Untrusted's own doc comment, mirrored
//     here rather than trusted just because a human clicked a button);
//   - it records the association on the run itself
//     (store.AttachResearchRunCard, Phase 1d's own accessor, whose doc
//     comment already scopes it to exactly this future action);
//   - it does not read, write or touch card_states, card_action_states,
//     decision_classifications or decision_records in any way, and it does
//     not import water/internal/decisions.
//
// A card id is accepted as given (a free-text reference the CEO already has
// open elsewhere, e.g. a decisions.Card.ID) and is not validated against a
// live card lookup: validating it would mean importing internal/decisions
// or calling decisions.Merge from this file, which is exactly the coupling
// THE INVARIANT exists to prevent. An unknown card id simply produces
// evidence that nothing currently reads (card_evidence_extra rows are
// looked up by card id when decisions.Merge assembles a card that DOES
// exist -- an orphaned row for a card id that never exists is inert, not
// unsafe). The run must be "finished": attaching a queued or still-running
// run's (empty) report is refused, and attaching a "failed" run's partial
// report is refused too -- there is nothing meaningful to attach yet.
func (d *Daemon) handleAttachResearchRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	run, err := d.cfg.Store.GetResearchRun(ctx, r.PathValue("id"))
	if err != nil {
		researchRunLookupError(w, err)
		return
	}
	if run.Status != "finished" {
		http.Error(w, "research run is not finished yet", http.StatusBadRequest)
		return
	}
	var body researchAttachRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkspaceBody)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	cardID := strings.TrimSpace(body.CardID)
	if cardID == "" {
		http.Error(w, "card_id is required", http.StatusBadRequest)
		return
	}
	if _, err := d.cfg.Store.AddCardEvidenceExtra(ctx, store.CardEvidenceExtra{
		CardID: cardID, Source: "research:" + run.ID, Text: run.ReportText, Untrusted: true,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := d.cfg.Store.AttachResearchRunCard(ctx, run.ID, cardID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	run.AttachedCardID = cardID
	writeJSON(w, http.StatusOK, researchRunViewOf(run))
}
