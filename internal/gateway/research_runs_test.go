package gateway

import (
	"net/http"
	"reflect"
	"testing"

	"water/internal/store"
)

// seedFinishedRun creates a finished research run directly at the store
// layer (bypassing the runner: these tests are about the read/attach
// surface, not the runner itself -- research_runner_test.go covers that).
func seedFinishedRun(t *testing.T, h *harness, ideaID, reportText string) store.ResearchRun {
	t.Helper()
	ctx := t.Context()
	run, err := h.st.CreateResearchRun(ctx, store.ResearchRun{IdeaID: ideaID, Topic: "Faster onboarding", Status: "queued"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetResearchRunReport(ctx, run.ID, reportText); err != nil {
		t.Fatal(err)
	}
	if err := h.st.UpdateResearchRunStatus(ctx, run.ID, "finished", h.d.computeNow()); err != nil {
		t.Fatal(err)
	}
	run, err = h.st.GetResearchRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func mustCreateIdea(t *testing.T, h *harness, title string) store.Idea {
	t.Helper()
	idea, err := h.st.CreateIdea(t.Context(), store.Idea{Title: title, Stage: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	return idea
}

func TestListResearchRunsReturnsEveryRun(t *testing.T) {
	h := newHarness(t)
	idea := mustCreateIdea(t, h, "Faster onboarding")
	run := seedFinishedRun(t, h, idea.ID, "## Overview\nSome report text.\n")

	resp := do(t, h.srv.URL, "GET", "/v1/research/runs", "", h.token)
	var out []map[string]any
	decodeInto(t, resp, http.StatusOK, &out)
	if len(out) != 1 || out[0]["id"] != run.ID || out[0]["status"] != "finished" {
		t.Fatalf("got = %+v", out)
	}
}

func TestGetResearchRunIncludesSteps(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	idea := mustCreateIdea(t, h, "Faster onboarding")
	run := seedFinishedRun(t, h, idea.ID, "## Overview\nSome report text.\n")
	for i, facet := range researchFacets {
		if err := h.st.UpsertResearchStep(ctx, store.ResearchStep{RunID: run.ID, N: i + 1, Label: facet, Status: "done", SourceCount: 2}); err != nil {
			t.Fatal(err)
		}
	}

	resp := do(t, h.srv.URL, "GET", "/v1/research/runs/"+run.ID, "", h.token)
	var out map[string]any
	decodeInto(t, resp, http.StatusOK, &out)
	if out["report_text"] != "## Overview\nSome report text.\n" {
		t.Fatalf("report_text = %v", out["report_text"])
	}
	if out["report_untrusted"] != true {
		t.Fatalf("report_untrusted = %v, want true (a research report is always untrusted)", out["report_untrusted"])
	}
	steps, ok := out["steps"].([]any)
	if !ok || len(steps) != 5 {
		t.Fatalf("steps = %+v, want 5", out["steps"])
	}
}

func TestGetResearchRunUnknownIs404(t *testing.T) {
	h := newHarness(t)
	if got := statusOf(do(t, h.srv.URL, "GET", "/v1/research/runs/run_does_not_exist", "", h.token)); got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
}

// TestAttachResearchRunOnlyTouchesCardEvidenceExtra is docs/slices/UI.md
// Phase 5c's own named acceptance item: "Attach touches only
// card_evidence_extra, and the other card fields are byte-identical."
//
// A card here is a persisted store.DecisionRecord (docs/slices/UI.md Phase
// 1c, U13): the full serialized decisions.Card a caller would otherwise
// build. This test seeds one directly at the store layer (no decisions.Card
// construction needed -- store.DecisionRecord's CardJSON is opaque to this
// package, exactly as its own doc comment says), snapshots every one of its
// fields, calls Attach, and asserts the row that comes back out of
// GetDecisionRecord is byte-for-byte identical (reflect.DeepEqual on the
// whole struct, which includes CardJSON verbatim) -- while confirming
// card_evidence_extra gained exactly the one new row Attach is allowed to
// write, and that card_states/card_action_states/decision_classifications/
// decision_records (row counts) never moved at all.
func TestAttachResearchRunOnlyTouchesCardEvidenceExtra(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	idea := mustCreateIdea(t, h, "Faster onboarding")
	reportText := "## Overview\nOnboarding takes 5 steps today.\nSources:\n- Example (https://example.com/a)\n"
	run := seedFinishedRun(t, h, idea.ID, reportText)

	const cardID = "card-attach-1"
	before := store.DecisionRecord{
		CardID:     cardID,
		CardJSON:   `{"id":"card-attach-1","lead":"Renew Meridian?","question":"Should we renew?","untrusted":false}`,
		Provenance: "demo_seed",
		Simulated:  true,
	}
	if err := h.st.UpsertDecisionRecord(ctx, before); err != nil {
		t.Fatal(err)
	}
	beforeRecord, err := h.st.GetDecisionRecord(ctx, cardID)
	if err != nil {
		t.Fatal(err)
	}
	beforeEvidence, err := h.st.ListCardEvidenceExtra(ctx, cardID)
	if err != nil {
		t.Fatal(err)
	}
	if len(beforeEvidence) != 0 {
		t.Fatalf("unexpected pre-existing evidence: %+v", beforeEvidence)
	}
	dbPath := h.dir + "/water.db"
	tableCountsBefore := wsDecisionTableCounts(t, dbPath)

	resp := do(t, h.srv.URL, "POST", "/v1/research/runs/"+run.ID+"/attach", `{"card_id":"`+cardID+`"}`, h.token)
	var attached map[string]any
	decodeInto(t, resp, http.StatusOK, &attached)
	if attached["attached_card_id"] != cardID {
		t.Fatalf("attached_card_id = %v, want %q", attached["attached_card_id"], cardID)
	}

	// research_runs.attached_card_id is the one field Attach may change on
	// the run itself.
	gotRun, err := h.st.GetResearchRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotRun.AttachedCardID != cardID {
		t.Fatalf("stored run's attached_card_id = %q, want %q", gotRun.AttachedCardID, cardID)
	}

	// The card's own persisted record is byte-for-byte unchanged.
	afterRecord, err := h.st.GetDecisionRecord(ctx, cardID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeRecord, afterRecord) {
		t.Fatalf("decision record changed by Attach:\nbefore = %+v\nafter  = %+v", beforeRecord, afterRecord)
	}

	// Exactly one new card_evidence_extra row, carrying the run's own
	// (always-untrusted) report text.
	afterEvidence, err := h.st.ListCardEvidenceExtra(ctx, cardID)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterEvidence) != 1 {
		t.Fatalf("card_evidence_extra rows = %d, want 1", len(afterEvidence))
	}
	if !afterEvidence[0].Untrusted {
		t.Fatal("the attached evidence must be marked untrusted")
	}
	if afterEvidence[0].Text != reportText {
		t.Fatalf("attached evidence text = %q, want the run's report text %q", afterEvidence[0].Text, reportText)
	}

	// No decision-adjacent table's row count moved (THE INVARIANT).
	tableCountsAfter := wsDecisionTableCounts(t, dbPath)
	for table, n := range tableCountsBefore {
		if tableCountsAfter[table] != n {
			t.Errorf("%s: rows went from %d to %d -- Attach must never write here", table, n, tableCountsAfter[table])
		}
	}
}

func TestAttachResearchRunRefusesUnfinishedRun(t *testing.T) {
	h := newHarness(t)
	idea := mustCreateIdea(t, h, "Faster onboarding")
	run, err := h.st.CreateResearchRun(t.Context(), store.ResearchRun{IdeaID: idea.ID, Topic: idea.Title, Status: "queued"})
	if err != nil {
		t.Fatal(err)
	}
	if got := statusOf(do(t, h.srv.URL, "POST", "/v1/research/runs/"+run.ID+"/attach", `{"card_id":"card-x"}`, h.token)); got != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (run is not finished)", got)
	}
}

func TestAttachResearchRunRequiresACardID(t *testing.T) {
	h := newHarness(t)
	idea := mustCreateIdea(t, h, "Faster onboarding")
	run := seedFinishedRun(t, h, idea.ID, "report")
	if got := statusOf(do(t, h.srv.URL, "POST", "/v1/research/runs/"+run.ID+"/attach", `{}`, h.token)); got != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", got)
	}
}
