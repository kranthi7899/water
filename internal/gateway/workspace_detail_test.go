package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
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

	dbPath := filepath.Join(h.dir, "water.db")
	before := wsDecisionTableCounts(t, dbPath)

	table := []struct {
		name, method, path string
	}{
		{"project, no github source", "GET", "/v1/workspaces/crawler-nogh"},
		{"project, github source", "GET", "/v1/workspaces/crawler-gh"},
		{"finance", "GET", "/v1/workspaces/finance"},
		{"clients", "GET", "/v1/workspaces/clients"},
		{"unknown id (404)", "GET", "/v1/workspaces/does-not-exist"},
	}
	for _, tc := range table {
		t.Run(tc.name, func(t *testing.T) {
			resp := do(t, h.srv.URL, tc.method, tc.path, "", h.token)
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
