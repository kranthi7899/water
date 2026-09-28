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
      - {name: create_comment, level: A}
      - {name: set_issue_priority, level: A}
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
	q *approvals.Queue
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
	return &harness{g: g, q: q}
}

func (h *harness) invoke(t *testing.T, args map[string]any) (gate.Result, error) {
	t.Helper()
	return h.g.Invoke(context.Background(), gate.Call{Function: "linear.list_issues", Args: args, Origin: gate.P0, Taint: gate.Clean})
}

// approve proposes and immediately decides yes on an envelope for action
// with the given payload, mirroring internal/connectors/google/gcal_test.go's
// own helper of the same name: A-level calls in these tests reach the
// connector only through this path, exactly like the real approval flow.
func (h *harness) approve(t *testing.T, action string, payload map[string]any) approvals.Envelope {
	t.Helper()
	ctx := context.Background()
	e, err := h.q.Propose(ctx, approvals.Envelope{Action: action, Payload: payload, Origin: "p0", Risk: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	if e, err = h.q.Decide(ctx, e.ID, approvals.Yes); err != nil || e.Status != approvals.Approved {
		t.Fatalf("approve: %+v %v", e, err)
	}
	return e
}

// ---- schema ----

// TestFunctionsSchema checks every function's schema shape (closed schema,
// unknown args refused) plus the level/External split docs/slices/UI.md U4
// establishes: list_issues stays R/External (someone else's ticket data,
// read-only); create_comment/set_issue_priority are the new A-level write
// functions (an external effect the CEO's own turn originates, matching
// gmail.send_message's External:false convention -- not content read from
// someone else).
func TestFunctionsSchema(t *testing.T) {
	l := New()
	wantLevel := map[string]twins.Level{
		"list_issues":        twins.R,
		"create_comment":     twins.A,
		"set_issue_priority": twins.A,
	}
	wantExternal := map[string]bool{
		"list_issues":        true,
		"create_comment":     false,
		"set_issue_priority": false,
	}
	seen := map[string]bool{}
	for _, f := range l.Functions() {
		seen[f.Name] = true
		if want, ok := wantLevel[f.Name]; !ok {
			t.Fatalf("%s: unexpected function, add it to wantLevel/wantExternal", f.Name)
		} else if f.Level != want {
			t.Fatalf("%s: level %s, want %s", f.Name, f.Level, want)
		}
		if f.External != wantExternal[f.Name] {
			t.Fatalf("%s: External = %v, want %v", f.Name, f.External, wantExternal[f.Name])
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
	for name := range wantLevel {
		if !seen[name] {
			t.Fatalf("%s: missing from Functions()", name)
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

// ---- create_comment / set_issue_priority (docs/slices/UI.md U4) ----
//
// Both are A-level (gate-enforced, envelope-required before Invoke ever
// runs them for real), so -- matching internal/connectors/google/gmail's
// own test convention for send_message -- these call the implementation
// methods directly against a raw tokenapi.Client, not through
// harness.invoke/gate.Invoke: the gate's own approval enforcement is
// already covered by internal/gate's test suite, and this package's job is
// only to prove the GraphQL calls and output shape are correct.

// craGQLServer is a fake single-endpoint GraphQL server that dispatches by
// operation name (Linear, like every GraphQL API, has one endpoint for
// every query and mutation) -- resolving "CRA-3" to a fixed internal id,
// then recording and answering whichever mutation follows. onMutation, if
// set, can inspect the decoded request body before responding (used to
// assert on exactly what was sent).
type craGQLServer struct {
	t           *testing.T
	mutationReq map[string]any
	onMutation  func(body map[string]any)
	mutationOK  bool // false makes the mutation respond success:false
	resolveErr  bool // true makes the resolve step answer "no such issue"
}

func (s *craGQLServer) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			s.t.Fatal(err)
		}
		q, _ := body["query"].(string)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(q, "ResolveIssue"):
			if s.resolveErr {
				_, _ = w.Write([]byte(`{"data":{"issue":null}}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"issue":{"id":"uuid-cra-3","identifier":"CRA-3","url":"https://linear.app/nimbus/issue/CRA-3"}}}`))
		case strings.Contains(q, "CreateComment"), strings.Contains(q, "SetPriority"):
			s.mutationReq = body
			if s.onMutation != nil {
				s.onMutation(body)
			}
			if strings.Contains(q, "CreateComment") {
				ok := "true"
				if !s.mutationOK {
					ok = "false"
				}
				fmt.Fprintf(w, `{"data":{"commentCreate":{"success":%s,"comment":{"id":"comment-1","url":"https://linear.app/nimbus/issue/CRA-3#comment-1"}}}}`, ok)
			} else {
				ok := "true"
				if !s.mutationOK {
					ok = "false"
				}
				fmt.Fprintf(w, `{"data":{"issueUpdate":{"success":%s}}}`, ok)
			}
		default:
			s.t.Fatalf("unexpected GraphQL operation: %s", q)
		}
	}))
}

func newLinearDirectClient(t *testing.T, srv *httptest.Server) *tokenapi.Client {
	t.Helper()
	cl, err := tokenapi.New(tokenapi.Credential{Token: "lin_api_testkey1234567890"}, tokenapi.RawAuth, apiHost, &tokenapi.Options{BaseURL: srv.URL, HTTPClient: srv.Client(), Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	return cl
}

func TestCreateCommentPostsExactlyOnceAndSetsRelay(t *testing.T) {
	s := &craGQLServer{t: t, mutationOK: true}
	srv := s.server()
	defer srv.Close()
	cl := newLinearDirectClient(t, srv)

	out, err := (&Linear{}).createComment(context.Background(), cl, map[string]any{"issue": "CRA-3", "body": "Checking in on the dedup fix."})
	if err != nil {
		t.Fatal(err)
	}
	var got createCommentOutput
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Issue != "CRA-3" || got.Body != "Checking in on the dedup fix." || !got.Relay || got.Title != "Comment on CRA-3" {
		t.Fatalf("output = %+v", got)
	}
	if got.Source != "https://linear.app/nimbus/issue/CRA-3" {
		t.Fatalf("source = %q, want the issue URL", got.Source)
	}
	body, _ := s.mutationReq["variables"].(map[string]any)
	if body["issueId"] != "uuid-cra-3" {
		t.Fatalf("mutation issueId = %v, want the resolved internal id, not the identifier", body["issueId"])
	}
}

// TestCreateCommentSimulatedRelayPrefixIsConnectorControlled is U4's own
// named invariant: the model can neither omit the prefix by not asking for
// it (it can only set simulated_relay, never write the prefix itself) nor
// smuggle a different one past it -- the connector always produces exactly
// one canonical prefix.
func TestCreateCommentSimulatedRelayPrefixIsConnectorControlled(t *testing.T) {
	cases := []struct {
		name, callerBody, wantBody string
	}{
		{"plain body gets the prefix prepended", "Nina, via Slack: sounds good, ship it.", "(simulated) Relayed: Nina, via Slack: sounds good, ship it."},
		{"a caller-supplied near-miss prefix is not trusted, still gets the canonical one", "(simulated) relayed: sounds good", "(simulated) Relayed: (simulated) relayed: sounds good"},
		{"a caller-supplied exact prefix is not duplicated", "(simulated) Relayed: sounds good", "(simulated) Relayed: sounds good"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &craGQLServer{t: t, mutationOK: true}
			srv := s.server()
			defer srv.Close()
			cl := newLinearDirectClient(t, srv)
			out, err := (&Linear{}).createComment(context.Background(), cl, map[string]any{
				"issue": "CRA-3", "body": c.callerBody, "simulated_relay": true,
			})
			if err != nil {
				t.Fatal(err)
			}
			var got createCommentOutput
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			if got.Body != c.wantBody {
				t.Fatalf("body = %q, want %q", got.Body, c.wantBody)
			}
			if !strings.HasPrefix(got.Body, relayedCommentPrefix) {
				t.Fatalf("body = %q, does not start with the canonical prefix", got.Body)
			}
		})
	}
}

func TestCreateCommentWithoutSimulatedRelayNeverGetsThePrefix(t *testing.T) {
	s := &craGQLServer{t: t, mutationOK: true}
	srv := s.server()
	defer srv.Close()
	cl := newLinearDirectClient(t, srv)
	out, err := (&Linear{}).createComment(context.Background(), cl, map[string]any{"issue": "CRA-3", "body": "Fixed in the latest deploy."})
	if err != nil {
		t.Fatal(err)
	}
	var got createCommentOutput
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(got.Body, relayedCommentPrefix) {
		t.Fatalf("body = %q, an ordinary comment must never get the simulated prefix", got.Body)
	}
}

func TestCreateCommentUnknownIssueRefusesClearly(t *testing.T) {
	s := &craGQLServer{t: t, resolveErr: true}
	srv := s.server()
	defer srv.Close()
	cl := newLinearDirectClient(t, srv)
	if _, err := (&Linear{}).createComment(context.Background(), cl, map[string]any{"issue": "CRA-999", "body": "x"}); err == nil {
		t.Fatal("expected an error for an unknown issue")
	}
}

func TestCreateCommentMutationFailureRefusesClearly(t *testing.T) {
	s := &craGQLServer{t: t, mutationOK: false}
	srv := s.server()
	defer srv.Close()
	cl := newLinearDirectClient(t, srv)
	if _, err := (&Linear{}).createComment(context.Background(), cl, map[string]any{"issue": "CRA-3", "body": "x"}); err == nil {
		t.Fatal("expected an error when commentCreate reports success:false")
	}
}

func TestSetIssuePriorityUpdatesByResolvedIDAndReturnsTheLabel(t *testing.T) {
	s := &craGQLServer{t: t, mutationOK: true}
	srv := s.server()
	defer srv.Close()
	cl := newLinearDirectClient(t, srv)

	out, err := (&Linear{}).setIssuePriority(context.Background(), cl, map[string]any{"issue": "CRA-3", "priority": "urgent"})
	if err != nil {
		t.Fatal(err)
	}
	var got setIssuePriorityOutput
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Issue != "CRA-3" || got.Priority != "Urgent" {
		t.Fatalf("output = %+v", got)
	}
	body, _ := s.mutationReq["variables"].(map[string]any)
	if body["id"] != "uuid-cra-3" {
		t.Fatalf("mutation id = %v, want the resolved internal id", body["id"])
	}
	if rank, _ := body["priority"].(float64); int(rank) != 1 {
		t.Fatalf("mutation priority = %v, want 1 (Urgent)", body["priority"])
	}
}

func TestSetIssuePriorityRejectsAnUnknownLabel(t *testing.T) {
	s := &craGQLServer{t: t, mutationOK: true}
	srv := s.server()
	defer srv.Close()
	cl := newLinearDirectClient(t, srv)
	if _, err := (&Linear{}).setIssuePriority(context.Background(), cl, map[string]any{"issue": "CRA-3", "priority": "super urgent"}); err == nil {
		t.Fatal("expected an error for an unrecognized priority label")
	}
	if s.mutationReq != nil {
		t.Fatal("no mutation should have been sent for an invalid label")
	}
}

// TestGateInvokeRefusesCreateCommentWithoutAnApprovedEnvelope is CLAUDE.md's
// own outward-action invariant, proven for this connector specifically: an
// A-level call with no approved envelope must be refused before Invoke's
// dispatch ever reaches createComment, so no request should hit the server
// at all.
func TestGateInvokeRefusesCreateCommentWithoutAnApprovedEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no request should ever reach Linear without an approved envelope")
	}))
	defer srv.Close()
	h := newHarness(t, srv, testCredential(t))
	_, err := h.g.Invoke(context.Background(), gate.Call{Function: "linear.create_comment", Args: map[string]any{"issue": "CRA-3", "body": "x"}, Origin: gate.P0, Taint: gate.Clean})
	if err == nil {
		t.Fatal("expected the gate to refuse an unapproved A-level call")
	}
}

// TestGateInvokeDispatchesCreateCommentAndSetIssuePriority proves Invoke's
// switch statement routes each function name to its own implementation,
// through the real gate/approval path exactly as production traffic would
// (propose, approve, then Invoke with the resulting envelope id) -- not
// just the implementation methods called directly, which the tests above
// already cover in detail.
func TestGateInvokeDispatchesCreateCommentAndSetIssuePriority(t *testing.T) {
	s := &craGQLServer{t: t, mutationOK: true}
	srv := s.server()
	defer srv.Close()
	h := newHarness(t, srv, testCredential(t))

	commentArgs := map[string]any{"issue": "CRA-3", "body": "Checking in on the dedup fix."}
	e := h.approve(t, "linear.create_comment", commentArgs)
	res, err := h.g.Invoke(context.Background(), gate.Call{Function: "linear.create_comment", Args: commentArgs, Origin: gate.P0, Taint: gate.Clean, EnvelopeID: e.ID})
	if err != nil {
		t.Fatalf("create_comment: %v", err)
	}
	var comment createCommentOutput
	if err := json.Unmarshal(res.Output, &comment); err != nil || comment.Issue != "CRA-3" {
		t.Fatalf("create_comment output: %v %+v", err, comment)
	}

	priorityArgs := map[string]any{"issue": "CRA-3", "priority": "Urgent"}
	e = h.approve(t, "linear.set_issue_priority", priorityArgs)
	res, err = h.g.Invoke(context.Background(), gate.Call{Function: "linear.set_issue_priority", Args: priorityArgs, Origin: gate.P0, Taint: gate.Clean, EnvelopeID: e.ID})
	if err != nil {
		t.Fatalf("set_issue_priority: %v", err)
	}
	var priority setIssuePriorityOutput
	if err := json.Unmarshal(res.Output, &priority); err != nil || priority.Priority != "Urgent" {
		t.Fatalf("set_issue_priority output: %v %+v", err, priority)
	}
}
