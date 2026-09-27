package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	_ "modernc.org/sqlite"

	"water/internal/connectors/github"
	"water/internal/connectors/linear"
	"water/internal/dashboards"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/workspaces"
)

// ---- a minimal dashboards.Invoker double, mirroring internal/dashboards'
// own compute_test.go fakeInvoker (a different package, so redefined here
// rather than imported) -- this is what lets workspace_detail.go's project/
// finance/clients tiles be tested with exact fixture values without a real
// gate, vault, registry or connector at all. ----

type wsFakeInvoker struct {
	mu       sync.Mutex
	calls    map[string]int
	handlers map[string]func(args map[string]any) (gate.Result, error)
}

func newWSFakeInvoker() *wsFakeInvoker {
	return &wsFakeInvoker{calls: map[string]int{}, handlers: map[string]func(map[string]any) (gate.Result, error){}}
}

func (f *wsFakeInvoker) on(fn string, h func(args map[string]any) (gate.Result, error)) *wsFakeInvoker {
	f.handlers[fn] = h
	return f
}

func (f *wsFakeInvoker) Invoke(_ context.Context, c gate.Call) (gate.Result, error) {
	f.mu.Lock()
	f.calls[c.Function]++
	f.mu.Unlock()
	h, ok := f.handlers[c.Function]
	if !ok {
		return gate.Result{}, fmt.Errorf("wsFakeInvoker: no handler registered for %s", c.Function)
	}
	return h(c.Args)
}

func (f *wsFakeInvoker) callCount(fn string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[fn]
}

func wsJSONResult(v any) (gate.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return gate.Result{}, err
	}
	return gate.Result{Output: b, Untrusted: true}, nil
}

func wsNotConnected(conn string) (gate.Result, error) {
	return gate.Result{}, &gate.DenyError{Reason: "credential for " + conn + " is unavailable"}
}

func wsFixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

const wsTestNow = "2026-09-27T00:00:00Z"

// wsWorkspacesFixture is every workspace spec these tests share:
//   - "crawler-nogh": a project workspace with no github: source at all
//     (the plan's first Connect-GitHub trigger).
//   - "crawler-gh": a project workspace whose source IS github:acme/api
//     (the second trigger is exercised by denying the call in the test
//     itself, not by the spec).
//   - "finance": the finance-template workspace, mirroring
//     twins/ceo/workspaces/finance.yaml.
//   - "clients": the clients-template workspace, mirroring
//     twins/ceo/workspaces/clients.yaml.
//   - "people": the people-template workspace (Phase 5b), mirroring
//     twins/ceo/workspaces/people.yaml.
//   - "marketing": the marketing-template workspace (Phase 5d), mirroring
//     twins/ceo/workspaces/marketing.yaml.
func wsWorkspacesFixture(t *testing.T) *workspaces.Registry {
	t.Helper()
	fsys := fstest.MapFS{
		"twins/t/workspaces/crawler-nogh.yaml": &fstest.MapFile{Data: []byte(`
id: crawler-nogh
name: Crawler
template: project
source: linear_team:CRA
teams: [CRA]
`)},
		"twins/t/workspaces/crawler-gh.yaml": &fstest.MapFile{Data: []byte(`
id: crawler-gh
name: Crawler (GitHub)
template: project
source: github:acme/api
teams: [CRA]
`)},
		"twins/t/workspaces/finance.yaml": &fstest.MapFile{Data: []byte(`
id: finance
name: Finance
template: finance
source: company_finance
`)},
		"twins/t/workspaces/clients.yaml": &fstest.MapFile{Data: []byte(`
id: clients
name: Clients
template: clients
source: company_customers
clients: all
`)},
		"twins/t/workspaces/people.yaml": &fstest.MapFile{Data: []byte(`
id: people
name: People
template: people
source: roster
`)},
		"twins/t/workspaces/marketing.yaml": &fstest.MapFile{Data: []byte(`
id: marketing
name: Marketing
template: marketing
source: company_customers
`)},
	}
	reg, err := workspaces.LoadRegistry(fsys, "t")
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// wsDashboardsFixture mirrors twins/ceo/dashboards/finance.yaml and
// clients.yaml exactly (docs/slices/UI.md Phase 4's own specs), so
// financeWorkspaceView/clientsWorkspaceView's "reuse the Finance/Clients
// dashboard's own tiles directly" has a real dashboards.Registry to look
// "finance"/"clients" up in.
func wsDashboardsFixture(t *testing.T) *dashboards.Registry {
	t.Helper()
	fsys := fstest.MapFS{
		"twins/t/dashboards/finance.yaml": &fstest.MapFile{Data: []byte(`
name: Finance
source: company_finance
metrics: [cash_position, monthly_burn, runway_months]
breakdown: spend_by_application
callout: worst_app_margin
`)},
		"twins/t/dashboards/clients.yaml": &fstest.MapFile{Data: []byte(`
name: Clients
source: company_customers
metrics: [accounts_at_risk, open_tickets, nps_score]
breakdown: accounts_by_health
callout: longest_silent_account_with_balance
`)},
	}
	reg, err := dashboards.LoadRegistry(fsys, "t")
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func wsGet(t *testing.T, srvURL, path, token string) (int, map[string]any) {
	t.Helper()
	resp := do(t, srvURL, "GET", path, "", token)
	defer resp.Body.Close()
	var out map[string]any
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
	return resp.StatusCode, out
}

func wsPost(t *testing.T, srvURL, path, body, token string) (int, map[string]any) {
	t.Helper()
	resp := do(t, srvURL, "POST", path, body, token)
	defer resp.Body.Close()
	var out map[string]any
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
	return resp.StatusCode, out
}

// ---- the core invariant: no workspace route can ever create a decision ----

// wsDecisionTableCounts counts every row in the four tables no workspace
// control may ever write to, by opening a second, read-only connection to
// the same on-disk SQLite file newHarness already built (t.TempDir()-based,
// never ~/.water) -- a "spy/counting store" that needs no change to
// internal/store, which this phase's own scope keeps untouched.
func wsDecisionTableCounts(t *testing.T, dbPath string) map[string]int {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	out := map[string]int{}
	for _, table := range []string{"card_states", "card_action_states", "decision_classifications", "decision_records"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatalf("counting %s: %v", table, err)
		}
		out[table] = n
	}
	return out
}

// TestWorkspaceDetailRoutesNeverWriteADecision is docs/slices/UI.md Phase
// 5's acceptance item, scoped to this sub-phase's one new route: a table
// test enumerating every route this phase adds under /v1/workspaces,
// invoked with a valid request, asserting zero writes to card_states/
// card_action_states/decision_classifications/decision_records. h.d.cfg.
// Decisions is also left nil (newHarness never sets it), so there is no
// decisions.Trigger reachable from these calls even indirectly.
func TestWorkspaceDetailRoutesNeverWriteADecision(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now, _ := time.Parse(time.RFC3339, wsTestNow)
	inv := newWSFakeInvoker().
		on("linear.list_issues", func(map[string]any) (gate.Result, error) { return wsJSONResult([]linear.Issue{}) }).
		on("company_finance.cash_position", func(map[string]any) (gate.Result, error) {
			return wsJSONResult(map[string]any{"values": map[string]any{}})
		}).
		on("company_finance.spend_breakdown_all", func(map[string]any) (gate.Result, error) {
			return wsJSONResult(map[string]any{"rows": [][]any{}})
		}).
		on("company_finance.monthly_costs", func(map[string]any) (gate.Result, error) {
			return wsJSONResult(map[string]any{"rows": [][]any{}})
		}).
		on("company_finance.outstanding_invoices", func(map[string]any) (gate.Result, error) {
			return wsJSONResult(map[string]any{"rows": [][]any{}})
		}).
		on("company_customers.accounts", func(map[string]any) (gate.Result, error) {
			return wsJSONResult(map[string]any{"rows": [][]any{}})
		})
	h.d.cfg.Workspaces = wsWorkspacesFixture(t)
	h.d.cfg.Dashboards = wsDashboardsFixture(t)
	h.d.cfg.Compute = &dashboards.Compute{Gate: inv, Cache: &dashboards.Cache{TTL: dashboards.DefaultCacheTTL}, Now: wsFixedClock(now)}

	// Phase 5b: a roster team, so POST /v1/workspaces/people/drafts below
	// actually reaches store.CreateDraft (a drafts-table write) instead of
	// being refused at the team lookup -- the point of this test is to
	// prove that write, like every other route here, never touches the
	// four decision tables, not merely that a 400 also doesn't.
	if err := h.st.Upsert(ctx, &store.Team{Meta: store.Meta{Source: "seed", SourceID: "crawler"}, Name: "Crawler", LinearKey: "CRA"}); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(h.dir, "water.db")
	before := wsDecisionTableCounts(t, dbPath)

	table := []struct {
		name, method, path, body string
	}{
		{"project, no github source", "GET", "/v1/workspaces/crawler-nogh", ""},
		{"project, github source", "GET", "/v1/workspaces/crawler-gh", ""},
		{"finance", "GET", "/v1/workspaces/finance", ""},
		{"clients", "GET", "/v1/workspaces/clients", ""},
		{"people", "GET", "/v1/workspaces/people", ""},
		{"people team_message draft", "POST", "/v1/workspaces/people/drafts", `{"kind":"team_message","team":"crawler"}`},
		{"people pulse_check draft", "POST", "/v1/workspaces/people/drafts", `{"kind":"pulse_check","team":"crawler"}`},
		{"people draft, unknown kind", "POST", "/v1/workspaces/people/drafts", `{"kind":"newsletter","team":"crawler"}`},
		{"marketing", "GET", "/v1/workspaces/marketing", ""},
		{"marketing prospect_outreach draft, unknown prospect", "POST", "/v1/workspaces/marketing/drafts", `{"kind":"prospect_outreach","prospect_id":"acme"}`},
		{"marketing review_reply draft", "POST", "/v1/workspaces/marketing/drafts", `{"kind":"review_reply","review_id":"rev-1"}`},
		{"unknown id (404)", "GET", "/v1/workspaces/does-not-exist", ""},
	}
	for _, tc := range table {
		t.Run(tc.name, func(t *testing.T) {
			resp := do(t, h.srv.URL, tc.method, tc.path, tc.body, h.token)
			resp.Body.Close()
		})
	}

	after := wsDecisionTableCounts(t, dbPath)
	for table, n := range before {
		if after[table] != n {
			t.Errorf("%s: rows went from %d to %d -- a workspace route must never write here", table, n, after[table])
		}
	}
}

// TestWorkspaceDetailFileNeverReferencesDecisionWrites is the static
// counterpart: a source-scan of workspace_detail.go's own AST (mirroring
// internal/guards' AST-scan style and internal/dashboards'
// TestComputeHasNoHardcodedMetricValues), asserting the file neither
// imports water/internal/decisions nor references Trigger/SetCardState/
// SetCardActionState/SetDecisionClassification/UpsertDecisionRecord by
// name anywhere.
func TestWorkspaceDetailFileNeverReferencesDecisionWrites(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "workspace_detail.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range f.Imports {
		if strings.Contains(imp.Path.Value, "internal/decisions") {
			t.Errorf("workspace_detail.go imports %s, which this file must never depend on", imp.Path.Value)
		}
	}
	banned := map[string]bool{
		"Trigger": true, "SetCardState": true, "SetCardActionState": true,
		"SetDecisionClassification": true, "UpsertDecisionRecord": true,
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && banned[id.Name] {
			t.Errorf("workspace_detail.go references %q, which this file must never call (the no-decision invariant)", id.Name)
		}
		return true
	})
}

// ---- Connect GitHub: both trigger conditions ----

func TestConnectGitHubWhenWorkspaceHasNoGitHubSource(t *testing.T) {
	h := newHarness(t)
	now, _ := time.Parse(time.RFC3339, wsTestNow)
	inv := newWSFakeInvoker().on("linear.list_issues", func(map[string]any) (gate.Result, error) { return wsJSONResult([]linear.Issue{}) })
	h.d.cfg.Workspaces = wsWorkspacesFixture(t)
	h.d.cfg.Compute = &dashboards.Compute{Gate: inv, Cache: &dashboards.Cache{TTL: dashboards.DefaultCacheTTL}, Now: wsFixedClock(now)}

	status, body := wsGet(t, h.srv.URL, "/v1/workspaces/crawler-nogh", h.token)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	project := body["project"].(map[string]any)
	for _, tile := range []string{"open_prs", "merged_this_week"} {
		got := project[tile].(map[string]any)
		if got["state"] != "not_connected" {
			t.Errorf("%s.state = %v, want not_connected", tile, got["state"])
		}
		if _, hasValue := got["value"]; hasValue {
			t.Errorf("%s carries a value %v even though not_connected", tile, got["value"])
		}
	}
	bars := project["commit_bars"].(map[string]any)
	if bars["state"] != "not_connected" {
		t.Errorf("commit_bars.state = %v, want not_connected", bars["state"])
	}
	if _, hasWeeks := bars["weeks"]; hasWeeks {
		t.Errorf("commit_bars carries weeks %v even though not_connected", bars["weeks"])
	}
	if n := inv.callCount("github.list_prs") + inv.callCount("github.list_commits"); n != 0 {
		t.Errorf("a workspace with no github: source made %d github calls, want 0", n)
	}
}

func TestConnectGitHubWhenTheCallIsDeniedNotConnected(t *testing.T) {
	h := newHarness(t)
	now, _ := time.Parse(time.RFC3339, wsTestNow)
	inv := newWSFakeInvoker().
		on("linear.list_issues", func(map[string]any) (gate.Result, error) { return wsJSONResult([]linear.Issue{}) }).
		on("github.list_prs", func(map[string]any) (gate.Result, error) { return wsNotConnected("github") }).
		on("github.list_commits", func(map[string]any) (gate.Result, error) { return wsNotConnected("github") })
	h.d.cfg.Workspaces = wsWorkspacesFixture(t)
	h.d.cfg.Compute = &dashboards.Compute{Gate: inv, Cache: &dashboards.Cache{TTL: dashboards.DefaultCacheTTL}, Now: wsFixedClock(now)}

	status, body := wsGet(t, h.srv.URL, "/v1/workspaces/crawler-gh", h.token)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	project := body["project"].(map[string]any)
	for _, tile := range []string{"open_prs", "merged_this_week"} {
		got := project[tile].(map[string]any)
		if got["state"] != "not_connected" {
			t.Errorf("%s.state = %v, want not_connected", tile, got["state"])
		}
		if _, hasValue := got["value"]; hasValue {
			t.Errorf("%s carries a value %v even though not_connected", tile, got["value"])
		}
	}
	bars := project["commit_bars"].(map[string]any)
	if bars["state"] != "not_connected" {
		t.Errorf("commit_bars.state = %v, want not_connected", bars["state"])
	}
	if inv.callCount("github.list_prs") == 0 || inv.callCount("github.list_commits") == 0 {
		t.Error("a workspace WITH a github: source must actually call github.list_prs/list_commits (and get denied), not skip the call")
	}
}

// ---- exact fixture values: project workspace ----

func TestProjectWorkspaceExactFixtureValues(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now, _ := time.Parse(time.RFC3339, wsTestNow)

	// Roster: team "crawler" (linear key CRA) with two projects, so the
	// progress bar's TargetAt must pick the earliest of the two
	// (2026-11-15), never the later one (2026-12-01) -- the judgment call
	// projectEarliestTarget's own doc comment explains.
	mustLink := func(l store.Link) {
		t.Helper()
		if err := h.st.AddLink(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.st.Upsert(ctx, &store.Team{Meta: store.Meta{Source: "seed", SourceID: "crawler"}, Name: "Crawler", LinearKey: "CRA"}); err != nil {
		t.Fatal(err)
	}
	target1, _ := time.Parse("2006-01-02", "2026-11-15")
	target2, _ := time.Parse("2006-01-02", "2026-12-01")
	if err := h.st.Upsert(ctx, &store.Project{Meta: store.Meta{Source: "seed", SourceID: "ingestion-v2"}, Name: "Ingestion v2", TargetAt: target1}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.Upsert(ctx, &store.Project{Meta: store.Meta{Source: "seed", SourceID: "ranking-quality"}, Name: "Ranking quality", TargetAt: target2}); err != nil {
		t.Fatal(err)
	}
	mustLink(store.Link{Kind: store.LinkMemberOf, FromType: "project", FromID: "ingestion-v2", ToType: "team", ToID: "crawler"})
	mustLink(store.Link{Kind: store.LinkMemberOf, FromType: "project", FromID: "ranking-quality", ToType: "team", ToID: "crawler"})

	issues := []linear.Issue{
		{Identifier: "CRA-1", Team: "CRA", StateType: "completed"},
		{Identifier: "CRA-2", Team: "CRA", StateType: "started", Title: "Speed up the crawl loop"},
		{Identifier: "CRA-3", Team: "CRA", StateType: "started", Title: "Dedup pipeline"},
		{Identifier: "CRA-4", Team: "CRA", StateType: "unstarted", InverseRelations: []linear.Relation{{Type: "blocks", Identifier: "CRA-1", StateType: "completed"}}},
		{Identifier: "CRA-5", Team: "CRA", StateType: "unstarted", InverseRelations: []linear.Relation{{Type: "blocks", Identifier: "CRA-2", StateType: "started"}}},
		{Identifier: "WAT-9", Team: "WAT", StateType: "started"}, // a different team: must never be counted
	}
	prs := []github.PR{
		{ID: "1", Number: 1, State: "open", UpdatedAt: now.Add(-2 * 24 * time.Hour).Format(time.RFC3339)},
		{ID: "2", Number: 2, State: "merged", UpdatedAt: now.Add(-3 * 24 * time.Hour).Format(time.RFC3339)},  // within the last week: counted
		{ID: "3", Number: 3, State: "merged", UpdatedAt: now.Add(-10 * 24 * time.Hour).Format(time.RFC3339)}, // outside the last week: not counted
		{ID: "4", Number: 4, State: "closed", UpdatedAt: now.Add(-1 * 24 * time.Hour).Format(time.RFC3339)},  // neither open nor merged
	}
	commits := []github.Commit{
		{SHA: "a", CommittedAt: now.Add(-2 * 24 * time.Hour).Format(time.RFC3339)},  // bucket 3 (last 7d)
		{SHA: "b", CommittedAt: now.Add(-9 * 24 * time.Hour).Format(time.RFC3339)},  // bucket 2
		{SHA: "c", CommittedAt: now.Add(-16 * 24 * time.Hour).Format(time.RFC3339)}, // bucket 1
		{SHA: "d", CommittedAt: now.Add(-25 * 24 * time.Hour).Format(time.RFC3339)}, // bucket 0
		{SHA: "e", CommittedAt: now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)}, // outside the 4-week window: dropped
	}
	inv := newWSFakeInvoker().
		on("linear.list_issues", func(map[string]any) (gate.Result, error) { return wsJSONResult(issues) }).
		on("github.list_prs", func(map[string]any) (gate.Result, error) { return wsJSONResult(prs) }).
		on("github.list_commits", func(map[string]any) (gate.Result, error) { return wsJSONResult(commits) })
	h.d.cfg.Workspaces = wsWorkspacesFixture(t)
	h.d.cfg.Compute = &dashboards.Compute{Gate: inv, Cache: &dashboards.Cache{TTL: dashboards.DefaultCacheTTL}, Now: wsFixedClock(now)}

	status, body := wsGet(t, h.srv.URL, "/v1/workspaces/crawler-gh", h.token)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	project := body["project"].(map[string]any)

	progress := project["progress"].(map[string]any)
	if progress["state"] != "ok" || progress["done"].(float64) != 1 || progress["total"].(float64) != 5 {
		t.Fatalf("progress = %+v, want state ok, done 1, total 5", progress)
	}
	if got := progress["target_at"].(string); !strings.HasPrefix(got, "2026-11-15") {
		t.Fatalf("progress.target_at = %q, want the earliest project target (2026-11-15), not the later one", got)
	}

	blocked := project["blocked_issues"].(map[string]any)
	if blocked["state"] != "ok" || blocked["value"].(float64) != 1 {
		t.Fatalf("blocked_issues = %+v, want state ok, value 1 (only CRA-5)", blocked)
	}

	building := project["building_now"].(map[string]any)
	items := building["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("building_now items = %+v, want exactly CRA-2 and CRA-3", items)
	}
	if items[0].(map[string]any)["identifier"] != "CRA-2" || items[1].(map[string]any)["identifier"] != "CRA-3" {
		t.Fatalf("building_now items = %+v, want [CRA-2, CRA-3] in identifier order", items)
	}

	openPRs := project["open_prs"].(map[string]any)
	if openPRs["state"] != "ok" || openPRs["value"].(float64) != 1 {
		t.Fatalf("open_prs = %+v, want state ok, value 1", openPRs)
	}
	merged := project["merged_this_week"].(map[string]any)
	if merged["state"] != "ok" || merged["value"].(float64) != 1 {
		t.Fatalf("merged_this_week = %+v, want state ok, value 1 (only PR #2, within 7 days)", merged)
	}

	bars := project["commit_bars"].(map[string]any)
	if bars["state"] != "ok" {
		t.Fatalf("commit_bars.state = %v, want ok", bars["state"])
	}
	weeks := bars["weeks"].([]any)
	want := []float64{1, 1, 1, 1}
	if len(weeks) != 4 {
		t.Fatalf("commit_bars.weeks = %+v, want 4 entries", weeks)
	}
	for i, w := range want {
		if weeks[i].(float64) != w {
			t.Fatalf("commit_bars.weeks = %+v, want %+v (the 30-day-old commit dropped)", weeks, want)
		}
	}
}

// ---- exact fixture values: finance workspace ----

func TestFinanceWorkspaceExactFixtureValues(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now, _ := time.Parse(time.RFC3339, wsTestNow)

	if err := h.st.Upsert(ctx, &store.Vendor{Meta: store.Meta{Source: "seed", SourceID: "aws"}, Name: "AWS", Product: "cloud", MonthlyMinor: 500000, RenewalAt: mustDate(t, "2026-10-01")}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.Upsert(ctx, &store.Vendor{Meta: store.Meta{Source: "seed", SourceID: "dd"}, Name: "Datadog", Product: "observability", MonthlyMinor: 100000, RenewalAt: mustDate(t, "2026-12-01")}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.Upsert(ctx, &store.Vendor{Meta: store.Meta{Source: "seed", SourceID: "no-renewal"}, Name: "NoRenewalCo", Product: "misc", MonthlyMinor: 20000}); err != nil {
		t.Fatal(err)
	}

	inv := newWSFakeInvoker().
		on("company_finance.cash_position", func(map[string]any) (gate.Result, error) {
			return wsJSONResult(map[string]any{"values": map[string]any{"cash_usd": 250000.0, "burn_usd": 40000.0, "runway_months": 6.25}})
		}).
		on("company_finance.spend_breakdown_all", func(map[string]any) (gate.Result, error) {
			return wsJSONResult(map[string]any{"rows": [][]any{}})
		}).
		on("company_finance.monthly_costs", func(map[string]any) (gate.Result, error) {
			return wsJSONResult(map[string]any{"rows": [][]any{}})
		}).
		on("company_finance.outstanding_invoices", func(map[string]any) (gate.Result, error) {
			return wsJSONResult(map[string]any{"rows": [][]any{
				{"inv-1", "Acme", 5000.0, "2026-08-01", "Open"},
				{"inv-2", "Beta", 1200.0, "2026-09-20", "Open"},
			}})
		})
	h.d.cfg.Workspaces = wsWorkspacesFixture(t)
	h.d.cfg.Dashboards = wsDashboardsFixture(t)
	h.d.cfg.Compute = &dashboards.Compute{Gate: inv, Cache: &dashboards.Cache{TTL: dashboards.DefaultCacheTTL}, Now: wsFixedClock(now)}

	status, body := wsGet(t, h.srv.URL, "/v1/workspaces/finance", h.token)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	fin := body["finance"].(map[string]any)

	dv := fin["dashboard"].(map[string]any)
	metrics := dv["metrics"].([]any)
	if len(metrics) != 3 {
		t.Fatalf("dashboard.metrics = %+v, want 3 (reused directly from the Finance dashboard's own compute)", metrics)
	}
	cash := metrics[0].(map[string]any)
	if cash["id"] != "cash_position" || cash["value"].(float64) != 250000 || cash["state"] != "ok" {
		t.Fatalf("dashboard.metrics[0] = %+v, want cash_position 250000 ok", cash)
	}

	invoices := fin["invoices"].(map[string]any)
	if invoices["state"] != "ok" {
		t.Fatalf("invoices.state = %v", invoices["state"])
	}
	items := invoices["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("invoices.items = %+v, want 2", items)
	}
	// Sorted by aging descending: inv-1 (57 days from 2026-08-01) before
	// inv-2 (7 days from 2026-09-20).
	first := items[0].(map[string]any)
	if first["id"] != "inv-1" || first["aging_days"].(float64) != 57 {
		t.Fatalf("invoices.items[0] = %+v, want inv-1 with aging_days 57", first)
	}
	second := items[1].(map[string]any)
	if second["id"] != "inv-2" || second["aging_days"].(float64) != 7 {
		t.Fatalf("invoices.items[1] = %+v, want inv-2 with aging_days 7", second)
	}

	vendors := fin["vendors"].(map[string]any)
	if vendors["state"] != "ok" {
		t.Fatalf("vendors.state = %v", vendors["state"])
	}
	vitems := vendors["items"].([]any)
	if len(vitems) != 3 {
		t.Fatalf("vendors.items = %+v, want 3", vitems)
	}
	names := []string{vitems[0].(map[string]any)["name"].(string), vitems[1].(map[string]any)["name"].(string), vitems[2].(map[string]any)["name"].(string)}
	if names[0] != "AWS" || names[1] != "Datadog" || names[2] != "NoRenewalCo" {
		t.Fatalf("vendors.items names = %+v, want [AWS, Datadog, NoRenewalCo] (soonest renewal first, no-renewal last)", names)
	}
	if got := vitems[0].(map[string]any)["monthly_usd"].(float64); got != 5000 {
		t.Fatalf("AWS monthly_usd = %v, want 5000", got)
	}
}

// ---- exact fixture values: clients workspace ----

func TestClientsWorkspaceExactFixtureValues(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now, _ := time.Parse(time.RFC3339, wsTestNow)

	if err := h.st.Upsert(ctx, &store.Person{Meta: store.Meta{Source: "seed", SourceID: "dana"}, Name: "Dana"}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.Upsert(ctx, &store.Client{Meta: store.Meta{Source: "seed", SourceID: "acme-co"}, Name: "Acme"}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.Upsert(ctx, &store.Client{Meta: store.Meta{Source: "seed", SourceID: "north"}, Name: "Northstar"}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.AddLink(ctx, store.Link{Kind: store.LinkOwnsClient, FromType: "person", FromID: "dana", ToType: "client", ToID: "acme-co"}); err != nil {
		t.Fatal(err)
	}
	// Northstar has no owner link on purpose: the owner fields must come
	// back empty, not guessed.

	inv := newWSFakeInvoker().
		on("company_customers.accounts", func(map[string]any) (gate.Result, error) {
			return wsJSONResult(map[string]any{"rows": [][]any{
				{"Acme", "healthy", 1.0, 9.0, "2026-09-20"},
				{"Northstar", "at_risk", 4.0, 5.0, "2026-08-01"},
			}})
		}).
		on("company_finance.outstanding_invoices", func(map[string]any) (gate.Result, error) {
			return wsJSONResult(map[string]any{"rows": [][]any{
				{"inv-1", "Northstar", 5000.0, "2026-09-01", "Open"},
			}})
		})
	h.d.cfg.Workspaces = wsWorkspacesFixture(t)
	h.d.cfg.Dashboards = wsDashboardsFixture(t)
	h.d.cfg.Compute = &dashboards.Compute{Gate: inv, Cache: &dashboards.Cache{TTL: dashboards.DefaultCacheTTL}, Now: wsFixedClock(now)}

	status, body := wsGet(t, h.srv.URL, "/v1/workspaces/clients", h.token)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	cl := body["clients"].(map[string]any)
	accounts := cl["accounts"].(map[string]any)
	if accounts["state"] != "ok" {
		t.Fatalf("accounts.state = %v", accounts["state"])
	}
	items := accounts["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("accounts.items = %+v, want 2", items)
	}
	acme := items[0].(map[string]any) // sorted by name: Acme before Northstar
	if acme["name"] != "Acme" || acme["health"] != "healthy" || acme["open_tickets"].(float64) != 1 || acme["nps"].(float64) != 9 {
		t.Fatalf("accounts.items[0] = %+v", acme)
	}
	if got := acme["days_since_contact"].(float64); got != 7 {
		t.Fatalf("Acme days_since_contact = %v, want 7 (2026-09-20 to 2026-09-27)", got)
	}
	if _, hasOutstanding := acme["outstanding_usd"]; hasOutstanding {
		t.Fatalf("Acme outstanding_usd = %v, want omitted (no invoice)", acme["outstanding_usd"])
	}
	if acme["owner_name"] != "Dana" || acme["owner_initials"] != "DA" {
		t.Fatalf("Acme owner = %v/%v, want Dana/DA", acme["owner_name"], acme["owner_initials"])
	}

	north := items[1].(map[string]any)
	if north["name"] != "Northstar" || north["days_since_contact"].(float64) != 57 {
		t.Fatalf("accounts.items[1] = %+v, want Northstar with days_since_contact 57", north)
	}
	if got := north["outstanding_usd"].(float64); got != 5000 {
		t.Fatalf("Northstar outstanding_usd = %v, want 5000 (joined from company_finance.outstanding_invoices)", got)
	}
	if _, hasOwner := north["owner_name"]; hasOwner {
		t.Fatalf("Northstar owner_name = %v, want omitted (no owns_client link)", north["owner_name"])
	}
}

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// ---- People workspace (docs/slices/UI.md Phase 5b) ----

// TestPeopleStrainSignatureCarriesNoTextParameter is THE MORALE INVARIANT's
// own proof (docs/slices/UI.md Phase 5b's named acceptance item): it parses
// workspace_detail.go's own AST and checks peopleStrain's parameter types
// directly, rather than trusting a bare unit test's behaviour to stand in
// for a signature guarantee -- mirroring
// TestWorkspaceDetailFileNeverReferencesDecisionWrites' own AST-scan style
// above. A future edit that widens peopleStrain to take a string (a
// message body, a Slack/email excerpt, anything free-text) fails this test
// even if every other behavioural test still passes.
func TestPeopleStrainSignatureCarriesNoTextParameter(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "workspace_detail.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		if d, ok := n.(*ast.FuncDecl); ok && d.Name.Name == "peopleStrain" {
			fn = d
			return false
		}
		return true
	})
	if fn == nil {
		t.Fatal("peopleStrain not found in workspace_detail.go")
	}
	var params []string
	for _, field := range fn.Type.Params.List {
		typeStr := wsExprString(field.Type)
		n := len(field.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			params = append(params, typeStr)
		}
	}
	want := []string{"float64", "int", "[]PulseAnswer"}
	if len(params) != len(want) {
		t.Fatalf("peopleStrain params = %v, want %v", params, want)
	}
	for i, p := range params {
		if p != want[i] {
			t.Fatalf("peopleStrain params = %v, want %v", params, want)
		}
		lower := strings.ToLower(p)
		if strings.Contains(lower, "string") || strings.Contains(lower, "byte") || strings.Contains(lower, "rune") {
			t.Fatalf("peopleStrain has a text-shaped parameter %q -- THE MORALE INVARIANT requires no string/text-content parameter at all", p)
		}
	}
}

// wsExprString renders an ast.Expr type node back to source-ish text (only
// the forms peopleStrain's own signature can plausibly use: identifiers,
// slices, selectors and pointers), for TestPeopleStrainSignatureCarriesNoTextParameter's
// own parameter-type comparison above.
func wsExprString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.ArrayType:
		return "[]" + wsExprString(t.Elt)
	case *ast.SelectorExpr:
		return wsExprString(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + wsExprString(t.X)
	default:
		return fmt.Sprintf("%T", e)
	}
}

// TestPeopleWorkspaceResourceBarAndTeamTilesExactValues pins the resource
// bar's three numbers and both team tiles' load/strain/overdue numbers to
// exact fixture values -- roster allocations plus a fixture Linear read,
// computed at request time, never stored.
//
// Fixture: two roster teams (crawler/CRA, econ-rag/ECO), each with two
// members and one already-started project. No person here is assigned
// issues on more than one team, so this fixture's own cross_team_callouts
// must come back empty -- TestPeopleWorkspaceCrossTeamCallout below is the
// dedicated positive/negative case for that.
func TestPeopleWorkspaceResourceBarAndTeamTilesExactValues(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now, _ := time.Parse(time.RFC3339, wsTestNow)

	upsert := func(v store.Record) {
		t.Helper()
		if err := h.st.Upsert(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	mustLink := func(l store.Link) {
		t.Helper()
		if err := h.st.AddLink(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	idJSON := func(label string) string { return `{"linear_owner_label":"` + label + `"}` }

	upsert(&store.Team{Meta: store.Meta{Source: "seed", SourceID: "crawler"}, Name: "Crawler", LinearKey: "CRA"})
	upsert(&store.Team{Meta: store.Meta{Source: "seed", SourceID: "econ-rag"}, Name: "Econ-RAG", LinearKey: "ECO"})

	upsert(&store.Project{Meta: store.Meta{Source: "seed", SourceID: "ingestion-v2"}, Name: "Ingestion v2", StartAt: mustDate(t, "2026-08-01")})
	upsert(&store.Project{Meta: store.Meta{Source: "seed", SourceID: "halcyon-rollout"}, Name: "Halcyon rollout", StartAt: mustDate(t, "2026-08-01")})

	upsert(&store.Person{Meta: store.Meta{Source: "seed", SourceID: "lee"}, Name: "Lee", HomeTeam: "crawler", Identities: idJSON("Lee")})
	upsert(&store.Person{Meta: store.Meta{Source: "seed", SourceID: "theo"}, Name: "Theo", HomeTeam: "crawler", Identities: idJSON("Theo")})
	upsert(&store.Person{Meta: store.Meta{Source: "seed", SourceID: "dana"}, Name: "Dana", HomeTeam: "econ-rag", Identities: idJSON("Dana")})
	upsert(&store.Person{Meta: store.Meta{Source: "seed", SourceID: "kai"}, Name: "Kai", HomeTeam: "econ-rag", Identities: idJSON("Kai")})

	mustLink(store.Link{Kind: store.LinkMemberOf, FromType: "person", FromID: "lee", ToType: "team", ToID: "crawler"})
	mustLink(store.Link{Kind: store.LinkMemberOf, FromType: "person", FromID: "theo", ToType: "team", ToID: "crawler"})
	mustLink(store.Link{Kind: store.LinkMemberOf, FromType: "person", FromID: "dana", ToType: "team", ToID: "econ-rag"})
	mustLink(store.Link{Kind: store.LinkMemberOf, FromType: "person", FromID: "kai", ToType: "team", ToID: "econ-rag"})

	// lee 0.5 and dana 0.7 and kai 0.9 are all < 1.0 (free capacity); theo
	// is exactly 1.0 (not free capacity -- the boundary is strict-less-than).
	mustLink(store.Link{Kind: store.LinkAllocated, FromType: "person", FromID: "lee", ToType: "project", ToID: "ingestion-v2", Fraction: 0.5})
	mustLink(store.Link{Kind: store.LinkAllocated, FromType: "person", FromID: "theo", ToType: "project", ToID: "ingestion-v2", Fraction: 1.0})
	mustLink(store.Link{Kind: store.LinkAllocated, FromType: "person", FromID: "dana", ToType: "project", ToID: "halcyon-rollout", Fraction: 0.7})
	mustLink(store.Link{Kind: store.LinkAllocated, FromType: "person", FromID: "kai", ToType: "project", ToID: "halcyon-rollout", Fraction: 0.9})

	issues := []linear.Issue{
		{Identifier: "CRA-1", Team: "CRA", Assignee: "Lee", StateType: "started"},                         // no due date: never overdue
		{Identifier: "CRA-2", Team: "CRA", Assignee: "Theo", StateType: "started", DueDate: "2026-09-20"}, // overdue
		{Identifier: "CRA-3", Team: "CRA", Assignee: "Theo", StateType: "started", DueDate: "2026-10-05"}, // due in the future: not overdue
		{Identifier: "CRA-4", Team: "CRA", StateType: "unstarted", DueDate: "2026-09-10"},                 // unassigned, still counts for the team's own overdue count
		{Identifier: "ECO-1", Team: "ECO", Assignee: "Dana", StateType: "started", DueDate: "2026-09-01"}, // overdue
		{Identifier: "ECO-2", Team: "ECO", Assignee: "Kai", StateType: "started"},                         // no due date
		{Identifier: "ECO-3", Team: "ECO", Assignee: "Kai", StateType: "started", DueDate: "2026-09-05"},  // overdue
		{Identifier: "ECO-4", Team: "ECO", StateType: "unstarted", DueDate: "2026-09-02"},                 // unassigned, overdue -- pushes econ-rag's overdue count to 3, bumping strain to high
	}
	inv := newWSFakeInvoker().
		on("linear.list_issues", func(map[string]any) (gate.Result, error) { return wsJSONResult(issues) }).
		on("company_finance.cash_position", func(map[string]any) (gate.Result, error) {
			return wsJSONResult(map[string]any{"values": map[string]any{"cash_usd": 250000.0, "burn_usd": 40000.0, "runway_months": 6.25}})
		})
	h.d.cfg.Workspaces = wsWorkspacesFixture(t)
	h.d.cfg.Dashboards = wsDashboardsFixture(t)
	h.d.cfg.Compute = &dashboards.Compute{Gate: inv, Cache: &dashboards.Cache{TTL: dashboards.DefaultCacheTTL}, Now: wsFixedClock(now)}

	status, body := wsGet(t, h.srv.URL, "/v1/workspaces/people", h.token)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	people := body["people"].(map[string]any)

	bar := people["resource_bar"].(map[string]any)
	budget := bar["budget_left_usd"].(map[string]any)
	if budget["state"] != "ok" || budget["value"].(float64) != 250000 {
		t.Fatalf("budget_left_usd = %+v, want cash_position 250000 ok (the Finance dashboard's own cash-on-hand metric, this section's documented judgment call)", budget)
	}
	hours := bar["hours_this_week"].(map[string]any)
	if hours["state"] != "ok" || hours["value"].(float64) != 124 {
		t.Fatalf("hours_this_week = %+v, want 124 (20 lee + 40 theo + 28 dana + 36 kai)", hours)
	}
	free := bar["people_with_free_capacity"].(map[string]any)
	if free["state"] != "ok" || free["value"].(float64) != 3 {
		t.Fatalf("people_with_free_capacity = %+v, want 3 (lee, dana, kai; theo is exactly 1.0, not free)", free)
	}

	teams := people["teams"].([]any)
	if len(teams) != 2 {
		t.Fatalf("teams = %+v, want 2", teams)
	}
	crawler := teams[0].(map[string]any) // sorted by name: "Crawler" before "Econ-RAG"
	if crawler["name"] != "Crawler" {
		t.Fatalf("teams[0] = %+v, want Crawler first", crawler)
	}
	if got := crawler["load"].(float64); math.Abs(got-1.05) > 1e-9 {
		t.Fatalf("crawler load = %v, want 1.05 (mean of lee 0.7, theo 1.4)", got)
	}
	if crawler["strain"] != "normal" {
		t.Fatalf("crawler strain = %v, want normal (load 1.05 is between the thresholds, overdue 2 is under the strainOverdueBump of 3)", crawler["strain"])
	}
	if crawler["overdue"].(float64) != 2 {
		t.Fatalf("crawler overdue = %v, want 2 (CRA-2, CRA-4)", crawler["overdue"])
	}
	cMembers := crawler["members"].([]any)
	if len(cMembers) != 2 {
		t.Fatalf("crawler members = %+v, want 2", cMembers)
	}
	leeM := cMembers[0].(map[string]any) // sorted by name: Lee before Theo
	if leeM["name"] != "Lee" || leeM["initials"] != "LE" {
		t.Fatalf("crawler members[0] = %+v, want Lee/LE", leeM)
	}
	if got := leeM["load"].(float64); math.Abs(got-0.7) > 1e-9 {
		t.Fatalf("lee load = %v, want 0.7 (allocation 0.5 + 1 open issue / 5)", got)
	}
	if leeM["load_level"] != "normal" {
		t.Fatalf("lee load_level = %v, want normal (0.7 is exactly strainLowMax, the boundary is strict-less-than)", leeM["load_level"])
	}
	theoM := cMembers[1].(map[string]any)
	if theoM["name"] != "Theo" {
		t.Fatalf("crawler members[1] = %+v, want Theo", theoM)
	}
	if got := theoM["load"].(float64); math.Abs(got-1.4) > 1e-9 {
		t.Fatalf("theo load = %v, want 1.4 (allocation 1.0 + 2 open issues / 5)", got)
	}
	if theoM["load_level"] != "high" {
		t.Fatalf("theo load_level = %v, want high (load 1.4 > strainHighMin)", theoM["load_level"])
	}

	econ := teams[1].(map[string]any)
	if econ["name"] != "Econ-RAG" {
		t.Fatalf("teams[1] = %+v, want Econ-RAG", econ)
	}
	if got := econ["load"].(float64); math.Abs(got-1.1) > 1e-9 {
		t.Fatalf("econ-rag load = %v, want 1.1 (mean of dana 0.9, kai 1.3)", got)
	}
	if econ["overdue"].(float64) != 3 {
		t.Fatalf("econ-rag overdue = %v, want 3 (ECO-1, ECO-3, ECO-4)", econ["overdue"])
	}
	if econ["strain"] != "high" {
		t.Fatalf("econ-rag strain = %v, want high -- load 1.1 alone reads normal, but the overdue count of 3 (>= strainOverdueBump) bumps it, proving overdue is its own signal, not just a side effect of load", econ["strain"])
	}

	if raw, ok := people["cross_team_callouts"]; ok {
		if arr, ok := raw.([]any); ok && len(arr) != 0 {
			t.Fatalf("cross_team_callouts = %+v, want none (nobody in this fixture is assigned issues on two different teams)", arr)
		}
	}
}

// TestPeopleWorkspaceCrossTeamCallout is the dedicated positive/negative
// case docs/slices/UI.md Phase 5b names explicitly: a real overlap (two
// different teams, deadlines within 7 days of each other) fires; a
// same-team pair and an out-of-window pair both don't.
func TestPeopleWorkspaceCrossTeamCallout(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now, _ := time.Parse(time.RFC3339, wsTestNow)

	upsert := func(v store.Record) {
		t.Helper()
		if err := h.st.Upsert(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	idJSON := func(label string) string { return `{"linear_owner_label":"` + label + `"}` }

	upsert(&store.Team{Meta: store.Meta{Source: "seed", SourceID: "crawler"}, Name: "Crawler", LinearKey: "CRA"})
	upsert(&store.Team{Meta: store.Meta{Source: "seed", SourceID: "econ-rag"}, Name: "Econ-RAG", LinearKey: "ECO"})
	upsert(&store.Person{Meta: store.Meta{Source: "seed", SourceID: "avery"}, Name: "Avery", Identities: idJSON("Avery")})
	upsert(&store.Person{Meta: store.Meta{Source: "seed", SourceID: "blair"}, Name: "Blair", Identities: idJSON("Blair")})
	upsert(&store.Person{Meta: store.Meta{Source: "seed", SourceID: "casey"}, Name: "Casey", Identities: idJSON("Casey")})

	issues := []linear.Issue{
		// Avery: two different teams, 4 days apart -- fires.
		{Identifier: "CRA-10", Team: "CRA", Assignee: "Avery", StateType: "started", DueDate: "2026-09-20"},
		{Identifier: "ECO-10", Team: "ECO", Assignee: "Avery", StateType: "started", DueDate: "2026-09-24"},
		// Blair: two different teams, 19 days apart -- outside the 7-day
		// window, no callout.
		{Identifier: "CRA-11", Team: "CRA", Assignee: "Blair", StateType: "started", DueDate: "2026-09-01"},
		{Identifier: "ECO-11", Team: "ECO", Assignee: "Blair", StateType: "started", DueDate: "2026-09-20"},
		// Casey: the same team twice, 1 day apart -- within the window but
		// not cross-team, no callout.
		{Identifier: "CRA-12", Team: "CRA", Assignee: "Casey", StateType: "started", DueDate: "2026-09-20"},
		{Identifier: "CRA-13", Team: "CRA", Assignee: "Casey", StateType: "started", DueDate: "2026-09-21"},
	}
	inv := newWSFakeInvoker().on("linear.list_issues", func(map[string]any) (gate.Result, error) { return wsJSONResult(issues) })
	h.d.cfg.Workspaces = wsWorkspacesFixture(t)
	h.d.cfg.Compute = &dashboards.Compute{Gate: inv, Cache: &dashboards.Cache{TTL: dashboards.DefaultCacheTTL}, Now: wsFixedClock(now)}

	status, body := wsGet(t, h.srv.URL, "/v1/workspaces/people", h.token)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	people := body["people"].(map[string]any)
	calloutsRaw, ok := people["cross_team_callouts"]
	if !ok {
		t.Fatal("cross_team_callouts missing, want exactly one callout for Avery")
	}
	callouts := calloutsRaw.([]any)
	if len(callouts) != 1 {
		t.Fatalf("cross_team_callouts = %+v, want exactly 1 (Avery only)", callouts)
	}
	c := callouts[0].(map[string]any)
	if c["person"] != "Avery" {
		t.Fatalf("callout person = %v, want Avery", c["person"])
	}
	if c["window_days"].(float64) != 7 {
		t.Fatalf("window_days = %v, want 7", c["window_days"])
	}
	teamsGot := c["teams"].([]any)
	if len(teamsGot) != 2 || teamsGot[0] != "Crawler" || teamsGot[1] != "Econ-RAG" {
		t.Fatalf("callout teams = %+v, want [Crawler, Econ-RAG]", teamsGot)
	}
	deadlines := c["deadlines"].([]any)
	if len(deadlines) != 2 {
		t.Fatalf("deadlines = %+v, want 2", deadlines)
	}
	d0 := deadlines[0].(map[string]any)
	if d0["issue_identifier"] != "CRA-10" || d0["due_date"] != "2026-09-20" {
		t.Fatalf("deadlines[0] = %+v, want CRA-10 on 2026-09-20", d0)
	}
	d1 := deadlines[1].(map[string]any)
	if d1["issue_identifier"] != "ECO-10" || d1["due_date"] != "2026-09-24" {
		t.Fatalf("deadlines[1] = %+v, want ECO-10 on 2026-09-24", d1)
	}
}

// ---- POST /v1/workspaces/{id}/drafts (docs/slices/UI.md Phase 5b) ----

func TestCreateWorkspaceDraftTeamMessageAndPulseCheck(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.d.cfg.Workspaces = wsWorkspacesFixture(t)
	if err := h.st.Upsert(ctx, &store.Team{Meta: store.Meta{Source: "seed", SourceID: "crawler"}, Name: "Crawler", LinearKey: "CRA"}); err != nil {
		t.Fatal(err)
	}

	status, body := wsPost(t, h.srv.URL, "/v1/workspaces/people/drafts", `{"kind":"team_message","team":"crawler"}`, h.token)
	if status != http.StatusOK {
		t.Fatalf("team_message status = %d", status)
	}
	if body["template"] != "team_message" {
		t.Fatalf("template = %v, want team_message", body["template"])
	}
	if to, ok := body["to"]; ok && to != "" {
		t.Fatalf("to = %v, want empty (no populated roster email identity to guess a recipient from)", to)
	}
	subj, _ := body["subject"].(string)
	if !strings.Contains(subj, "Crawler") {
		t.Fatalf("subject = %q, want it to name the team", subj)
	}
	bodyText, _ := body["body"].(string)
	if !strings.Contains(bodyText, "Crawler") {
		t.Fatalf("body = %q, want it to name the team", bodyText)
	}

	status2, body2 := wsPost(t, h.srv.URL, "/v1/workspaces/people/drafts", `{"kind":"pulse_check","team":"crawler"}`, h.token)
	if status2 != http.StatusOK {
		t.Fatalf("pulse_check status = %d", status2)
	}
	if body2["template"] != "pulse_check" {
		t.Fatalf("template = %v, want pulse_check", body2["template"])
	}
	pulseBody, _ := body2["body"].(string)
	if !strings.Contains(pulseBody, "1") || !strings.Contains(pulseBody, "5") {
		t.Fatalf("pulse_check body = %q, want a code-built 1..5 scale question", pulseBody)
	}

	drafts, err := h.st.ListDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 2 {
		t.Fatalf("drafts = %+v, want exactly 2 (one per call)", drafts)
	}
}

// TestCreateWorkspaceDraftRejectsUnknownKind is the plan's own named case:
// "an unknown kind is rejected (400), never silently accepted."
func TestCreateWorkspaceDraftRejectsUnknownKind(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.d.cfg.Workspaces = wsWorkspacesFixture(t)
	if err := h.st.Upsert(ctx, &store.Team{Meta: store.Meta{Source: "seed", SourceID: "crawler"}, Name: "Crawler", LinearKey: "CRA"}); err != nil {
		t.Fatal(err)
	}
	resp := do(t, h.srv.URL, "POST", "/v1/workspaces/people/drafts", `{"kind":"newsletter","team":"crawler"}`, h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown kind", resp.StatusCode)
	}
	drafts, err := h.st.ListDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 0 {
		t.Fatalf("an unknown kind must never create a draft, got %+v", drafts)
	}
}

func TestCreateWorkspaceDraftRejectsUnknownTeam(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.d.cfg.Workspaces = wsWorkspacesFixture(t)
	resp := do(t, h.srv.URL, "POST", "/v1/workspaces/people/drafts", `{"kind":"team_message","team":"does-not-exist"}`, h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown team", resp.StatusCode)
	}
	drafts, err := h.st.ListDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 0 {
		t.Fatalf("an unknown team must never create a draft, got %+v", drafts)
	}
}

func TestCreateWorkspaceDraftUnknownWorkspaceIs404(t *testing.T) {
	h := newHarness(t)
	h.d.cfg.Workspaces = wsWorkspacesFixture(t)
	resp := do(t, h.srv.URL, "POST", "/v1/workspaces/does-not-exist/drafts", `{"kind":"team_message","team":"crawler"}`, h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// ---- Marketing workspace (docs/slices/UI.md Phase 5d) ----

// TestMarketingWorkspaceExactFixtureValues pins the Marketing template's
// shape to exact fixture values: no research run is linked in_workspace, so
// every trend tile must be illustrative; company_customers.accounts has one
// healthy account (Acme, excluded) and two blank-health rows (prospects,
// included, sorted by name); no roster person has role "Design lead", so
// there is no capacity note; public reviews always report not_connected
// with no items.
func TestMarketingWorkspaceExactFixtureValues(t *testing.T) {
	h := newHarness(t)
	now, _ := time.Parse(time.RFC3339, wsTestNow)

	inv := newWSFakeInvoker().on("company_customers.accounts", func(map[string]any) (gate.Result, error) {
		return wsJSONResult(map[string]any{"rows": [][]any{
			{"Acme", "healthy", 1.0, 9.0, "2026-09-20"},
			{"Northstar", "", 4.0, nil, "2026-08-01"},
			{"Fenwick", "", 0.0, nil, "2026-09-10"},
		}})
	})
	h.d.cfg.Workspaces = wsWorkspacesFixture(t)
	h.d.cfg.Compute = &dashboards.Compute{Gate: inv, Cache: &dashboards.Cache{TTL: dashboards.DefaultCacheTTL}, Now: wsFixedClock(now)}

	status, body := wsGet(t, h.srv.URL, "/v1/workspaces/marketing", h.token)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	mk := body["marketing"].(map[string]any)

	trend := mk["trend_tiles"].([]any)
	if len(trend) != 3 {
		t.Fatalf("trend_tiles = %+v, want 3", trend)
	}
	for _, raw := range trend {
		tile := raw.(map[string]any)
		if tile["state"] != "illustrative" {
			t.Errorf("trend tile %+v: state = %v, want illustrative (no research run linked)", tile, tile["state"])
		}
		if _, has := tile["run_id"]; has {
			t.Errorf("trend tile %+v has run_id set with no run linked", tile)
		}
	}

	prospects := mk["prospects"].(map[string]any)
	if prospects["state"] != "ok" {
		t.Fatalf("prospects.state = %v", prospects["state"])
	}
	items := prospects["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("prospects.items = %+v, want 2 (blank-health rows only)", items)
	}
	fenwick := items[0].(map[string]any) // sorted by name: Fenwick before Northstar
	if fenwick["name"] != "Fenwick" {
		t.Fatalf("prospects.items[0] = %+v, want Fenwick", fenwick)
	}
	if _, hasTickets := fenwick["open_tickets"]; hasTickets {
		t.Fatalf("Fenwick open_tickets = %v, want omitted (0)", fenwick["open_tickets"])
	}
	if got := fenwick["days_since_contact"].(float64); got != 17 {
		t.Fatalf("Fenwick days_since_contact = %v, want 17 (2026-09-10 to 2026-09-27)", got)
	}
	north := items[1].(map[string]any)
	if north["name"] != "Northstar" || north["open_tickets"].(float64) != 4 {
		t.Fatalf("prospects.items[1] = %+v, want Northstar with open_tickets 4", north)
	}

	reviews := mk["public_reviews"].(map[string]any)
	if reviews["state"] != "not_connected" {
		t.Fatalf("public_reviews.state = %v, want not_connected (no data source exists)", reviews["state"])
	}
	if _, hasItems := reviews["items"]; hasItems {
		t.Fatalf("public_reviews.items = %v, want omitted/empty -- no fabricated reviews", reviews["items"])
	}

	if _, hasNote := mk["capacity_note"]; hasNote {
		t.Fatalf("capacity_note = %v, want omitted (no roster person has role \"Design lead\")", mk["capacity_note"])
	}
}

// TestMarketingTrendTilesReferenceRealRun is the positive case for the
// in_workspace mechanism marketingAttachedResearchRun documents: once a
// finished research run is linked to the marketing workspace (store.AddLink
// with FromType "research" -- no production writer creates this edge yet,
// so a test writes it directly, exactly like marketingAttachedResearchRun's
// own doc comment says), every trend tile whose facet has a matching report
// section switches to "ok" and names that run; the "market" section's own
// "42%" is extracted as a real, traceable value.
func TestMarketingTrendTilesReferenceRealRun(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now, _ := time.Parse(time.RFC3339, wsTestNow)

	idea, err := h.st.CreateIdea(ctx, store.Idea{Title: "On-device transcription", Stage: "explored"})
	if err != nil {
		t.Fatal(err)
	}
	report := "## Market\nGrowing at 42% year over year.\n\n\n## Competitors\nThree main competitors, no clear leader.\n\n\n## Pricing\nNo pricing changes this quarter.\n"
	run, err := h.st.CreateResearchRun(ctx, store.ResearchRun{
		IdeaID: idea.ID, Topic: "On-device transcription", Status: "finished",
		ReportText: report, FinishedAt: now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.AddLink(ctx, store.Link{Kind: store.LinkInWorkspace, FromType: "research", FromID: run.ID, ToType: "workspace", ToID: "marketing"}); err != nil {
		t.Fatal(err)
	}

	inv := newWSFakeInvoker().on("company_customers.accounts", func(map[string]any) (gate.Result, error) {
		return wsJSONResult(map[string]any{"rows": [][]any{}})
	})
	h.d.cfg.Workspaces = wsWorkspacesFixture(t)
	h.d.cfg.Compute = &dashboards.Compute{Gate: inv, Cache: &dashboards.Cache{TTL: dashboards.DefaultCacheTTL}, Now: wsFixedClock(now)}

	status, body := wsGet(t, h.srv.URL, "/v1/workspaces/marketing", h.token)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	mk := body["marketing"].(map[string]any)
	trend := mk["trend_tiles"].([]any)
	if len(trend) != 3 {
		t.Fatalf("trend_tiles = %+v, want 3", trend)
	}
	byLabel := map[string]map[string]any{}
	for _, raw := range trend {
		tile := raw.(map[string]any)
		byLabel[tile["label"].(string)] = tile
	}
	market, ok := byLabel["Market"]
	if !ok {
		t.Fatalf("no Market trend tile in %+v", trend)
	}
	if market["state"] != "ok" {
		t.Fatalf("Market tile state = %v, want ok (a finished run is linked)", market["state"])
	}
	if market["run_id"] != run.ID {
		t.Fatalf("Market tile run_id = %v, want %v", market["run_id"], run.ID)
	}
	if market["run_topic"] != run.Topic {
		t.Fatalf("Market tile run_topic = %v, want %v", market["run_topic"], run.Topic)
	}
	if got := market["value"].(float64); got != 42 {
		t.Fatalf("Market tile value = %v, want 42 (extracted from the run's own report text)", got)
	}
	if market["unit"] != "%" {
		t.Fatalf("Market tile unit = %v, want %%", market["unit"])
	}

	for _, label := range []string{"Competitors", "Pricing"} {
		tile, ok := byLabel[label]
		if !ok {
			t.Fatalf("no %s trend tile in %+v", label, trend)
		}
		if tile["state"] != "ok" || tile["run_id"] != run.ID {
			t.Fatalf("%s tile = %+v, want state ok and run_id %v (no fabricated number required)", label, tile, run.ID)
		}
		if _, hasValue := tile["value"]; hasValue {
			t.Fatalf("%s tile value = %v, want omitted (no percentage in that section's text)", label, tile["value"])
		}
	}
}

// TestMarketingCapacityNoteThreshold is the plan's own named boundary
// (docs/slices/UI.md Phase 5d: "the design lead's computed allocation is
// >=1.0"): exactly 1.0 fires, one allocation link short of it does not.
func TestMarketingCapacityNoteThreshold(t *testing.T) {
	setup := func(t *testing.T, fraction float64) (status int, body map[string]any) {
		h := newHarness(t)
		ctx := context.Background()
		now, _ := time.Parse(time.RFC3339, wsTestNow)
		if err := h.st.Upsert(ctx, &store.Person{Meta: store.Meta{Source: "seed", SourceID: "quinn"}, Name: "Quinn", Role: "Design lead"}); err != nil {
			t.Fatal(err)
		}
		if err := h.st.Upsert(ctx, &store.Project{Meta: store.Meta{Source: "seed", SourceID: "halcyon-rollout"}, Name: "Halcyon rollout", StartAt: mustDate(t, "2026-08-01")}); err != nil {
			t.Fatal(err)
		}
		if err := h.st.AddLink(ctx, store.Link{Kind: store.LinkAllocated, FromType: "person", FromID: "quinn", ToType: "project", ToID: "halcyon-rollout", Fraction: fraction}); err != nil {
			t.Fatal(err)
		}
		inv := newWSFakeInvoker().on("company_customers.accounts", func(map[string]any) (gate.Result, error) {
			return wsJSONResult(map[string]any{"rows": [][]any{}})
		})
		h.d.cfg.Workspaces = wsWorkspacesFixture(t)
		h.d.cfg.Compute = &dashboards.Compute{Gate: inv, Cache: &dashboards.Cache{TTL: dashboards.DefaultCacheTTL}, Now: wsFixedClock(now)}
		return wsGet(t, h.srv.URL, "/v1/workspaces/marketing", h.token)
	}

	t.Run("exactly 1.0 fires", func(t *testing.T) {
		status, body := setup(t, 1.0)
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		mk := body["marketing"].(map[string]any)
		note, ok := mk["capacity_note"].(map[string]any)
		if !ok {
			t.Fatalf("capacity_note missing at exactly 1.0: %+v", mk)
		}
		if note["person_name"] != "Quinn" || note["allocation"].(float64) != 1.0 {
			t.Fatalf("capacity_note = %+v, want Quinn at 1.0", note)
		}
	})

	t.Run("below 1.0 does not fire", func(t *testing.T) {
		status, body := setup(t, 0.99)
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		mk := body["marketing"].(map[string]any)
		if _, has := mk["capacity_note"]; has {
			t.Fatalf("capacity_note = %v, want omitted below the 1.0 threshold", mk["capacity_note"])
		}
	})
}

// TestCreateWorkspaceDraftProspectOutreach is "Draft outreach" (docs/
// slices/UI.md Phase 5d): a prospect resolved from the real
// company_customers.accounts fixture, a code-built body naming the
// prospect, never a model call.
func TestCreateWorkspaceDraftProspectOutreach(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now, _ := time.Parse(time.RFC3339, wsTestNow)
	inv := newWSFakeInvoker().on("company_customers.accounts", func(map[string]any) (gate.Result, error) {
		return wsJSONResult(map[string]any{"rows": [][]any{
			{"Fenwick", "", 2.0, nil, "2026-09-10"},
		}})
	})
	h.d.cfg.Workspaces = wsWorkspacesFixture(t)
	h.d.cfg.Compute = &dashboards.Compute{Gate: inv, Cache: &dashboards.Cache{TTL: dashboards.DefaultCacheTTL}, Now: wsFixedClock(now)}

	status, body := wsPost(t, h.srv.URL, "/v1/workspaces/marketing/drafts", `{"kind":"prospect_outreach","prospect_id":"Fenwick"}`, h.token)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if body["template"] != "prospect_outreach" {
		t.Fatalf("template = %v, want prospect_outreach", body["template"])
	}
	subj, _ := body["subject"].(string)
	if !strings.Contains(subj, "Fenwick") {
		t.Fatalf("subject = %q, want it to name the prospect", subj)
	}
	bodyText, _ := body["body"].(string)
	if !strings.Contains(bodyText, "Fenwick") {
		t.Fatalf("body = %q, want it to name the prospect", bodyText)
	}
	if !strings.Contains(bodyText, "17 days") {
		t.Fatalf("body = %q, want it to cite the real days-since-contact number (17, 2026-09-10 to 2026-09-27)", bodyText)
	}

	drafts, err := h.st.ListDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 {
		t.Fatalf("drafts = %+v, want exactly 1", drafts)
	}
}

func TestCreateWorkspaceDraftProspectOutreachUnknownProspectIs400(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now, _ := time.Parse(time.RFC3339, wsTestNow)
	inv := newWSFakeInvoker().on("company_customers.accounts", func(map[string]any) (gate.Result, error) {
		return wsJSONResult(map[string]any{"rows": [][]any{}})
	})
	h.d.cfg.Workspaces = wsWorkspacesFixture(t)
	h.d.cfg.Compute = &dashboards.Compute{Gate: inv, Cache: &dashboards.Cache{TTL: dashboards.DefaultCacheTTL}, Now: wsFixedClock(now)}

	resp := do(t, h.srv.URL, "POST", "/v1/workspaces/marketing/drafts", `{"kind":"prospect_outreach","prospect_id":"does-not-exist"}`, h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown prospect", resp.StatusCode)
	}
	drafts, err := h.st.ListDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 0 {
		t.Fatalf("an unknown prospect must never create a draft, got %+v", drafts)
	}
}

// TestCreateWorkspaceDraftReviewReply is "Draft reply"'s mechanism (docs/
// slices/UI.md Phase 5d): built and tested even though no real
// public-reviews source exists yet (marketingPublicReviewsTile's own doc
// comment) -- a caller-supplied review id builds a generic, code-built
// reply template, never any fabricated review text.
func TestCreateWorkspaceDraftReviewReply(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.d.cfg.Workspaces = wsWorkspacesFixture(t)

	status, body := wsPost(t, h.srv.URL, "/v1/workspaces/marketing/drafts", `{"kind":"review_reply","review_id":"rev-42"}`, h.token)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if body["template"] != "review_reply" {
		t.Fatalf("template = %v, want review_reply", body["template"])
	}
	bodyText, _ := body["body"].(string)
	if !strings.Contains(bodyText, "rev-42") {
		t.Fatalf("body = %q, want it to reference the review id", bodyText)
	}
	if strings.Contains(strings.ToLower(bodyText), "stars") || strings.Contains(bodyText, "\"") {
		t.Fatalf("body = %q, looks like it might contain fabricated review content", bodyText)
	}

	drafts, err := h.st.ListDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 {
		t.Fatalf("drafts = %+v, want exactly 1", drafts)
	}
}

func TestCreateWorkspaceDraftReviewReplyBlankIDIs400(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.d.cfg.Workspaces = wsWorkspacesFixture(t)

	resp := do(t, h.srv.URL, "POST", "/v1/workspaces/marketing/drafts", `{"kind":"review_reply","review_id":""}`, h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a blank review_id", resp.StatusCode)
	}
	drafts, err := h.st.ListDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 0 {
		t.Fatalf("a blank review_id must never create a draft, got %+v", drafts)
	}
}
