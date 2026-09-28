package gateway

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"water/internal/backend"
)

// TestIntentsListEndpoint: with no embedded or learned intents beyond the
// required _shared.yaml, GET /v1/intents reports an empty list rather than
// erroring — the same "no intents directory content" posture LoadRegistry
// itself takes.
func TestIntentsListEndpoint(t *testing.T) {
	h, _ := newIntentsHarness(t, true)
	resp := h.get(t, "/v1/intents", h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	var items []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %+v, want empty", items)
	}
}

// TestIntentsPromoteEndpoint runs the full loop end to end over HTTP: draft
// a candidate, promote the resulting pending file, and confirm the learned
// intent is now live (GET /v1/intents reports it, origin "learned", active).
func TestIntentsPromoteEndpoint(t *testing.T) {
	h, _ := newIntentsHarness(t, true)
	h.fake.Reply = func(req backend.Request) string { return draftReplyYAML }

	now := time.Now()
	insertCandidateFixture(t, h, "d1", now.Add(-48*time.Hour), "quick.next_event", 3)
	insertCandidateFixture(t, h, "d2", now.Add(-24*time.Hour), "quick.next_event", 2)
	candID := firstCandidateID(t, h)

	draftResp := h.post(t, "/v1/intents/draft", `{"candidate_id":"`+candID+`"}`, h.token)
	if draftResp.StatusCode != http.StatusOK {
		t.Fatalf("draft status = %d, body = %s", draftResp.StatusCode, readBody(t, draftResp))
	}
	var draftOut struct {
		ID    string `json:"id"`
		Valid bool   `json:"valid"`
	}
	if err := json.NewDecoder(draftResp.Body).Decode(&draftOut); err != nil {
		t.Fatalf("decode draft: %v", err)
	}
	draftResp.Body.Close()
	if !draftOut.Valid {
		t.Fatalf("draft did not validate")
	}

	promoteResp := h.post(t, "/v1/intents/promote", `{"candidate_id":"`+candID+`"}`, h.token)
	if promoteResp.StatusCode != http.StatusOK {
		t.Fatalf("promote status = %d, body = %s", promoteResp.StatusCode, readBody(t, promoteResp))
	}
	var promoteOut struct {
		ID       string `json:"id"`
		Path     string `json:"path"`
		Reloaded bool   `json:"reloaded"`
	}
	if err := json.NewDecoder(promoteResp.Body).Decode(&promoteOut); err != nil {
		t.Fatalf("decode promote: %v", err)
	}
	promoteResp.Body.Close()
	if promoteOut.ID != draftOut.ID {
		t.Fatalf("promote id = %q, want draft id %q", promoteOut.ID, draftOut.ID)
	}
	if !promoteOut.Reloaded {
		t.Fatalf("promote did not report reloaded=true")
	}

	listResp := h.get(t, "/v1/intents", h.token)
	defer listResp.Body.Close()
	var items []struct {
		ID     string `json:"id"`
		Origin string `json:"origin"`
		Active bool   `json:"active"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&items); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	found := false
	for _, it := range items {
		if it.ID == promoteOut.ID {
			found = true
			if it.Origin != "learned" {
				t.Errorf("origin = %q, want learned", it.Origin)
			}
			if !it.Active {
				t.Errorf("active = false, want true")
			}
		}
	}
	if !found {
		t.Fatalf("promoted intent %q not in list: %+v", promoteOut.ID, items)
	}
}

func TestIntentsPromoteRefusesWhenPromotionDisabled(t *testing.T) {
	h, _ := newIntentsHarness(t, false)
	resp := h.post(t, "/v1/intents/promote", `{"candidate_id":"anything"}`, h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (router.promotion.enabled=false)", resp.StatusCode)
	}
}

func TestIntentsPromoteNoPendingDraft(t *testing.T) {
	h, _ := newIntentsHarness(t, true)
	// A validly-shaped (12 hex chars) but nonexistent candidate id: this
	// must fail with "no pending file", not the shape-validation 400 that
	// TestIntentsPromoteRejectsPathTraversalCandidateID covers separately.
	resp := h.post(t, "/v1/intents/promote", `{"candidate_id":"aaaaaaaaaaaa"}`, h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestIntentsPromoteRejectsPathTraversalCandidateID: a candidate_id that
// isn't candidateID's own 12-hex-character shape must be refused with 400
// BEFORE it is ever joined into a filesystem path — proving
// handleIntentsPromote can't be made to read (and, on a passing
// ValidateLearned, promote) an arbitrary file outside PendingDir.
func TestIntentsPromoteRejectsPathTraversalCandidateID(t *testing.T) {
	h, _ := newIntentsHarness(t, true)

	outside := filepath.Join(h.dir, "outside-secret.yaml")
	if err := os.WriteFile(outside, []byte(draftReplyYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"../outside-secret", "../../outside-secret", "not-hex-id", ""} {
		body := `{"candidate_id":"` + bad + `"}`
		resp := h.post(t, "/v1/intents/promote", body, h.token)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("candidate_id %q: status = %d, body = %s, want 400", bad, resp.StatusCode, readBody(t, resp))
		} else {
			resp.Body.Close()
		}
	}

	// The learned overlay must still be empty: nothing was ever written.
	learnedDir := filepath.Join(h.dir, "twins", "test", "intents", "learned")
	if entries, err := os.ReadDir(learnedDir); err == nil && len(entries) != 0 {
		t.Fatalf("learned dir = %v, want empty (no traversal attempt should ever write anything)", entries)
	}
}

// TestIntentsDemoteAndEnableEndpoint: the manual demote/enable round trip
// (R-23's store-level TestManualDemoteAndEnableRoundTrip) exercised over
// HTTP, and proven NOT gated on router.promotion.enabled (unlike
// draft/promote): this harness has the flag off.
func TestIntentsDemoteAndEnableEndpoint(t *testing.T) {
	h, _ := newIntentsHarness(t, false)

	demoteResp := h.post(t, "/v1/intents/demote", `{"id":"learned.some_intent","reason":"too noisy"}`, h.token)
	if demoteResp.StatusCode != http.StatusOK {
		t.Fatalf("demote status = %d, body = %s", demoteResp.StatusCode, readBody(t, demoteResp))
	}
	demoteResp.Body.Close()

	states, err := h.st.ListIntentStates(t.Context())
	if err != nil {
		t.Fatalf("ListIntentStates: %v", err)
	}
	if reason, ok := states["learned.some_intent"]; !ok || reason != "too noisy" {
		t.Fatalf("states = %+v, want learned.some_intent disabled with reason %q", states, "too noisy")
	}

	enableResp := h.post(t, "/v1/intents/enable", `{"id":"learned.some_intent"}`, h.token)
	if enableResp.StatusCode != http.StatusOK {
		t.Fatalf("enable status = %d, body = %s", enableResp.StatusCode, readBody(t, enableResp))
	}
	enableResp.Body.Close()

	states, err = h.st.ListIntentStates(t.Context())
	if err != nil {
		t.Fatalf("ListIntentStates: %v", err)
	}
	if _, ok := states["learned.some_intent"]; ok {
		t.Fatalf("states = %+v, want learned.some_intent no longer disabled", states)
	}
}

func TestIntentsDemoteRequiresID(t *testing.T) {
	h, _ := newIntentsHarness(t, true)
	resp := h.post(t, "/v1/intents/demote", `{}`, h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestRouterHealthReportsPromotionEnabled: GET /v1/router's
// promotion_enabled field mirrors router.promotion.enabled directly, so
// `water intent promote` can refuse before ever prompting.
func TestRouterHealthReportsPromotionEnabled(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		h, _ := newIntentsHarness(t, enabled)
		resp := h.get(t, "/v1/router", h.token)
		var health map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
			t.Fatalf("decode: %v", err)
		}
		resp.Body.Close()
		if got, _ := health["promotion_enabled"].(bool); got != enabled {
			t.Fatalf("promotion_enabled = %v, want %v", got, enabled)
		}
	}
}
