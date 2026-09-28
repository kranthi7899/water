package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"water/internal/approvals"
	"water/internal/gate"
	"water/internal/store"
)

// TestApprovalEmailPreview_RendersTheFinalBrandedHTML: GET
// /v1/approvals/{id}/preview on a gmail.send_message envelope returns 200
// with the real internal/brand.RenderEmail output for that envelope's own
// body and the real (non-demo) twin's signature -- the header/koi/glass
// cid: references, the signature's own name, and the server-injected CSP
// meta tag are all present. This exercises newCEODraftHarness (defined in
// brand_payload_test.go), the real "ceo" twin id, not a synthetic test
// manifest, since the whole point is proving this against the real,
// committed twins/ceo/brand/signature.yaml.
func TestApprovalEmailPreview_RendersTheFinalBrandedHTML(t *testing.T) {
	h := newCEODraftHarness(t)
	ctx := context.Background()
	d, err := h.st.CreateDraft(ctx, store.Draft{Template: "reply", To: "a@x.com", Subject: "s", Body: "Line one.\n\nLine two <b>not html</b>."})
	if err != nil {
		t.Fatal(err)
	}
	submit := h.post(t, "/v1/drafts/"+d.ID+"/submit", `{"to":"a@x.com","subject":"s","body":"Line one.\n\nLine two <b>not html</b>."}`, h.token)
	defer submit.Body.Close()
	if submit.StatusCode != http.StatusOK {
		t.Fatalf("submit status = %d", submit.StatusCode)
	}
	var out struct {
		Envelope struct {
			ID string `json:"id"`
		} `json:"envelope"`
	}
	if err := json.NewDecoder(submit.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}

	resp := h.get(t, "/v1/approvals/"+out.Envelope.ID+"/preview", h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d", resp.StatusCode)
	}
	var pv struct {
		HTML string `json:"html"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pv); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"cid:water-header", "cid:water-koi", "cid:water-glass",
		"Alex Morgan", // twins/ceo/brand/signature.yaml's seeded name
		"Content-Security-Policy", "img-src 'none'",
		"Line one.", "Line two", "&lt;b&gt;not html&lt;/b&gt;", // escaped, never live markup
	} {
		if !strings.Contains(pv.HTML, want) {
			t.Errorf("preview html missing %q", want)
		}
	}
	if strings.Contains(pv.HTML, "<b>not html</b>") {
		t.Fatalf("preview html contains an unescaped <b> tag from the body -- want it escaped")
	}
}

// TestApprovalEmailPreview_NonMailActionIs404: an envelope whose action
// isn't gmail.send_message has nothing to preview.
func TestApprovalEmailPreview_NonMailActionIs404(t *testing.T) {
	h := newHarness(t)
	env, err := h.q.Propose(context.Background(), approvals.Envelope{
		Action: "notes.save_note", Payload: map[string]any{"text": "hi"}, Origin: string(gate.P0), Risk: "low",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := h.get(t, "/v1/approvals/"+env.ID+"/preview", h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestApprovalEmailPreview_UnknownIDIs404 mirrors handleGetApproval's own
// miss behavior.
func TestApprovalEmailPreview_UnknownIDIs404(t *testing.T) {
	h := newHarness(t)
	resp := h.get(t, "/v1/approvals/env_missing/preview", h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
