package gateway

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"water/internal/store"
)

// TestCreateIdeaIsAPOSTBodyNeverAQuery is docs/slices/UI.md Phase 5c's own
// explicit call-out: the capture bar posts a body, never a query string, so
// an idea's text never lands in a server access log via a URL.
// handleCreateIdea (ideas.go) only ever decodes the JSON body; a request
// carrying the title/gist as a query string instead of a body is rejected
// (its body is empty, so decoding fails) rather than silently accepted.
func TestCreateIdeaIsAPOSTBodyNeverAQuery(t *testing.T) {
	h := newHarness(t)

	// A query-string-only request (no body at all) is refused: there is
	// nothing to decode, and the handler never falls back to
	// r.URL.Query().
	if got := statusOf(do(t, h.srv.URL, "POST", "/v1/ideas?title=Sneaky&gist=via+query", "", h.token)); got != http.StatusBadRequest {
		t.Fatalf("query-string-only capture: status = %d, want 400", got)
	}
	ideas, err := h.st.ListIdeas(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(ideas) != 0 {
		t.Fatalf("a query-string-only capture must create nothing, got %d ideas", len(ideas))
	}

	// A real body works, and the title makes it into the stored idea (not
	// the query string above, which this call doesn't even carry).
	resp := do(t, h.srv.URL, "POST", "/v1/ideas", `{"title":"On-device short-utterance model","gist":"faster wake word"}`, h.token)
	var created map[string]any
	decodeInto(t, resp, http.StatusCreated, &created)
	if created["title"] != "On-device short-utterance model" || created["stage"] != "raw" {
		t.Fatalf("created idea = %+v", created)
	}
}

// TestHandleCreateIdeaNeverReadsTheQueryString is the static counterpart:
// ideas.go's own source text never mentions r.URL.Query() or r.URL.RawQuery
// at all, so there is no code path -- exercised by the test above or not --
// that could read a capture from the URL.
func TestHandleCreateIdeaNeverReadsTheQueryString(t *testing.T) {
	src, err := os.ReadFile("ideas.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"URL.Query", "RawQuery"} {
		if strings.Contains(string(src), needle) {
			t.Errorf("ideas.go references %q; the idea capture bar must only ever read a POST body", needle)
		}
	}
}

// TestListIdeasReturnsEveryIdea confirms GET /v1/ideas returns every idea,
// oldest first, with enough on each row for the client to bucket into
// Raw/Explored itself.
func TestListIdeasReturnsEveryIdea(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	raw, err := h.st.CreateIdea(ctx, store.Idea{Title: "Raw idea", Stage: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	explored, err := h.st.CreateIdea(ctx, store.Idea{Title: "Explored idea", Stage: "explored"})
	if err != nil {
		t.Fatal(err)
	}

	resp := do(t, h.srv.URL, "GET", "/v1/ideas", "", h.token)
	var out []map[string]any
	decodeInto(t, resp, http.StatusOK, &out)
	if len(out) != 2 {
		t.Fatalf("got %d ideas, want 2", len(out))
	}
	byID := map[string]map[string]any{}
	for _, iv := range out {
		byID[iv["id"].(string)] = iv
	}
	if byID[raw.ID]["stage"] != "raw" || byID[explored.ID]["stage"] != "explored" {
		t.Fatalf("got = %+v", out)
	}
}

// TestStartIdeaResearchQueuesARun exercises POST /v1/ideas/{id}/research:
// it creates a "queued" research_runs row (with its five steps pre-seeded
// "pending") and hands it to the runner's FIFO queue -- proven here by
// waiting for the background worker to finish and observing the run reach
// a terminal state (this harness's backend is backend.Fake, non-metered,
// but there is no research connector wired into newHarness's manifest at
// all, so the run is expected to fail once it tries its first step, not to
// silently stay queued forever).
func TestStartIdeaResearchQueuesARun(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	idea, err := h.st.CreateIdea(ctx, store.Idea{Title: "Faster onboarding", Stage: "raw"})
	if err != nil {
		t.Fatal(err)
	}

	resp := do(t, h.srv.URL, "POST", "/v1/ideas/"+idea.ID+"/research", "", h.token)
	var run map[string]any
	decodeInto(t, resp, http.StatusOK, &run)
	if run["status"] != "queued" {
		t.Fatalf("run status = %v, want queued", run["status"])
	}
	runID := run["id"].(string)

	// The five steps are pre-seeded synchronously, before this handler ever
	// returns -- but the background worker may already be running by the
	// time this test looks, so only N/Label (never Status, which races
	// with the worker) are checked here.
	steps, err := h.st.ListResearchSteps(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 5 {
		t.Fatalf("got %d pre-seeded steps, want 5", len(steps))
	}
	for i, facet := range researchFacets {
		if steps[i].N != i+1 || steps[i].Label != facet {
			t.Fatalf("step %d = %+v, want n %d label %q", i, steps[i], i+1, facet)
		}
	}

	h.d.bg.Wait()
	got, err := h.st.GetResearchRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == "queued" || got.Status == "running" {
		t.Fatalf("run never left %q after the worker finished", got.Status)
	}
}

// TestStartIdeaResearchOnUnknownIdeaIs404 confirms the idea must actually
// exist (the research_runs table has a hard FK to ideas, so this also
// avoids ever hitting that constraint from a client-supplied id).
func TestStartIdeaResearchOnUnknownIdeaIs404(t *testing.T) {
	h := newHarness(t)
	if got := statusOf(do(t, h.srv.URL, "POST", "/v1/ideas/idea_does_not_exist/research", "", h.token)); got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
}

// TestProposeIdeaDraftCreatesACodeBuiltDraft exercises POST
// /v1/ideas/{id}/propose: it creates a drafts row (template
// "idea_proposal") whose subject/body are built from the idea's own title
// and gist, never a model call.
func TestProposeIdeaDraftCreatesACodeBuiltDraft(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	idea, err := h.st.CreateIdea(ctx, store.Idea{Title: "Faster onboarding", Gist: "Cut signup from 5 steps to 2.", Stage: "raw"})
	if err != nil {
		t.Fatal(err)
	}

	resp := do(t, h.srv.URL, "POST", "/v1/ideas/"+idea.ID+"/propose", "", h.token)
	var draft map[string]any
	decodeInto(t, resp, http.StatusOK, &draft)
	if draft["template"] != "idea_proposal" {
		t.Fatalf("draft template = %v, want idea_proposal", draft["template"])
	}
	subject, _ := draft["subject"].(string)
	body, _ := draft["body"].(string)
	if !strings.Contains(subject, idea.Title) {
		t.Fatalf("draft subject = %q, want it to mention %q", subject, idea.Title)
	}
	if !strings.Contains(body, idea.Title) || !strings.Contains(body, idea.Gist) {
		t.Fatalf("draft body = %q, want it built from the idea's own title and gist", body)
	}

	// Persisted: GET /v1/drafts/{id} returns the same draft.
	got, err := h.st.GetDraft(ctx, draft["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if got.Template != "idea_proposal" {
		t.Fatalf("stored draft template = %q", got.Template)
	}
}

func TestProposeIdeaDraftOnUnknownIdeaIs404(t *testing.T) {
	h := newHarness(t)
	if got := statusOf(do(t, h.srv.URL, "POST", "/v1/ideas/idea_does_not_exist/propose", "", h.token)); got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
}
