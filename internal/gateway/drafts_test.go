package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"water/internal/store"
)

// TestDraftsListAndGetHTTPShape: GET /v1/drafts lists most-recently-updated
// first (store.ListDrafts's own order) with the code-built template label,
// and GET /v1/drafts/{id} returns the same shape for one row.
func TestDraftsListAndGetHTTPShape(t *testing.T) {
	h, _ := newDraftHarness(t)
	ctx := context.Background()
	older, err := h.st.CreateDraft(ctx, store.Draft{Template: "reply", To: "a@x.com", Subject: "s1", Body: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	newer, err := h.st.CreateDraft(ctx, store.Draft{Template: "investor_update", To: "b@x.com", Subject: "s2", Body: "b2", SourceCardID: "card-1"})
	if err != nil {
		t.Fatal(err)
	}

	var list []map[string]any
	resp := h.get(t, "/v1/drafts", h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0]["id"] != newer.ID || list[1]["id"] != older.ID {
		t.Fatalf("list = %v, want [%s, %s]", list, newer.ID, older.ID)
	}
	if list[0]["template_label"] != "Investor update section" || list[0]["source_card_id"] != "card-1" {
		t.Fatalf("newer draft = %v", list[0])
	}
	if list[1]["template_label"] != "Reply" {
		t.Fatalf("older draft = %v", list[1])
	}

	one := h.get(t, "/v1/drafts/"+older.ID, h.token)
	defer one.Body.Close()
	var got map[string]any
	if err := json.NewDecoder(one.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != older.ID || got["to"] != "a@x.com" || got["subject"] != "s1" || got["body"] != "b1" {
		t.Fatalf("get = %v", got)
	}

	missing := h.get(t, "/v1/drafts/draft_missing", h.token)
	defer missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown id status = %d, want 404", missing.StatusCode)
	}
}

// TestSaveDraftRoundTripsThroughHTTP: saving then getting returns exactly
// what was saved, and creates no envelope.
func TestSaveDraftRoundTripsThroughHTTP(t *testing.T) {
	h, _ := newDraftHarness(t)
	d, err := h.st.CreateDraft(context.Background(), store.Draft{Template: "delegation", To: "old@x.com", Subject: "old", Body: "old body"})
	if err != nil {
		t.Fatal(err)
	}
	resp := h.post(t, "/v1/drafts/"+d.ID, `{"to":"new@x.com","subject":"new subject","body":"new body"}`, h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status = %d", resp.StatusCode)
	}
	var saved map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&saved); err != nil {
		t.Fatal(err)
	}
	if saved["to"] != "new@x.com" || saved["subject"] != "new subject" || saved["body"] != "new body" {
		t.Fatalf("save response = %v", saved)
	}

	get := h.get(t, "/v1/drafts/"+d.ID, h.token)
	defer get.Body.Close()
	var got map[string]any
	if err := json.NewDecoder(get.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["to"] != "new@x.com" || got["subject"] != "new subject" || got["body"] != "new body" {
		t.Fatalf("get after save = %v, want exactly what was saved", got)
	}
	if pending, _ := h.q.Pending(context.Background()); len(pending) != 0 {
		t.Fatalf("Save created %d envelopes, want 0", len(pending))
	}
}

func TestSaveDraftUnknownIDIs404(t *testing.T) {
	h, _ := newDraftHarness(t)
	resp := h.post(t, "/v1/drafts/draft_missing", `{"to":"a@x.com","subject":"s","body":"b"}`, h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestSubmitDraftCreatesExactlyOnePendingEnvelopeFromCurrentEditorState is
// the central Phase 3c invariant: submit proposes exactly the request
// body's to/subject/body -- never the drafts row's own (possibly stale)
// stored values -- carries SourceCardID onto the envelope, creates exactly
// one pending envelope, executes nothing (the fake gmail connector's
// Invoke is never called), and leaves the draft row completely unchanged.
func TestSubmitDraftCreatesExactlyOnePendingEnvelopeFromCurrentEditorState(t *testing.T) {
	h, gm := newDraftHarness(t)
	ctx := context.Background()
	d, err := h.st.CreateDraft(ctx, store.Draft{Template: "reply", To: "stale@x.com", Subject: "stale subject", Body: "stale body", SourceCardID: "card-42"})
	if err != nil {
		t.Fatal(err)
	}

	// The editor has unsaved edits: submit's body differs from the stored
	// row on every field.
	resp := h.post(t, "/v1/drafts/"+d.ID+"/submit", `{"to":"fresh@x.com","subject":"fresh subject","body":"fresh body"}`, h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		Status     string `json:"status"`
		DraftID    string `json:"draft_id"`
		ApprovalID string `json:"approval_id"`
		Envelope   struct {
			ID           string         `json:"id"`
			Payload      map[string]any `json:"payload"`
			SourceCardID string         `json:"source_card_id"`
			Status       string         `json:"status"`
		} `json:"envelope"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "queued" || out.DraftID != d.ID || out.ApprovalID != out.Envelope.ID {
		t.Fatalf("submit response = %+v", out)
	}
	if out.Envelope.SourceCardID != "card-42" {
		t.Fatalf("envelope source_card_id = %q, want card-42", out.Envelope.SourceCardID)
	}
	toList, _ := out.Envelope.Payload["to"].([]any)
	if len(toList) != 1 || toList[0] != "fresh@x.com" || out.Envelope.Payload["subject"] != "fresh subject" || out.Envelope.Payload["body"] != "fresh body" {
		t.Fatalf("envelope payload = %v, want the request body's fresh values, not the stale stored ones", out.Envelope.Payload)
	}

	pending, err := h.q.Pending(ctx)
	if err != nil || len(pending) != 1 || pending[0].ID != out.Envelope.ID {
		t.Fatalf("pending = %+v, %v; want exactly the one proposed envelope", pending, err)
	}

	// Submit never executes: the fake gmail connector's Invoke was never
	// called (Propose only ever queues a PENDING envelope).
	if gm.drafts != 0 {
		t.Fatalf("gm.drafts = %d, want 0 (submit must never invoke a connector)", gm.drafts)
	}

	// The draft row is completely unchanged: submit never writes it.
	stored, err := h.st.GetDraft(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.To != "stale@x.com" || stored.Subject != "stale subject" || stored.Body != "stale body" {
		t.Fatalf("stored draft = %+v, want unchanged by submit", stored)
	}
}

// TestSubmitDraftRequiresAllThreeFields: an empty to/subject/body is a 400,
// before anything is proposed.
func TestSubmitDraftRequiresAllThreeFields(t *testing.T) {
	h, _ := newDraftHarness(t)
	d, err := h.st.CreateDraft(context.Background(), store.Draft{Template: "reply", To: "a@x.com", Subject: "s", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"to":"","subject":"s","body":"b"}`,
		`{"to":"a@x.com","subject":"","body":"b"}`,
		`{"to":"a@x.com","subject":"s","body":""}`,
		`{}`,
	} {
		resp := h.post(t, "/v1/drafts/"+d.ID+"/submit", body, h.token)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, resp.StatusCode)
		}
	}
	if pending, _ := h.q.Pending(context.Background()); len(pending) != 0 {
		t.Fatalf("pending = %d, want 0", len(pending))
	}
}

func TestSubmitDraftUnknownIDIs404(t *testing.T) {
	h, _ := newDraftHarness(t)
	resp := h.post(t, "/v1/drafts/draft_missing/submit", `{"to":"a@x.com","subject":"s","body":"b"}`, h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestSubmitDraftRecipientRefusalIs422: a near-miss recipient (the same
// spoken-email confirm-the-address case Slice W refuses everywhere else,
// docs/slices/W.md D4c) is refused with exactly 422, creates no envelope,
// invokes nothing, and leaves the draft row unchanged.
func TestSubmitDraftRecipientRefusalIs422(t *testing.T) {
	h, gm := newDraftHarness(t)
	ctx := context.Background()
	d, err := h.st.CreateDraft(ctx, store.Draft{Template: "reply", To: "old@x.com", Subject: "old", Body: "old body"})
	if err != nil {
		t.Fatal(err)
	}
	resp := h.post(t, "/v1/drafts/"+d.ID+"/submit", `{"to":"kranthetjob@therightgmail.com","subject":"Job search","body":"Hi"}`, h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if pending, _ := h.q.Pending(ctx); len(pending) != 0 {
		t.Fatalf("pending = %d, want 0 (refused, nothing queued)", len(pending))
	}
	if gm.drafts != 0 {
		t.Fatalf("gm.drafts = %d, want 0", gm.drafts)
	}
	stored, err := h.st.GetDraft(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.To != "old@x.com" || stored.Subject != "old" || stored.Body != "old body" {
		t.Fatalf("stored draft = %+v, want unchanged by the refused submit", stored)
	}
}
