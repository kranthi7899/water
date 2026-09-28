package gateway

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/connectors"
	"water/internal/connectors/research"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

// recordingResearchRunner is a fake research.Runner (docs/slices/W.md's own
// Runner interface): it records every query it is asked to search, and
// optionally fails the call at a given 1-based call index, to simulate a
// runner-level failure distinct from a gate denial. No subprocess, no
// network -- exactly the posture internal/connectors/research's own tests
// (research_test.go, TestResearchWebEscalatesSessionTaint) already use a
// fake runner for.
type recordingResearchRunner struct {
	mu      sync.Mutex
	queries []string
}

func (r *recordingResearchRunner) Search(_ context.Context, query string, _ int) (research.Answer, error) {
	r.mu.Lock()
	r.queries = append(r.queries, query)
	r.mu.Unlock()
	return research.Answer{
		Summary: "summary for " + query,
		Sources: []research.Source{{Title: "Example", URL: "https://example.com/a"}},
	}, nil
}

func (r *recordingResearchRunner) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.queries...)
}

// gatedResearchRunner blocks the very first Search call on hold, so a test
// can prove a second queued run never starts while the first is still in
// flight (docs/slices/UI.md Phase 5c: "it runs one run at a time").
type gatedResearchRunner struct {
	mu      sync.Mutex
	queries []string
	holding bool
	hold    chan struct{}
}

func (r *gatedResearchRunner) Search(_ context.Context, query string, _ int) (research.Answer, error) {
	r.mu.Lock()
	first := !r.holding
	r.holding = true
	r.queries = append(r.queries, query)
	r.mu.Unlock()
	if first {
		<-r.hold
	}
	return research.Answer{Summary: "summary for " + query, Sources: []research.Source{{Title: "T", URL: "https://example.com/x"}}}, nil
}

func (r *gatedResearchRunner) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.queries...)
}

// runnerTestManifest wires exactly one connector, research, with a
// configurable rate cap (per is fixed at 1h; max is the test's own choice,
// so TestResearchRunnerStopsOnADenial can pick a small one and hit P1's
// non-P0 share -- gate.go's own rateLimitFor, 75% of max rounded down --
// inside five steps). Deliberately does NOT list research.web on
// auto_allowlist: the runner calls at origin P1, which never checks that
// list (only P2 does), so this fixture matches the real twin.yaml's own
// posture and would catch a regression back to P2 (P2 would fail outright
// without this entry, rather than silently passing).
const runnerTestManifestFmt = `
id: test
name: Test twin
usage: {window: 1h, model_calls: 50, auto_model_calls: 10}
connectors:
  - name: research
    functions:
      - {name: web, level: R, rate: {max: %d, per: 1h}}
`

// newRunnerHarness builds a harness wired exactly like newResearchHarness
// (research_test.go) -- the real research connector and the real gate, so
// the runner's Gate.Invoke calls are genuine, not stubbed -- but
// parameterized by the manifest's own rate cap and by the fake Runner, so
// this file's tests can pick a small cap (a real denial) or a generous one
// (all five steps succeed).
func newRunnerHarness(t *testing.T, rateMax int, runner research.Runner) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	q := approvals.NewQueue(st, log)
	reg, err := connectors.NewRegistry(research.New(runner))
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(fmt.Sprintf(runnerTestManifestFmt, rateMax)))
	if err != nil {
		t.Fatal(err)
	}
	v := vault.NewMemory()
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: v, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	fb := backend.NewFake("fake")
	clients, err := LoadClients(filepath.Join(dir, "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := clients.EnsureCLI()
	if err != nil {
		t.Fatal(err)
	}
	d := New(Config{Manifest: m, Store: st, Audit: log, Approvals: q, Gate: g, Registry: reg, Backend: fb, Clients: clients,
		SocketPath: "unused-in-http-tests.sock", Nervous: testNervous(t, m, st)})
	srv := httptest.NewServer(d.Mux())
	t.Cleanup(srv.Close)
	return &harness{d: d, srv: srv, token: tok, st: st, log: log, q: q, fake: fb, vault: v, dir: dir}
}

// queueIdeaResearch is this file's own shorthand for POST
// /v1/ideas/{id}/research, returning the queued run's id.
func queueIdeaResearch(t *testing.T, h *harness, ideaID string) string {
	t.Helper()
	resp := do(t, h.srv.URL, "POST", "/v1/ideas/"+ideaID+"/research", "", h.token)
	var run map[string]any
	decodeInto(t, resp, http.StatusOK, &run)
	return run["id"].(string)
}

// TestResearchRunnerRunsFiveStepsAtP1WithTemplatedQueries is this phase's
// central runner test: five research.web calls, in researchFacets order,
// each query exactly "<idea title> <facet>", each recorded in the audit
// log at origin P1 -- a coordinator decision (not the plan document's own
// literal "P2" wording) made specifically so this runner never needs
// research.web on the manifest's auto_allowlist, keeping Slice W's "never
// auto-executed" invariant intact; see research_runner.go's own top-of-file
// doc comment for the full reasoning.
func TestResearchRunnerRunsFiveStepsAtP1WithTemplatedQueries(t *testing.T) {
	runner := &recordingResearchRunner{}
	h := newRunnerHarness(t, 20, runner)
	idea := mustCreateIdea(t, h, "On-device short-utterance model")

	runID := queueIdeaResearch(t, h, idea.ID)
	h.d.bg.Wait()

	got := runner.calls()
	if len(got) != len(researchFacets) {
		t.Fatalf("got %d research.web calls, want %d", len(got), len(researchFacets))
	}
	for i, facet := range researchFacets {
		want := idea.Title + " " + facet
		if got[i] != want {
			t.Errorf("call %d query = %q, want %q", i, got[i], want)
		}
	}

	// Every one of the five calls is recorded at origin P1.
	var p1Calls int
	for _, e := range readAuditEntries(t, filepath.Join(h.dir, "audit.jsonl")) {
		if e.Kind == audit.KindCall && e.Function == "research.web" {
			if e.Origin != "p1" {
				t.Errorf("research.web call recorded at origin %q, want p1", e.Origin)
			}
			p1Calls++
		}
	}
	if p1Calls != len(researchFacets) {
		t.Fatalf("got %d research.web audit calls, want %d", p1Calls, len(researchFacets))
	}

	run, err := h.st.GetResearchRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "finished" {
		t.Fatalf("run status = %q, want finished", run.Status)
	}
	if run.FinishedAt.IsZero() {
		t.Fatal("finished run must carry a finished_at")
	}
	// The report is always untrusted -- store.ResearchRun's own
	// CreateResearchRun/CHECK-enforced invariant (Phase 1d); this runner
	// never tries to override it.
	if !run.Untrusted {
		t.Fatal("run.Untrusted = false, want true (a research report is always untrusted)")
	}
	if run.ReportText == "" {
		t.Fatal("finished run has no report text")
	}

	steps, err := h.st.ListResearchSteps(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 5 {
		t.Fatalf("got %d steps, want 5", len(steps))
	}
	for i, s := range steps {
		if s.Status != "done" {
			t.Errorf("step %d status = %q, want done", i+1, s.Status)
		}
		if s.SourceCount != 1 {
			t.Errorf("step %d source_count = %d, want 1", i+1, s.SourceCount)
		}
	}
}

// TestResearchRunnerStopsOnADenial: a gate that denies partway through
// (here, the manifest's own rate cap, exhausted after P1's non-P0 share of
// a small max) must stop the run at once -- no retry, no skipping ahead to
// the next step -- and mark both the failing step and the run itself
// failed.
func TestResearchRunnerStopsOnADenial(t *testing.T) {
	runner := &recordingResearchRunner{}
	// rate max 4 -> P1's share is floor(4*0.75) = 3 (gate.go's own
	// rateLimitFor/backgroundRateSharePct), so the 4th of 5 steps is denied.
	h := newRunnerHarness(t, 4, runner)
	idea := mustCreateIdea(t, h, "Faster onboarding")

	runID := queueIdeaResearch(t, h, idea.ID)
	h.d.bg.Wait()

	if got := len(runner.calls()); got != 3 {
		t.Fatalf("got %d research.web calls, want exactly 3 (the run must stop at the denial, not retry or skip ahead)", got)
	}

	run, err := h.st.GetResearchRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" {
		t.Fatalf("run status = %q, want failed", run.Status)
	}
	if run.FinishedAt.IsZero() {
		t.Fatal("a stopped run must still carry a finished_at")
	}

	steps, err := h.st.ListResearchSteps(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 5 {
		t.Fatalf("got %d steps, want 5 (the later ones stay at their pre-seeded state)", len(steps))
	}
	for i, s := range steps {
		switch {
		case i < 3:
			if s.Status != "done" {
				t.Errorf("step %d status = %q, want done", i+1, s.Status)
			}
		case i == 3:
			if s.Status != "failed" {
				t.Errorf("step %d status = %q, want failed", i+1, s.Status)
			}
		default:
			if s.Status != "pending" {
				t.Errorf("step %d status = %q, want pending (never attempted)", i+1, s.Status)
			}
		}
	}
}

// TestResearchRunnerRefusesWhenBackendIsMetered is this phase's single most
// safety-critical test (W invariant 5): a backend.Fake configured to report
// Metered: true must prevent the run from EVER starting -- zero
// research.web calls, checked once, before the first step.
func TestResearchRunnerRefusesWhenBackendIsMetered(t *testing.T) {
	runner := &recordingResearchRunner{}
	h := newRunnerHarness(t, 20, runner)
	h.fake.Avail.Metered = true
	idea := mustCreateIdea(t, h, "Faster onboarding")

	runID := queueIdeaResearch(t, h, idea.ID)
	h.d.bg.Wait()

	if got := len(runner.calls()); got != 0 {
		t.Fatalf("got %d research.web calls while the backend is metered, want 0", got)
	}
	run, err := h.st.GetResearchRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" {
		t.Fatalf("run status = %q, want failed (refused before it ever started)", run.Status)
	}
	if run.Untrusted != true {
		t.Fatal("run.Untrusted must still read true even for a refused run")
	}
	steps, err := h.st.ListResearchSteps(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range steps {
		if s.Status != "pending" {
			t.Errorf("step %d status = %q, want pending (no step ever started)", i+1, s.Status)
		}
	}

	// No research.web audit entry was ever recorded either -- the refusal
	// happens before the gate is ever reached at all. (readAuditEntries
	// assumes a non-empty file; this run's refusal never calls the gate at
	// all, so the audit log may be entirely empty here.)
	if b, err := os.ReadFile(filepath.Join(h.dir, "audit.jsonl")); err == nil && strings.TrimSpace(string(b)) != "" {
		for _, e := range readAuditEntries(t, filepath.Join(h.dir, "audit.jsonl")) {
			if e.Function == "research.web" {
				t.Fatalf("unexpected research.web audit entry while metered: %+v", e)
			}
		}
	}
}

// TestResearchRunnerQueueRunsOneRunAtATime queues two runs back to back and
// proves the second never starts (no research.web call belonging to it)
// until the first has fully finished, and that the eventual call order is
// strictly FIFO.
func TestResearchRunnerQueueRunsOneRunAtATime(t *testing.T) {
	runner := &gatedResearchRunner{hold: make(chan struct{})}
	h := newRunnerHarness(t, 20, runner)
	idea1 := mustCreateIdea(t, h, "Idea One")
	idea2 := mustCreateIdea(t, h, "Idea Two")

	queueIdeaResearch(t, h, idea1.ID)
	run2ID := queueIdeaResearch(t, h, idea2.ID)

	// Wait for the worker to actually be inside run1's first step (blocked
	// on runner.hold).
	deadline := time.Now().Add(5 * time.Second)
	for {
		if len(runner.calls()) >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the first research.web call")
		}
		time.Sleep(2 * time.Millisecond)
	}

	// run2 must still be queued: the worker is a single goroutine, and it
	// is currently blocked inside run1.
	got2, err := h.st.GetResearchRun(t.Context(), run2ID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Status != "queued" {
		t.Fatalf("run2 status = %q while run1 is still in flight, want queued", got2.Status)
	}

	close(runner.hold) // release run1; it runs to completion, then run2 starts
	h.d.bg.Wait()

	queries := runner.calls()
	if len(queries) != 10 {
		t.Fatalf("got %d total queries, want 10 (5 per run)", len(queries))
	}
	for i := 0; i < 5; i++ {
		if !strings.HasPrefix(queries[i], "Idea One ") {
			t.Errorf("query %d = %q, want an Idea One step (run1 must finish before run2 starts)", i, queries[i])
		}
	}
	for i := 5; i < 10; i++ {
		if !strings.HasPrefix(queries[i], "Idea Two ") {
			t.Errorf("query %d = %q, want an Idea Two step", i, queries[i])
		}
	}

	run2, err := h.st.GetResearchRun(t.Context(), run2ID)
	if err != nil {
		t.Fatal(err)
	}
	if run2.Status != "finished" {
		t.Fatalf("run2 status = %q, want finished", run2.Status)
	}
}

// TestIdeasAndResearchRoutesNeverWriteADecision is docs/slices/UI.md Phase
// 5's acceptance item, scoped to Phase 5c's own new routes: every route
// under /v1/ideas and /v1/research, invoked with valid bodies against a
// real (counting) store, writes zero rows to card_states/
// card_action_states/decision_classifications/decision_records, and makes
// no decisions.Trigger.Run call (there is none reachable from this package
// at all in these tests -- newHarness never sets Config.Decisions).
func TestIdeasAndResearchRoutesNeverWriteADecision(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	idea, err := h.st.CreateIdea(ctx, store.Idea{Title: "Faster onboarding", Stage: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	finishedRun := seedFinishedRun(t, h, idea.ID, "## Overview\nSome text.\n")

	dbPath := filepath.Join(h.dir, "water.db")
	before := wsDecisionTableCounts(t, dbPath)

	table := []struct{ name, method, path, body string }{
		{"list ideas", "GET", "/v1/ideas", ""},
		{"create idea", "POST", "/v1/ideas", `{"title":"Another idea","gist":"g"}`},
		{"start research", "POST", "/v1/ideas/" + idea.ID + "/research", ""},
		{"propose", "POST", "/v1/ideas/" + idea.ID + "/propose", ""},
		{"unknown idea 404", "GET", "/v1/ideas/does-not-exist", ""}, // not a real route, but must still write nothing
		{"list research runs", "GET", "/v1/research/runs", ""},
		{"get research run", "GET", "/v1/research/runs/" + finishedRun.ID, ""},
		{"attach", "POST", "/v1/research/runs/" + finishedRun.ID + "/attach", `{"card_id":"card-inv-1"}`},
	}
	for _, tc := range table {
		t.Run(tc.name, func(t *testing.T) {
			resp := do(t, h.srv.URL, tc.method, tc.path, tc.body, h.token)
			resp.Body.Close()
		})
	}
	h.d.bg.Wait() // the "start research" call above queues a background run

	after := wsDecisionTableCounts(t, dbPath)
	for table, n := range before {
		if after[table] != n {
			t.Errorf("%s: rows went from %d to %d -- an ideas/research route must never write here", table, n, after[table])
		}
	}
}

// TestIdeasFileNeverReferencesDecisionWrites is the static counterpart,
// mirroring workspace_detail_test.go's own
// TestWorkspaceDetailFileNeverReferencesDecisionWrites: none of this
// phase's own new files import water/internal/decisions or reference
// Trigger/SetCardState/SetCardActionState/SetDecisionClassification/
// UpsertDecisionRecord by name -- Attach's own one sanctioned write
// (AddCardEvidenceExtra, AttachResearchRunCard) is not in this banned list
// at all, by design.
func TestIdeasFileNeverReferencesDecisionWrites(t *testing.T) {
	banned := map[string]bool{
		"Trigger": true, "SetCardState": true, "SetCardActionState": true,
		"SetDecisionClassification": true, "UpsertDecisionRecord": true,
	}
	for _, file := range []string{"ideas.go", "research_runs.go", "research_runner.go"} {
		assertFileNeverReferencesDecisionWrites(t, file, banned)
	}
}

// assertFileNeverReferencesDecisionWrites parses file's own AST and fails
// the test if it imports water/internal/decisions or references any name in
// banned -- the same style workspace_detail_test.go's own
// TestWorkspaceDetailFileNeverReferencesDecisionWrites inlines for
// workspace_detail.go, factored out here so it can be run over several
// files at once.
func assertFileNeverReferencesDecisionWrites(t *testing.T, file string, banned map[string]bool) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range f.Imports {
		if strings.Contains(imp.Path.Value, "internal/decisions") {
			t.Errorf("%s imports %s, which this file must never depend on", file, imp.Path.Value)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && banned[id.Name] {
			t.Errorf("%s references %q, which this file must never call (the no-decision invariant)", file, id.Name)
		}
		return true
	})
}
