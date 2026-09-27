package linear

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/connectors"
	"water/internal/connectors/tokenapi"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

const testManifest = `
id: test
name: Test twin
usage: {window: 1h, model_calls: 10}
connectors:
  - name: linear
    functions:
      - {name: list_issues, level: R}
`

func noSleep(context.Context, time.Duration) error { return nil }

func testCredential(t *testing.T) vault.Secret {
	t.Helper()
	cred := tokenapi.Credential{Token: "lin_api_testkey1234567890"}
	sec, err := cred.Secret()
	if err != nil {
		t.Fatal(err)
	}
	return sec
}

type harness struct {
	g *gate.Gate
}

func newHarness(t *testing.T, srv *httptest.Server, tok vault.Secret) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log, err := audit.Open(filepath.Join(dir, "audit", "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	q := approvals.NewQueue(st, log)
	opts := &tokenapi.Options{BaseURL: srv.URL, HTTPClient: srv.Client(), Sleep: noSleep}
	conn := NewWithOptions(opts)
	reg, err := connectors.NewRegistry(conn)
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(testManifest))
	if err != nil {
		t.Fatal(err)
	}
	v := vault.NewMemory()
	if !tok.IsZero() {
		if err := v.Set(Service, Account, tok); err != nil {
			t.Fatal(err)
		}
	}
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: v, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{g: g}
}

func (h *harness) invoke(t *testing.T, args map[string]any) (gate.Result, error) {
	t.Helper()
	return h.g.Invoke(context.Background(), gate.Call{Function: "linear.list_issues", Args: args, Origin: gate.P0, Taint: gate.Clean})
}

// ---- schema ----

func TestFunctionsSchema(t *testing.T) {
	l := New()
	for _, f := range l.Functions() {
		if f.Level != twins.R {
			t.Fatalf("%s: level %s, want R", f.Name, f.Level)
		}
		if !f.External {
			t.Fatalf("%s: must be marked External", f.Name)
		}
		b, err := json.Marshal(f.Schema)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"additionalProperties":false`) {
			t.Fatalf("%s schema: %s", f.Name, b)
		}
		if err := f.Schema.Validate(map[string]any{"bogus": "x"}); err == nil {
			t.Fatalf("%s: unknown argument accepted", f.Name)
		}
	}
}

// ---- request shape: single POST, raw auth header, query+variables body ----

func nodeJSON(id, identifier, title string, priority float64, state, assignee, project string) wireIssue {
	w := wireIssue{ID: id, Identifier: identifier, Title: title, Priority: priority, URL: "https://linear.app/nimbus/issue/" + identifier}
	if state != "" {
		w.State = &struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}{Name: state}
	}
	if assignee != "" {
		w.Assignee = &struct {
			Name string `json:"name"`
		}{assignee}
	}
	if project != "" {
		w.Project = &struct {
			Name string `json:"name"`
		}{project}
	}
	return w
}

// ---- pagination via pageInfo cursor ----

func TestListIssuesFollowsCursorPagination(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "lin_api_testkey1234567890" {
			t.Fatalf("Authorization header = %q, want raw key with no Bearer prefix", got)
		}
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body.Query, "issues(") {
			t.Fatalf("query missing issues field: %s", body.Query)
		}
		calls++
		resp := issuesQueryResponse{}
		if body.Variables["after"] == nil {
			resp.Data.Issues.Nodes = []wireIssue{nodeJSON("i1", "ENG-1", "first page issue", 1, "In Progress", "alice", "Checkout")}
			resp.Data.Issues.PageInfo = pageInfo{HasNextPage: true, EndCursor: "cursor1"}
		} else if body.Variables["after"] == "cursor1" {
			resp.Data.Issues.Nodes = []wireIssue{nodeJSON("i2", "ENG-2", "second page issue", 2, "Todo", "bob", "Growth")}
			resp.Data.Issues.PageInfo = pageInfo{HasNextPage: false}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	h := newHarness(t, srv, testCredential(t))
	res, err := h.invoke(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	var issues []Issue
	if err := json.Unmarshal(res.Output, &issues); err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 {
		t.Fatalf("got %d issues, want 2 (calls=%d)", len(issues), calls)
	}
	if issues[0].Priority != "Urgent" || issues[1].Priority != "High" {
		t.Fatalf("priority labels: %+v", issues)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if !res.Untrusted {
		t.Fatal("linear.list_issues result must be marked untrusted (External)")
	}
}

// ---- Phase 4 read-extension fields (team, state.type, dueDate, labels,
// relations/inverseRelations) ----

// TestListIssuesReturnsThePhase4ReadExtensionFields is docs/slices/UI.md
// Phase 4's U4 sub-question worked example: list_issues' query gains
// team{key}, state{type}, dueDate, labels and relations/inverseRelations
// as read-only response fields (no new argument, no write), for
// internal/dashboards/compute.go's Delivery dashboard.
func TestListIssuesReturnsThePhase4ReadExtensionFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		for _, want := range []string{"team", "dueDate", "labels", "relations", "inverseRelations", "state { name type }"} {
			if !strings.Contains(body.Query, want) {
				t.Errorf("query missing %q: %s", want, body.Query)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"issues":{"nodes":[
			{
				"id": "i1", "identifier": "CRA-3", "title": "blocker issue", "priority": 1,
				"dueDate": "2026-11-01",
				"state": {"name": "In Progress", "type": "started"},
				"team": {"key": "CRA"},
				"labels": {"nodes": [{"name": "urgent"}, {"name": "customer"}]},
				"relations": {"nodes": [
					{"type": "blocks", "relatedIssue": {"identifier": "CRA-4", "state": {"type": "started"}}}
				]},
				"inverseRelations": {"nodes": []},
				"url": "https://linear.app/nimbus/issue/CRA-3"
			},
			{
				"id": "i2", "identifier": "CRA-4", "title": "blocked issue", "priority": 3,
				"state": {"name": "Todo", "type": "unstarted"},
				"team": {"key": "CRA"},
				"relations": {"nodes": []},
				"inverseRelations": {"nodes": [
					{"type": "blocks", "issue": {"identifier": "CRA-3", "state": {"type": "started"}}}
				]},
				"url": "https://linear.app/nimbus/issue/CRA-4"
			}
		], "pageInfo": {"hasNextPage": false}}}}`)
	}))
	defer srv.Close()

	h := newHarness(t, srv, testCredential(t))
	res, err := h.invoke(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	var issues []Issue
	if err := json.Unmarshal(res.Output, &issues); err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 {
		t.Fatalf("got %d issues, want 2", len(issues))
	}
	blocker, blocked := issues[0], issues[1]
	if blocker.Team != "CRA" || blocked.Team != "CRA" {
		t.Fatalf("team = %+v", issues)
	}
	if blocker.StateType != "started" || blocked.StateType != "unstarted" {
		t.Fatalf("state type = %+v", issues)
	}
	if blocker.DueDate != "2026-11-01" {
		t.Fatalf("due date = %q, want 2026-11-01", blocker.DueDate)
	}
	if len(blocker.Labels) != 2 || blocker.Labels[0] != "urgent" || blocker.Labels[1] != "customer" {
		t.Fatalf("labels = %+v", blocker.Labels)
	}
	if blocker.PriorityRank != 1 {
		t.Fatalf("priority rank = %d, want 1", blocker.PriorityRank)
	}
	if len(blocker.Relations) != 1 || blocker.Relations[0].Type != "blocks" || blocker.Relations[0].Identifier != "CRA-4" || blocker.Relations[0].StateType != "started" {
		t.Fatalf("blocker relations = %+v", blocker.Relations)
	}
	if len(blocked.InverseRelations) != 1 || blocked.InverseRelations[0].Type != "blocks" || blocked.InverseRelations[0].Identifier != "CRA-3" || blocked.InverseRelations[0].StateType != "started" {
		t.Fatalf("blocked inverse relations = %+v", blocked.InverseRelations)
	}
}

// TestListIssuesRelationsSkipNullOtherIssue: a relation node whose other-
// side issue is null (Linear can return this for a deleted issue) is
// skipped, not a panic.
func TestListIssuesRelationsSkipNullOtherIssue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"issues":{"nodes":[
			{
				"id": "i1", "identifier": "CRA-3", "title": "t", "priority": 3,
				"relations": {"nodes": [{"type": "blocks", "relatedIssue": null}]},
				"inverseRelations": {"nodes": [{"type": "blocks", "issue": null}]},
				"url": "https://linear.app/nimbus/issue/CRA-3"
			}
		], "pageInfo": {"hasNextPage": false}}}}`)
	}))
	defer srv.Close()

	h := newHarness(t, srv, testCredential(t))
	res, err := h.invoke(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	var issues []Issue
	if err := json.Unmarshal(res.Output, &issues); err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || len(issues[0].Relations) != 0 || len(issues[0].InverseRelations) != 0 {
		t.Fatalf("issues = %+v, want relations/inverseRelations dropped, not panicked", issues)
	}
}

// ---- filters ----

func TestListIssuesFilters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := issuesQueryResponse{}
		resp.Data.Issues.Nodes = []wireIssue{
			nodeJSON("i1", "ENG-1", "cart totals wrong", 1, "In Progress", "alice", "Checkout Revamp"),
			nodeJSON("i2", "ENG-2", "sso support", 2, "Done", "bob", "Platform Reliability"),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	h := newHarness(t, srv, testCredential(t))
	res, err := h.invoke(t, map[string]any{"status": "In Progress"})
	if err != nil {
		t.Fatal(err)
	}
	var issues []Issue
	if err := json.Unmarshal(res.Output, &issues); err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Identifier != "ENG-1" {
		t.Fatalf("status filter: %+v", issues)
	}
}

// ---- 401 ----

func TestInvokeReturns401AsAClearAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Authentication required"}`))
	}))
	defer srv.Close()

	h := newHarness(t, srv, testCredential(t))
	_, err := h.invoke(t, nil)
	if err == nil {
		t.Fatal("expected an error on 401")
	}
}

// ---- not connected ----

func TestInvokeWithNoTokenFailsClearly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no request should ever be made without a token")
	}))
	defer srv.Close()

	h := newHarness(t, srv, vault.Secret{})
	_, err := h.invoke(t, nil)
	if err == nil {
		t.Fatal("expected an error with no token configured")
	}
}

// ---- GraphQL-level errors (200 with an errors array) ----

func TestGraphQLErrorsSurfaceClearly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"field priority not found"}]}`))
	}))
	defer srv.Close()

	h := newHarness(t, srv, testCredential(t))
	_, err := h.invoke(t, nil)
	if err == nil || !strings.Contains(err.Error(), "field priority not found") {
		t.Fatalf("expected the GraphQL error message surfaced, got: %v", err)
	}
}

// ---- Normalize ----

func TestNormalizeIssues(t *testing.T) {
	l := New()
	issues := []Issue{{ID: "1", Identifier: "ENG-1", Title: "cart totals wrong", Status: "In Progress", Priority: "Urgent", Assignee: "alice", Project: "Checkout Revamp", URL: "https://linear.app/nimbus/issue/ENG-1"}}
	raw, err := json.Marshal(issues)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := l.Normalize("list_issues", raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("normalize: %d records, want 1", len(recs))
	}
	is, ok := recs[0].(*store.Issue)
	if !ok {
		t.Fatalf("did not normalize to *store.Issue: %T", recs[0])
	}
	if !is.External || is.Assignee != "alice" || is.Project != "Checkout Revamp" || !strings.Contains(is.Title, "ENG-1") {
		t.Fatalf("issue: %+v", is)
	}
}

// ---- secrets never leak ----

func TestTokenNeverAppearsInError(t *testing.T) {
	const token = "lin_api_supersecretkey00000000"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"message":"bad request, saw %s"}`, r.Header.Get("Authorization"))))
	}))
	defer srv.Close()

	cred := tokenapi.Credential{Token: token}
	sec, err := cred.Secret()
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, srv, sec)
	_, err = h.invoke(t, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("token leaked into error: %v", err)
	}
}

// ---- CheckStatus ----

func TestCheckStatusOKAndError(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"viewer":{"id":"u1"}}}`))
	}))
	defer ok.Close()
	optsOK := &tokenapi.Options{BaseURL: ok.URL, HTTPClient: ok.Client(), Sleep: noSleep}
	cred := tokenapi.Credential{Token: "lin_api_testkey1234567890"}
	sec, err := cred.Secret()
	if err != nil {
		t.Fatal(err)
	}
	if err := checkStatusWithOptions(context.Background(), sec, optsOK); err != nil {
		t.Fatalf("CheckStatus: %v", err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer bad.Close()
	optsBad := &tokenapi.Options{BaseURL: bad.URL, HTTPClient: bad.Client(), Sleep: noSleep}
	if err := checkStatusWithOptions(context.Background(), sec, optsBad); err == nil {
		t.Fatal("expected an error from a 401")
	}
}
