package dashboards

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"water/internal/connectors/google/gsheets"
	"water/internal/connectors/linear"
	"water/internal/gate"
)

// fakeInvoker is a minimal Invoker: each call is answered by a registered
// handler and counted, so a test can assert exactly how many gated calls a
// Dashboard render made and at what origin — without a real gate, store or
// connector. The end-to-end tests further down this file additionally
// drive the real gate and the real gsheets/linear connectors against
// httptest fixtures, which is what actually proves "computed from
// connectors" rather than from this fake.
type fakeInvoker struct {
	mu       sync.Mutex
	calls    map[string]int
	origins  map[string][]gate.Origin
	handlers map[string]func() (gate.Result, error)
}

func newFakeInvoker() *fakeInvoker {
	return &fakeInvoker{calls: map[string]int{}, origins: map[string][]gate.Origin{}, handlers: map[string]func() (gate.Result, error){}}
}

func jsonResult(v any) (gate.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return gate.Result{}, err
	}
	return gate.Result{Output: b, Untrusted: true}, nil
}

func (f *fakeInvoker) on(fn string, h func() (gate.Result, error)) *fakeInvoker {
	f.handlers[fn] = h
	return f
}

func (f *fakeInvoker) Invoke(_ context.Context, c gate.Call) (gate.Result, error) {
	f.mu.Lock()
	f.calls[c.Function]++
	f.origins[c.Function] = append(f.origins[c.Function], c.Origin)
	f.mu.Unlock()
	h, ok := f.handlers[c.Function]
	if !ok {
		return gate.Result{}, fmt.Errorf("fake: no handler registered for %s", c.Function)
	}
	return h()
}

func (f *fakeInvoker) callCount(fn string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[fn]
}

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

const testNow = "2026-09-27T00:00:00Z"

func testCompute(inv Invoker) *Compute {
	now, _ := time.Parse(time.RFC3339, testNow)
	return &Compute{Gate: inv, Cache: &Cache{TTL: DefaultCacheTTL}, Now: fixedClock(now)}
}

// ---- Finance: exact numbers from fixtures ----

func TestFinanceCashMetricsFromFixture(t *testing.T) {
	inv := newFakeInvoker().on("company_finance.cash_position", func() (gate.Result, error) {
		return jsonResult(sheetValues{Values: map[string]any{"cash_usd": 250000.0, "burn_usd": 40000.0, "runway_months": 6.25}})
	})
	co := testCompute(inv)
	ctx := context.Background()

	if got := metricFuncs["cash_position"](ctx, co); got.State != TileOK || got.Value != 250000 {
		t.Fatalf("cash_position = %+v", got)
	}
	if got := metricFuncs["monthly_burn"](ctx, co); got.State != TileOK || got.Value != 40000 {
		t.Fatalf("monthly_burn = %+v", got)
	}
	if got := metricFuncs["runway_months"](ctx, co); got.State != TileOK || got.Value != 6.25 {
		t.Fatalf("runway_months = %+v", got)
	}
	// All three metrics read the same underlying call: one gated call, not three.
	if n := inv.callCount("company_finance.cash_position"); n != 1 {
		t.Fatalf("cash_position called %d times, want 1 (shared cache)", n)
	}
}

func financeSpendFixture() []any {
	return []any{
		[]any{"crawler", 1000.0, 2000.0, 3000.0, 50000.0, 48000.0, -2000.0, -4.0},
		[]any{"econ-rag", 1000.0, 2000.0, 3000.0, 30000.0, 31000.0, 1000.0, 3.0},
	}
}

func TestFinanceSpendBreakdownFromFixture(t *testing.T) {
	inv := newFakeInvoker().on("company_finance.spend_breakdown_all", func() (gate.Result, error) {
		return jsonResult(sheetRows{Rows: toRows(financeSpendFixture())})
	})
	co := testCompute(inv)
	got := breakdownFuncs["spend_by_application"](context.Background(), co)
	if got.State != TileOK {
		t.Fatalf("state = %v", got.State)
	}
	want := []BreakdownItem{{Label: "crawler", Value: 50000}, {Label: "econ-rag", Value: 30000}}
	if len(got.Items) != 2 || got.Items[0] != want[0] || got.Items[1] != want[1] {
		t.Fatalf("items = %+v, want %+v", got.Items, want)
	}
}

// TestFinanceWorstAppMarginCalloutPicksMarginWhenNoSpike: crawler loses
// $2000/mo, no monthly_costs data qualifies as a spike, so the margin gap
// wins by default.
func TestFinanceWorstAppMarginCalloutPicksMarginWhenNoSpike(t *testing.T) {
	inv := newFakeInvoker().
		on("company_finance.spend_breakdown_all", func() (gate.Result, error) {
			return jsonResult(sheetRows{Rows: toRows(financeSpendFixture())})
		}).
		on("company_finance.monthly_costs", func() (gate.Result, error) {
			return jsonResult(sheetRows{Rows: toRows([]any{
				[]any{"2026-06", 20000.0, 30000.0, 10000.0},
				[]any{"2026-07", 20000.0, 31000.0, 11000.0},
				[]any{"2026-08", 20000.0, 29000.0, 9000.0},
			})})
		})
	co := testCompute(inv)
	got := calloutFuncs["worst_app_margin"](context.Background(), co)
	if got.State != TileOK || got.Kind != "margin_gap" || got.Title != "crawler" || got.Value != 2000 {
		t.Fatalf("callout = %+v", got)
	}
}

// TestFinanceWorstAppMarginCalloutPicksCostSpikeWhenMoreSevere: July's
// $70,000 cost is far more than 1.5x the ~$30,500 median, a bigger dollar
// excess than crawler's $2,000 margin gap, so the spike wins.
func TestFinanceWorstAppMarginCalloutPicksCostSpikeWhenMoreSevere(t *testing.T) {
	inv := newFakeInvoker().
		on("company_finance.spend_breakdown_all", func() (gate.Result, error) {
			return jsonResult(sheetRows{Rows: toRows(financeSpendFixture())})
		}).
		on("company_finance.monthly_costs", func() (gate.Result, error) {
			return jsonResult(sheetRows{Rows: toRows([]any{
				[]any{"2026-06", 20000.0, 30000.0, 10000.0},
				[]any{"2026-07", 20000.0, 70000.0, 50000.0},
				[]any{"2026-08", 20000.0, 31000.0, 11000.0},
			})})
		})
	co := testCompute(inv)
	got := calloutFuncs["worst_app_margin"](context.Background(), co)
	if got.State != TileOK || got.Kind != "cost_spike" || got.Title != "2026-07" {
		t.Fatalf("callout = %+v", got)
	}
	// median of [30000,70000,31000] = 31000; threshold 46500; excess = 23500
	if got.Value != 23500 {
		t.Fatalf("excess = %v, want 23500", got.Value)
	}
}

// TestFinanceWorstAppMarginTieBreaksToEarliestRow: crawler and econ-rag2
// both lose exactly $2000/mo; crawler appears first in the sheet, so it
// wins deterministically.
func TestFinanceWorstAppMarginTieBreaksToEarliestRow(t *testing.T) {
	inv := newFakeInvoker().
		on("company_finance.spend_breakdown_all", func() (gate.Result, error) {
			return jsonResult(sheetRows{Rows: toRows([]any{
				[]any{"crawler", 0.0, 0.0, 0.0, 50000.0, 48000.0, 0.0, 0.0},
				[]any{"econ-rag2", 0.0, 0.0, 0.0, 50000.0, 48000.0, 0.0, 0.0},
			})})
		}).
		on("company_finance.monthly_costs", func() (gate.Result, error) {
			return jsonResult(sheetRows{Rows: [][]any{}})
		})
	co := testCompute(inv)
	got := calloutFuncs["worst_app_margin"](context.Background(), co)
	if got.State != TileOK || got.Title != "crawler" {
		t.Fatalf("callout = %+v, want crawler (earliest row) to win the tie", got)
	}
}

// TestFinanceWorstAppMarginCalloutEmptyDataIsUnavailableNotPanic: neither
// signal has any qualifying row.
func TestFinanceWorstAppMarginCalloutEmptyDataIsUnavailableNotPanic(t *testing.T) {
	inv := newFakeInvoker().
		on("company_finance.spend_breakdown_all", func() (gate.Result, error) { return jsonResult(sheetRows{}) }).
		on("company_finance.monthly_costs", func() (gate.Result, error) { return jsonResult(sheetRows{}) })
	co := testCompute(inv)
	got := calloutFuncs["worst_app_margin"](context.Background(), co)
	if got.State != TileUnavailable {
		t.Fatalf("state = %v, want unavailable on empty data", got.State)
	}
}

// TestFinanceNotConnectedPropagatesToMetricsBreakdownAndCallout: the gate's
// own credential-missing denial, matched structurally.
func TestFinanceNotConnectedPropagatesToMetricsBreakdownAndCallout(t *testing.T) {
	denied := func() (gate.Result, error) {
		return gate.Result{}, &gate.DenyError{Reason: "credential for company_finance is unavailable"}
	}
	inv := newFakeInvoker().
		on("company_finance.cash_position", denied).
		on("company_finance.spend_breakdown_all", denied).
		on("company_finance.monthly_costs", denied)
	co := testCompute(inv)
	ctx := context.Background()
	if got := metricFuncs["cash_position"](ctx, co); got.State != TileNotConnected || got.Value != 0 {
		t.Fatalf("cash_position = %+v", got)
	}
	if got := breakdownFuncs["spend_by_application"](ctx, co); got.State != TileNotConnected {
		t.Fatalf("spend_by_application = %+v", got)
	}
	if got := calloutFuncs["worst_app_margin"](ctx, co); got.State != TileNotConnected {
		t.Fatalf("worst_app_margin = %+v", got)
	}
}

// ---- Delivery: exact numbers, breakdown and blocker callout ----

func deliveryFixture() []linear.Issue {
	return []linear.Issue{
		{Identifier: "CRA-3", StateType: "started", Team: "CRA", PriorityRank: 1,
			Relations: []linear.Relation{{Type: "blocks", Identifier: "CRA-4", StateType: "unstarted"}, {Type: "blocks", Identifier: "CRA-5", StateType: "unstarted"}}},
		{Identifier: "CRA-4", StateType: "unstarted", Team: "CRA", PriorityRank: 3,
			InverseRelations: []linear.Relation{{Type: "blocks", Identifier: "CRA-3", StateType: "started"}}},
		{Identifier: "CRA-5", StateType: "unstarted", Team: "CRA", PriorityRank: 2,
			InverseRelations: []linear.Relation{{Type: "blocks", Identifier: "CRA-3", StateType: "started"}}},
		{Identifier: "WAT-1", StateType: "completed", Team: "WAT", PriorityRank: 4},
		{Identifier: "YTR-6", StateType: "cancelled", Team: "YTR", PriorityRank: 1}, // closed: not urgent-counted-as-open, not blocked
	}
}

func TestDeliveryMetricsFromFixture(t *testing.T) {
	inv := newFakeInvoker().on("linear.list_issues", func() (gate.Result, error) { return jsonResult(deliveryFixture()) })
	co := testCompute(inv)
	ctx := context.Background()

	// open: CRA-3 (started), CRA-4 (unstarted), CRA-5 (unstarted) = 3
	if got := metricFuncs["open_issues"](ctx, co); got.State != TileOK || got.Value != 3 {
		t.Fatalf("open_issues = %+v", got)
	}
	// blocked: CRA-4 and CRA-5, both blocked by open CRA-3 = 2
	if got := metricFuncs["blocked_issues"](ctx, co); got.State != TileOK || got.Value != 2 {
		t.Fatalf("blocked_issues = %+v", got)
	}
	// urgent (priority rank 1): CRA-3 and YTR-6 = 2 (urgent counts every
	// issue regardless of open/closed, matching "priority=1" literally)
	if got := metricFuncs["urgent_issues"](ctx, co); got.State != TileOK || got.Value != 2 {
		t.Fatalf("urgent_issues = %+v", got)
	}
	if n := inv.callCount("linear.list_issues"); n != 1 {
		t.Fatalf("list_issues called %d times, want 1 (shared cache across 3 metrics)", n)
	}
}

func TestDeliveryIssuesByTeamBreakdown(t *testing.T) {
	inv := newFakeInvoker().on("linear.list_issues", func() (gate.Result, error) { return jsonResult(deliveryFixture()) })
	co := testCompute(inv)
	got := breakdownFuncs["issues_by_team"](context.Background(), co)
	if got.State != TileOK {
		t.Fatalf("state = %v", got.State)
	}
	want := []BreakdownItem{{Label: "CRA", Value: 3}} // only open issues count; WAT-1 completed, YTR-6 cancelled
	if len(got.Items) != 1 || got.Items[0] != want[0] {
		t.Fatalf("items = %+v, want %+v", got.Items, want)
	}
}

func TestDeliveryTopBlockerCallout(t *testing.T) {
	inv := newFakeInvoker().on("linear.list_issues", func() (gate.Result, error) { return jsonResult(deliveryFixture()) })
	co := testCompute(inv)
	got := calloutFuncs["top_blocker_issue"](context.Background(), co)
	if got.State != TileOK || got.Kind != "blocker" || got.Title != "CRA-3" || got.Value != 2 {
		t.Fatalf("callout = %+v, want CRA-3 blocking 2 open issues", got)
	}
}

func TestDeliveryTopBlockerCalloutEmptyIsUnavailableNotPanic(t *testing.T) {
	inv := newFakeInvoker().on("linear.list_issues", func() (gate.Result, error) { return jsonResult([]linear.Issue{}) })
	co := testCompute(inv)
	if got := calloutFuncs["top_blocker_issue"](context.Background(), co); got.State != TileUnavailable {
		t.Fatalf("state = %v, want unavailable on no issues", got.State)
	}
	if got := breakdownFuncs["issues_by_team"](context.Background(), co); got.State != TileUnavailable {
		t.Fatalf("breakdown state = %v, want unavailable on no issues", got.State)
	}
	if got := metricFuncs["open_issues"](context.Background(), co); got.State != TileOK || got.Value != 0 {
		t.Fatalf("open_issues = %+v, want ok/0 on an empty (but successfully read) issue list", got)
	}
}

// TestDeliveryTopBlockerTieBreaksAlphabetically: CRA-3 and CRA-4 each block
// exactly one open issue; CRA-3 sorts first.
func TestDeliveryTopBlockerTieBreaksAlphabetically(t *testing.T) {
	issues := []linear.Issue{
		{Identifier: "CRA-3", StateType: "started", Relations: []linear.Relation{{Type: "blocks", Identifier: "CRA-9", StateType: "unstarted"}}},
		{Identifier: "CRA-4", StateType: "started", Relations: []linear.Relation{{Type: "blocks", Identifier: "CRA-8", StateType: "unstarted"}}},
		{Identifier: "CRA-8", StateType: "unstarted"},
		{Identifier: "CRA-9", StateType: "unstarted"},
	}
	inv := newFakeInvoker().on("linear.list_issues", func() (gate.Result, error) { return jsonResult(issues) })
	co := testCompute(inv)
	got := calloutFuncs["top_blocker_issue"](context.Background(), co)
	if got.Title != "CRA-3" {
		t.Fatalf("title = %q, want CRA-3 (alphabetical tiebreak)", got.Title)
	}
}

// ---- Clients: exact numbers, breakdown and cross-connector callout ----

func clientsAccountsFixture() []any {
	return []any{
		[]any{"Meridian", "healthy", 1.0, 9.0, "2026-09-20"},
		[]any{"Northstar", "at_risk", 4.0, 5.0, "2026-08-01"},
		[]any{"Lexicon", "critical", 2.0, nil, "2026-07-15"},
	}
}

func TestClientsMetricsFromFixture(t *testing.T) {
	inv := newFakeInvoker().on("company_customers.accounts", func() (gate.Result, error) {
		return jsonResult(sheetRows{Rows: toRows(clientsAccountsFixture())})
	})
	co := testCompute(inv)
	ctx := context.Background()

	if got := metricFuncs["accounts_at_risk"](ctx, co); got.State != TileOK || got.Value != 2 {
		t.Fatalf("accounts_at_risk = %+v", got)
	}
	if got := metricFuncs["open_tickets"](ctx, co); got.State != TileOK || got.Value != 7 {
		t.Fatalf("open_tickets = %+v", got)
	}
	if got := metricFuncs["nps_score"](ctx, co); got.State != TileOK || got.Value != 7 { // (9+5)/2
		t.Fatalf("nps_score = %+v", got)
	}
}

func TestClientsAccountsByHealthBreakdown(t *testing.T) {
	inv := newFakeInvoker().on("company_customers.accounts", func() (gate.Result, error) {
		return jsonResult(sheetRows{Rows: toRows(clientsAccountsFixture())})
	})
	co := testCompute(inv)
	got := breakdownFuncs["accounts_by_health"](context.Background(), co)
	if got.State != TileOK || len(got.Items) != 3 {
		t.Fatalf("items = %+v", got.Items)
	}
}

func TestClientsSilentAccountCalloutJoinsFinanceInvoices(t *testing.T) {
	inv := newFakeInvoker().
		on("company_customers.accounts", func() (gate.Result, error) {
			return jsonResult(sheetRows{Rows: toRows(clientsAccountsFixture())})
		}).
		on("company_finance.outstanding_invoices", func() (gate.Result, error) {
			return jsonResult(sheetRows{Rows: toRows([]any{
				[]any{"inv-1", "Northstar", 5000.0, "2026-09-01", "Open"},
				[]any{"inv-2", "Lexicon", 2000.0, "2026-09-10", "Open"},
			})})
		})
	co := testCompute(inv)
	got := calloutFuncs["longest_silent_account_with_balance"](context.Background(), co)
	// Northstar: 2026-08-01 -> 57 days since contact (from testNow 2026-09-27); has a $5000 balance.
	// Lexicon: 2026-07-15 -> 74 days since contact; has a $2000 balance. Lexicon is more overdue.
	if got.State != TileOK || got.Kind != "silent_account" || got.Title != "Lexicon" {
		t.Fatalf("callout = %+v, want Lexicon (74 days, longer than Northstar's 57)", got)
	}
	if got.Value != 74 {
		t.Fatalf("days = %v, want 74", got.Value)
	}
}

// TestClientsSilentAccountCalloutDegradesWhenCustomersNotConnected is the
// spec's own explicit degrade-not-crash requirement: company_customers not
// configured yet must not silently omit the join or panic.
func TestClientsSilentAccountCalloutDegradesWhenCustomersNotConnected(t *testing.T) {
	inv := newFakeInvoker().
		on("company_customers.accounts", func() (gate.Result, error) { return gate.Result{}, gsheets.ErrCustomersNotConfigured }).
		on("company_finance.outstanding_invoices", func() (gate.Result, error) {
			return jsonResult(sheetRows{Rows: toRows([]any{[]any{"inv-1", "Northstar", 5000.0, "2026-09-01", "Open"}})})
		})
	co := testCompute(inv)
	got := calloutFuncs["longest_silent_account_with_balance"](context.Background(), co)
	if got.State != TileNotConnected {
		t.Fatalf("state = %v, want not_connected", got.State)
	}
}

// TestClientsSilentAccountCalloutDegradesWhenFinanceUnavailable: the
// customers side is fine but finance fails with something other than
// not_connected — unavailable, not a crash, not a wrong join.
func TestClientsSilentAccountCalloutDegradesWhenFinanceUnavailable(t *testing.T) {
	inv := newFakeInvoker().
		on("company_customers.accounts", func() (gate.Result, error) {
			return jsonResult(sheetRows{Rows: toRows(clientsAccountsFixture())})
		}).
		on("company_finance.outstanding_invoices", func() (gate.Result, error) {
			return gate.Result{}, fmt.Errorf("transient sheets api error")
		})
	co := testCompute(inv)
	got := calloutFuncs["longest_silent_account_with_balance"](context.Background(), co)
	if got.State != TileUnavailable {
		t.Fatalf("state = %v, want unavailable", got.State)
	}
}

// TestClientsMetricsEmptyIsUnavailableNotPanic
func TestClientsMetricsEmptyIsUnavailableNotPanic(t *testing.T) {
	inv := newFakeInvoker().on("company_customers.accounts", func() (gate.Result, error) { return jsonResult(sheetRows{}) })
	co := testCompute(inv)
	ctx := context.Background()
	for _, id := range []string{"accounts_at_risk", "open_tickets", "nps_score"} {
		if got := metricFuncs[id](ctx, co); got.State != TileUnavailable {
			t.Fatalf("%s = %+v, want unavailable on empty data", id, got)
		}
	}
	if got := breakdownFuncs["accounts_by_health"](ctx, co); got.State != TileUnavailable {
		t.Fatalf("accounts_by_health = %+v, want unavailable", got)
	}
}

// ---- Cache: dedup within TTL, refetch after, never P0 ----

func TestCacheDedupesAcrossManyDashboardRendersWithinTTL(t *testing.T) {
	inv := newFakeInvoker().
		on("company_finance.cash_position", func() (gate.Result, error) {
			return jsonResult(sheetValues{Values: map[string]any{"cash_usd": 1.0, "burn_usd": 1.0, "runway_months": 1.0}})
		}).
		on("company_finance.spend_breakdown_all", func() (gate.Result, error) { return jsonResult(sheetRows{}) }).
		on("company_finance.monthly_costs", func() (gate.Result, error) { return jsonResult(sheetRows{}) })
	co := testCompute(inv)
	spec := Spec{ID: "finance", Name: "Finance", Source: "company_finance",
		Metrics: []string{"cash_position", "monthly_burn", "runway_months"}, Breakdown: "spend_by_application", Callout: "worst_app_margin"}

	for i := 0; i < 5; i++ {
		co.Dashboard(context.Background(), spec)
	}
	for _, fn := range []string{"company_finance.cash_position", "company_finance.spend_breakdown_all", "company_finance.monthly_costs"} {
		if n := inv.callCount(fn); n != 1 {
			t.Errorf("%s called %d times across 5 renders within TTL, want 1", fn, n)
		}
	}
	// No call was ever made at P0: dashboards reserve P0 headroom for the
	// CEO's own immediate tool-call turn (docs/slices/UI.md Phase 4).
	for fn, origins := range inv.origins {
		for _, o := range origins {
			if o == gate.P0 {
				t.Fatalf("%s was called at gate.P0", fn)
			}
			if o != dashboardOrigin {
				t.Fatalf("%s was called at origin %v, want dashboardOrigin (%v)", fn, o, dashboardOrigin)
			}
		}
	}
}

func TestCacheRefetchesAfterTTLExpires(t *testing.T) {
	start, _ := time.Parse(time.RFC3339, testNow)
	cur := start
	inv := newFakeInvoker().on("company_finance.cash_position", func() (gate.Result, error) {
		return jsonResult(sheetValues{Values: map[string]any{"cash_usd": 1.0}})
	})
	co := &Compute{Gate: inv, Cache: &Cache{TTL: DefaultCacheTTL}, Now: func() time.Time { return cur }}

	co.Dashboard(context.Background(), Spec{Metrics: []string{"cash_position", "monthly_burn", "runway_months"}, Breakdown: "spend_by_application", Callout: "worst_app_margin"})
	if n := inv.callCount("company_finance.cash_position"); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
	cur = cur.Add(DefaultCacheTTL + time.Second)
	co.Dashboard(context.Background(), Spec{Metrics: []string{"cash_position", "monthly_burn", "runway_months"}, Breakdown: "spend_by_application", Callout: "worst_app_margin"})
	if n := inv.callCount("company_finance.cash_position"); n != 2 {
		t.Fatalf("calls after TTL expired = %d, want 2", n)
	}
}

// ---- registry contract with dashboards.go's closed id sets ----

func TestComputeRegistriesMatchDashboardsClosedIDSets(t *testing.T) {
	for id := range validMetricIDs {
		if _, ok := metricFuncs[id]; !ok {
			t.Errorf("validMetricIDs has %q but metricFuncs does not implement it", id)
		}
	}
	for id := range metricFuncs {
		if !validMetricIDs[id] {
			t.Errorf("metricFuncs implements %q, which is not in dashboards.go's validMetricIDs", id)
		}
	}
	for id := range validBreakdownIDs {
		if _, ok := breakdownFuncs[id]; !ok {
			t.Errorf("validBreakdownIDs has %q but breakdownFuncs does not implement it", id)
		}
	}
	for id := range breakdownFuncs {
		if !validBreakdownIDs[id] {
			t.Errorf("breakdownFuncs implements %q, which is not in dashboards.go's validBreakdownIDs", id)
		}
	}
	for id := range validCalloutIDs {
		if _, ok := calloutFuncs[id]; !ok {
			t.Errorf("validCalloutIDs has %q but calloutFuncs does not implement it", id)
		}
	}
	for id := range calloutFuncs {
		if !validCalloutIDs[id] {
			t.Errorf("calloutFuncs implements %q, which is not in dashboards.go's validCalloutIDs", id)
		}
	}
}

// toRows converts a []any of []any rows (test fixtures, written with float64
// literals so they match json.Unmarshal's own number decoding) into
// [][]any, exactly the shape sheetRows.Rows/json decoding produces.
func toRows(rows []any) [][]any {
	out := make([][]any, len(rows))
	for i, r := range rows {
		out[i] = r.([]any)
	}
	return out
}
