// This file implements docs/slices/UI.md Phase 5c's Ideas endpoints: GET
// /v1/ideas, POST /v1/ideas (capture -- a POST body, never a query string),
// POST /v1/ideas/{id}/research (queue a research run) and POST
// /v1/ideas/{id}/propose (create a draft from the idea's own title/gist).
//
// THE INVARIANT (see workspace_detail.go's own top-of-file doc comment):
// no workspace control may ever create a decision. This file never imports
// water/internal/decisions and never calls store.SetCardState/
// SetCardActionState/SetDecisionClassification/UpsertDecisionRecord.
// handleProposeIdeaDraft writes only a drafts row (store.CreateDraft, the
// same posture as workspace_detail.go's own handleCreateWorkspaceDraft);
// handleStartIdeaResearch writes only a research_runs row and queues it on
// the runner (research_runner.go), which is itself scoped to
// research_runs/research_steps plus one sanctioned card_evidence_extra
// write in the Attach path (research_runs.go's handleAttachResearchRun) --
// nothing here ever reaches a decision table.
// TestIdeasAndResearchRoutesNeverWriteADecision (research_runner_test.go)
// proves this at the route level; TestIdeasFileNeverReferencesDecisionWrites
// (same file) proves it by scanning this file's own AST.
package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"water/internal/store"
)

// Idea capture limits (docs/slices/UI.md Phase 5c): generous enough for a
// real idea, small enough to keep the capture bar a one-line/one-paragraph
// affair, matching maxThreadTitle/maxThreadText's own sizing convention in
// this package.
const (
	maxIdeaTitle = 200
	maxIdeaGist  = 4000
)

// ideaView is a store.Idea as the API returns it.
type ideaView struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Gist      string    `json:"gist"`
	Stage     string    `json:"stage"`
	CreatedAt time.Time `json:"created_at"`
}

func ideaViewOf(idea store.Idea) ideaView {
	return ideaView{ID: idea.ID, Title: idea.Title, Gist: idea.Gist, Stage: idea.Stage, CreatedAt: idea.CreatedAt}
}

// handleListIdeas serves GET /v1/ideas: every idea, oldest first
// (store.ListIdeas's own order). The client (view_workspaces.js) groups
// these into Raw/Explored itself from each idea's own stage field, rather
// than this endpoint returning two separate lists -- the same "one list,
// client groups it" shape GET /v1/approvals's status field already uses.
func (d *Daemon) handleListIdeas(w http.ResponseWriter, r *http.Request) {
	ideas, err := d.cfg.Store.ListIdeas(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]ideaView, 0, len(ideas))
	for _, idea := range ideas {
		out = append(out, ideaViewOf(idea))
	}
	writeJSON(w, http.StatusOK, out)
}

// ideaCaptureRequest is POST /v1/ideas' body. docs/slices/UI.md Phase 5c is
// explicit that the capture bar posts a body, never a query string (so an
// idea's text never lands in a server access log via a URL) --
// handleCreateIdea never inspects the request's query string for
// title/gist, only the decoded JSON body below.
type ideaCaptureRequest struct {
	Title string `json:"title"`
	Gist  string `json:"gist"`
}

// handleCreateIdea serves POST /v1/ideas: the capture bar. A new idea
// always starts at stage "raw" (docs/slices/UI.md Phase 1d's own two-value
// stage enum; "Explored" is reached later, not at capture time -- this
// phase adds no endpoint that moves an idea's stage, since the plan names
// none). Title is required; gist is optional.
func (d *Daemon) handleCreateIdea(w http.ResponseWriter, r *http.Request) {
	var body ideaCaptureRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkspaceBody)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}
	if len(title) > maxIdeaTitle {
		http.Error(w, "title too long (max 200 bytes)", http.StatusBadRequest)
		return
	}
	gist := strings.TrimSpace(body.Gist)
	if len(gist) > maxIdeaGist {
		http.Error(w, "gist too long (max 4000 bytes)", http.StatusBadRequest)
		return
	}
	idea, err := d.cfg.Store.CreateIdea(r.Context(), store.Idea{Title: title, Gist: gist, Stage: "raw"})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, ideaViewOf(idea))
}

func ideaLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "no such idea", http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// handleStartIdeaResearch serves POST /v1/ideas/{id}/research ("Start
// research"): creates a research_runs row at status "queued" (its own five
// fixed steps are seeded as "pending" so a client polling GET
// /v1/research/runs/{id}/... right away sees "0 of 5" rather than an empty
// list) and hands the run to the background runner's FIFO queue
// (research_runner.go's queueResearchRun). It never itself calls
// research.web or the model: queueing is a plain store write plus an
// in-memory enqueue, gated only by the idea existing.
func (d *Daemon) handleStartIdeaResearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	idea, err := d.cfg.Store.GetIdea(ctx, r.PathValue("id"))
	if err != nil {
		ideaLookupError(w, err)
		return
	}
	run, err := d.cfg.Store.CreateResearchRun(ctx, store.ResearchRun{IdeaID: idea.ID, Topic: idea.Title, Status: "queued"})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for i, facet := range researchFacets {
		_ = d.cfg.Store.UpsertResearchStep(ctx, store.ResearchStep{RunID: run.ID, N: i + 1, Label: facet, Status: "pending"})
	}
	d.queueResearchRun(run.ID)
	writeJSON(w, http.StatusOK, researchRunViewOf(run))
}

// ideaProposalBody builds the code-built subject/body a "Propose" draft
// carries (docs/slices/UI.md Phase 5c: "code-built body from the idea's own
// title/gist, never a model call"), mirroring
// workspace_detail.go's own workspaceTeamMessageBody/workspacePulseCheckBody
// in that it is a pure function of already-known, already-stored fields.
func ideaProposalBody(idea store.Idea) (subject, body string) {
	subject = "Proposal: " + idea.Title
	if idea.Gist == "" {
		return subject, idea.Title + "\n\n[Add your proposal here.]\n"
	}
	return subject, idea.Title + "\n\n" + idea.Gist + "\n\n[Add your proposal here.]\n"
}

// handleProposeIdeaDraft serves POST /v1/ideas/{id}/propose ("Propose"):
// creates a drafts row (store.CreateDraft, template "idea_proposal",
// migration 0024) pre-filled from the idea's own title/gist -- never a
// model call, never an approval envelope, never a decision (THE INVARIANT
// above), exactly like every other draft-creating control in this slice.
func (d *Daemon) handleProposeIdeaDraft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	idea, err := d.cfg.Store.GetIdea(ctx, r.PathValue("id"))
	if err != nil {
		ideaLookupError(w, err)
		return
	}
	subject, body := ideaProposalBody(idea)
	dr, err := d.cfg.Store.CreateDraft(ctx, store.Draft{Template: "idea_proposal", Subject: subject, Body: body})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, draftViewOf(dr))
}
