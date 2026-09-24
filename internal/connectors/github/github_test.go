package github

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
  - name: github
    functions:
      - {name: list_prs, level: R}
      - {name: list_issues, level: R}
`

func noSleep(context.Context, time.Duration) error { return nil }

func testCredential(t *testing.T) vault.Secret {
	t.Helper()
	cred := tokenapi.Credential{Token: "ghp_testtoken1234567890"}
	sec, err := cred.Secret()
	if err != nil {
		t.Fatal(err)
	}
	return sec
}

type harness struct {
	g  *gate.Gate
	st *store.Store
}

func newHarness(t *testing.T, srv *httptest.Server, repo string, tok vault.Secret) *harness {
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
	conn := NewWithOptions(repo, opts)
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
	return &harness{g: g, st: st}
}

func (h *harness) invoke(t *testing.T, fn string, args map[string]any) (gate.Result, error) {
	t.Helper()
	return h.g.Invoke(context.Background(), gate.Call{Function: "github." + fn, Args: args, Origin: gate.P0, Taint: gate.Clean})
}

// ---- schema ----

func TestFunctionsSchema(t *testing.T) {
	gh := New("owner/repo")
	for _, f := range gh.Functions() {
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

// ---- pagination via Link header ----

func TestListPRsFollowsLinkHeaderPagination(t *testing.T) {
	pages := [][]wirePR{
		{{ID: 1, Number: 10, Title: "first page pr", State: "open", User: wireUser{Login: "alice"}, HTMLURL: "https://github.com/owner/repo/pull/10", UpdatedAt: "2026-09-20T10:00:00Z"}},
		{{ID: 2, Number: 11, Title: "second page pr", State: "closed", Merged: true, User: wireUser{Login: "bob"}, HTMLURL: "https://github.com/owner/repo/pull/11", UpdatedAt: "2026-09-21T10:00:00Z"}},
	}
	var srv *httptest.Server
	calls := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghp_testtoken1234567890" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("missing Accept header")
		}
		page := 0
		if r.URL.Query().Get("page2") == "1" {
			page = 1
		}
		w.Header().Set("X-RateLimit-Remaining", "4999")
		if page == 0 {
			next := srv.URL + "/repos/owner/repo/pulls?page2=1"
			w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next", <%s>; rel="last"`, next, next))
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pages[page])
	}))
	defer srv.Close()

	h := newHarness(t, srv, "owner/repo", testCredential(t))
	res, err := h.invoke(t, "list_prs", nil)
	if err != nil {
		t.Fatal(err)
	}
	var prs []PR
	if err := json.Unmarshal(res.Output, &prs); err != nil {
		t.Fatal(err)
	}
	if len(prs) != 2 {
		t.Fatalf("got %d PRs across pages, want 2 (calls=%d)", len(prs), calls)
	}
	if prs[0].Number != 10 || prs[1].Number != 11 {
		t.Fatalf("PRs out of order or wrong: %+v", prs)
	}
	if prs[1].State != "merged" {
		t.Fatalf("merged PR state = %q, want merged", prs[1].State)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (one per page)", calls)
	}
	if !res.Untrusted {
		t.Fatal("github.list_prs result must be marked untrusted (External)")
	}
}

// ---- 401 ----

func TestInvokeReturns401AsAClearAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()

	h := newHarness(t, srv, "owner/repo", testCredential(t))
	_, err := h.invoke(t, "list_prs", nil)
	if err == nil {
		t.Fatal("expected an error on 401")
	}
	if tokenapi.Status(err) != http.StatusUnauthorized && !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected a 401 surfaced clearly, got: %v", err)
	}
}

// ---- not connected ----

func TestInvokeWithNoTokenFailsClearly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no request should ever be made without a token")
	}))
	defer srv.Close()

	h := newHarness(t, srv, "owner/repo", vault.Secret{})
	_, err := h.invoke(t, "list_prs", nil)
	if err == nil {
		t.Fatal("expected an error with no token configured")
	}
}

// ---- rate limit ----

func TestRateLimitExhaustedSurfacesClearErrorNotLoop(t *testing.T) {
	calls := 0
	reset := time.Now().Add(30 * time.Minute).Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", reset))
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	defer srv.Close()

	h := newHarness(t, srv, "owner/repo", testCredential(t))
	_, err := h.invoke(t, "list_prs", nil)
	if err == nil {
		t.Fatal("expected a rate-limit error")
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("error should clearly say rate limit: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want exactly 1 (no retry loop on exhausted rate limit)", calls)
	}
}

// ---- issues: filters + excludes PRs ----

func TestListIssuesFiltersAndExcludesPRs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("state"); got != "open" {
			t.Errorf("state query = %q, want open", got)
		}
		if got := r.URL.Query().Get("assignee"); got != "alice" {
			t.Errorf("assignee query = %q, want alice", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]wireIssue{
			{ID: 1, Number: 5, Title: "a real issue", State: "open", User: wireUser{Login: "alice"}, Assignee: &wireUser{Login: "alice"}, Labels: []wireLabel{{Name: "bug"}}, HTMLURL: "https://github.com/owner/repo/issues/5", UpdatedAt: "2026-09-20T10:00:00Z"},
			{ID: 2, Number: 6, Title: "actually a pr", State: "open", User: wireUser{Login: "alice"}, HTMLURL: "https://github.com/owner/repo/issues/6", UpdatedAt: "2026-09-20T10:00:00Z", PullRequest: &struct{}{}},
		})
	}))
	defer srv.Close()

	h := newHarness(t, srv, "owner/repo", testCredential(t))
	res, err := h.invoke(t, "list_issues", map[string]any{"state": "open", "assignee": "alice"})
	if err != nil {
		t.Fatal(err)
	}
	var issues []Issue
	if err := json.Unmarshal(res.Output, &issues); err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1 (PR must be excluded): %+v", len(issues), issues)
	}
	if issues[0].Number != 5 {
		t.Fatalf("issue = %+v", issues[0])
	}
}

// ---- Normalize ----

func TestNormalizePRsAndIssues(t *testing.T) {
	gh := New("owner/repo")

	prs := []PR{{ID: "1", Number: 10, Title: "fix bug", Author: "alice", State: "merged", URL: "https://github.com/owner/repo/pull/10", UpdatedAt: "2026-09-20T10:00:00Z"}}
	prRaw, err := json.Marshal(prs)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := gh.Normalize("list_prs", prRaw)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("normalize list_prs: %d records, want 1", len(recs))
	}
	c, ok := recs[0].(*store.Commit)
	if !ok {
		t.Fatalf("PR did not normalize to *store.Commit: %T", recs[0])
	}
	if !c.External || c.Author != "alice" || c.Repo != "owner/repo" || !strings.Contains(c.Message, "fix bug") {
		t.Fatalf("commit from PR: %+v", c)
	}

	issues := []Issue{{ID: "2", Number: 5, Title: "a bug", Author: "bob", State: "open", Assignee: "alice", Labels: []string{"bug", "p1"}, URL: "https://github.com/owner/repo/issues/5"}}
	issueRaw, err := json.Marshal(issues)
	if err != nil {
		t.Fatal(err)
	}
	recs, err = gh.Normalize("list_issues", issueRaw)
	if err != nil {
		t.Fatal(err)
	}
	is, ok := recs[0].(*store.Issue)
	if !ok {
		t.Fatalf("issue did not normalize to *store.Issue: %T", recs[0])
	}
	if !is.External || is.Assignee != "alice" || is.State != "open" || is.Project != "owner/repo" {
		t.Fatalf("issue: %+v", is)
	}
}

// ---- secrets never leak ----

func TestTokenNeverAppearsInError(t *testing.T) {
	const token = "ghp_supersecrettoken0000000000"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo back the Authorization header in the error body, simulating a
		// server that might misbehave; scrub must still catch it.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"message":"bad request, saw %s"}`, r.Header.Get("Authorization"))))
	}))
	defer srv.Close()

	cred := tokenapi.Credential{Token: token}
	sec, err := cred.Secret()
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, srv, "owner/repo", sec)
	_, err = h.invoke(t, "list_prs", nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("token leaked into error: %v", err)
	}
}

// ---- no repo configured ----

func TestInvokeWithNoRepoConfigured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no request should be made with no repo configured")
	}))
	defer srv.Close()
	h := newHarness(t, srv, "", testCredential(t))
	_, err := h.invoke(t, "list_prs", nil)
	if err == nil || !strings.Contains(err.Error(), "no repo configured") {
		t.Fatalf("expected a clear no-repo error, got: %v", err)
	}
}
