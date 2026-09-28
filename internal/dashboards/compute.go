// compute.go implements docs/slices/UI.md Phase 4: the closed registry of
// metric, breakdown and callout functions dashboards.go's Spec already
// validates every twins/<id>/dashboards/*.yaml file's ids against
// (validMetricIDs, validBreakdownIDs, validCalloutIDs). Every function here
// reads a connector only through Gate.Invoke, at dashboardOrigin, fronted
// by Cache (cache.go) — never a hardcoded number, never a direct connector
// call.
package dashboards

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"water/internal/connectors/google/gsheets"
	"water/internal/connectors/linear"
	"water/internal/gate"
)

// TileState is how a dashboard tile got its value. Every metric, breakdown
// and callout below resolves to exactly one of these; a Go zero value is
// never shown as if it were a real "ok" number.
type TileState string

const (
	// TileOK is a real computed number or list, from a connector call that
	// actually returned usable data.
	TileOK TileState = "ok"
	// TileNotConnected is what a missing shared credential or an
	// unconfigured connector (company_customers before the owner uploads
	// its spreadsheet, docs/slices/UI.md U3-A) produces: there is nothing
	// to read yet, not a failure. See isNotConnected below for exactly
	// which two error shapes this matches.
	TileNotConnected TileState = "not_connected"
	// TileUnavailable is any other real failure: a transient API error, a
	// rate-cap refusal, a malformed sheet, or a cross-connector callout
	// whose join could not complete for some reason other than
	// not_connected.
	TileUnavailable TileState = "unavailable"
	// TileIllustrative is reserved for a later phase that might show a
	// placeholder trend before real history exists (docs/slices/UI.md
	// Phase 4's own note). Nothing in this phase's finance/delivery/
	// clients metrics produces it — every number here is real, or
	// explicitly not_connected/unavailable — but the state exists now so a
	// later phase can use it without a breaking change to this type.
	TileIllustrative TileState = "illustrative"
)

// dashboardOrigin is the gate.Origin every function below uses (docs/
// slices/UI.md Phase 4: "a low origin, reserving P0 headroom"). P1 ("a
// task the CEO approved or scheduled") matches this codebase's existing
// convention for a gated read that prepares a UI surface rather than
// answers a live conversational question: decisions.Builder's own
// card-evidence resolution uses Origin: gate.P1
// (internal/cli/twin.go's buildDecisionsTrigger), and so does
// internal/sync's meeting-prefetch read (internal/sync/prefetch.go:124,
// "prefetchCall"). internal/sync's own unattended cursor-polling tick uses
// P2 instead (internal/sync/sync.go:410) — that is a background loop with
// nobody watching, which a dashboard render is not: the CEO just opened
// the page. Either way, rateLimitFor (internal/gate/gate.go, commit
// 4a47519) treats P1 and P2 identically — both may use at most 75% of a
// function's rate cap, with the rest reserved for P0 — so this choice
// changes nothing about the P0-headroom guarantee itself; it only follows
// the closer precedent. Never gate.P0: that is reserved for the CEO's own
// immediate tool-call turn.
const dashboardOrigin = gate.P1

// Invoker is the one gate method dashboard computations need. *gate.Gate
// satisfies it in production; tests use a fake that never talks to a real
// connector.
type Invoker interface {
	Invoke(ctx context.Context, c gate.Call) (gate.Result, error)
}

// Compute is Phase 4's closed registry of metric, breakdown and callout
// functions, keyed by the exact ids dashboards.go's Spec already
// validates.
type Compute struct {
	Gate  Invoker
	Cache *Cache
	// Now returns the current time; nil uses time.Now. Overridden in tests
	// so "days since last contact" and "a month more than 1.5x the
	// median" are deterministic instead of depending on the real clock —
	// matching internal/runtime.Env.Now's own nil-defaults-to-time.Now
	// convention (internal/runtime/runtime.go), this package's closest
	// precedent for a pluggable clock.
	Now func() time.Time
}

func (co *Compute) now() time.Time {
	if co != nil && co.Now != nil {
		return co.Now()
	}
	return time.Now()
}

// MetricTile is one metric's computed value.
type MetricTile struct {
	ID    string    `json:"id"`
	Value float64   `json:"value,omitempty"`
	State TileState `json:"state"`
}

// BreakdownItem is one row of a breakdown (one application, team or
// account health group).
type BreakdownItem struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

// BreakdownTile is one dashboard's breakdown.
type BreakdownTile struct {
	ID    string          `json:"id"`
	Items []BreakdownItem `json:"items,omitempty"`
	State TileState       `json:"state"`
}

// CalloutTile is one dashboard's callout: a single highlighted subject
// (an application, an issue, an account, or a month) plus a code-built
// Detail phrase — never model text, matching this codebase's other
// code-built, never-model-phrased UI facts (docs/slices/UI.md U1's
// simulated-reply note makes the same "code-built, never from model text"
// argument for a different surface).
type CalloutTile struct {
	ID string `json:"id"`
	// Kind says which underlying check produced this callout, when a
	// callout id folds together more than one check (see
	// financeWorstAppMarginCallout below): "margin_gap" or "cost_spike"
	// for worst_app_margin, "blocker" for top_blocker_issue,
	// "silent_account" for longest_silent_account_with_balance.
	Kind   string    `json:"kind,omitempty"`
	Title  string    `json:"title,omitempty"`
	Detail string    `json:"detail,omitempty"`
	Value  float64   `json:"value,omitempty"`
	State  TileState `json:"state"`
}

// DashboardView is GET /v1/dashboards/{id}'s payload: one spec's identity
// plus its three computed metrics, one breakdown and one callout.
type DashboardView struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Source    string        `json:"source"`
	Metrics   []MetricTile  `json:"metrics"`
	Breakdown BreakdownTile `json:"breakdown"`
	Callout   CalloutTile   `json:"callout"`
}

// Dashboard computes every tile for one loaded Spec. It never returns an
// error: an id spec.Validate already vouched for but this registry somehow
// doesn't recognize (which should never happen — see
// TestComputeRegistriesMatchDashboardsClosedIDSets) degrades to
// TileUnavailable rather than panicking or omitting the tile.
func (co *Compute) Dashboard(ctx context.Context, spec Spec) DashboardView {
	out := DashboardView{ID: spec.ID, Name: spec.Name, Source: spec.Source}
	for _, m := range spec.Metrics {
		t := MetricTile{ID: m, State: TileUnavailable}
		if fn, ok := metricFuncs[m]; ok {
			t = fn(ctx, co)
			t.ID = m
		}
		out.Metrics = append(out.Metrics, t)
	}
	out.Breakdown = BreakdownTile{ID: spec.Breakdown, State: TileUnavailable}
	if fn, ok := breakdownFuncs[spec.Breakdown]; ok {
		out.Breakdown = fn(ctx, co)
		out.Breakdown.ID = spec.Breakdown
	}
	out.Callout = CalloutTile{ID: spec.Callout, State: TileUnavailable}
	if fn, ok := calloutFuncs[spec.Callout]; ok {
		out.Callout = fn(ctx, co)
		out.Callout.ID = spec.Callout
	}
	return out
}

// ---- error classification ----

// isNotConnected matches exactly two error shapes, both meaning "there is
// nothing to read yet", never a real failure:
//   - the gate's own credential-missing denial (internal/gate/gate.go:239,
//     deny("credential for %s is unavailable", conn.Name())), matched
//     structurally through gate.DenyError rather than by guessing the
//     whole message;
//   - gsheets.ErrCustomersNotConfigured, company_customers' own
//     not-yet-configured sentinel (the owner hasn't uploaded
//     Renaissance_Customers.xlsx yet, docs/slices/UI.md U3-A).
func isNotConnected(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gsheets.ErrCustomersNotConfigured) {
		return true
	}
	var de *gate.DenyError
	if errors.As(err, &de) {
		return strings.HasPrefix(de.Reason, "credential for ") && strings.HasSuffix(de.Reason, " is unavailable")
	}
	return false
}

// IsNotConnected exports isNotConnected's own gate-DenyError/
// gsheets.ErrCustomersNotConfigured classification for a caller outside
// this package that needs the exact same "not_connected" detection this
// registry's own tiles use — docs/slices/UI.md Phase 5a's workspace
// control-room tiles (internal/gateway's new workspace-detail handler),
// which reuse this rather than inventing a second not-connected check for
// e.g. a missing GitHub credential. See isNotConnected's own doc comment
// for exactly which two error shapes this matches.
func IsNotConnected(err error) bool { return isNotConnected(err) }

// classify turns a fetch error into a tile state.
func classify(err error) TileState {
	if err == nil {
		return TileOK
	}
	if isNotConnected(err) {
		return TileNotConnected
	}
	return TileUnavailable
}

// ---- gated, cached fetch ----

// fetchJSON invokes fn through the gate at dashboardOrigin with no
// arguments (every function this registry calls takes none), decodes its
// Output as T, and caches the result (success or failure) under key fn —
// so every tile that reads the same underlying function (e.g. finance's
// three metrics, all reading company_finance.cash_position) shares one
// gated call per Cache.TTL window.
func fetchJSON[T any](ctx context.Context, co *Compute, fn string) (T, error) {
	return Get(co.Cache, co.now(), fn, func() (T, error) {
		var zero T
		if co == nil || co.Gate == nil {
			return zero, fmt.Errorf("dashboards: no gate configured")
		}
		res, err := co.Gate.Invoke(ctx, gate.Call{Function: fn, Origin: dashboardOrigin, Taint: gate.Clean})
		if err != nil {
			return zero, err
		}
		var v T
		if err := json.Unmarshal(res.Output, &v); err != nil {
			return zero, fmt.Errorf("dashboards: decoding %s: %w", fn, err)
		}
		return v, nil
	})
}

// sheetValues is company_finance.cash_position's own {"values": {...}}
// output shape (internal/connectors/google/gsheets.valuesOutput).
type sheetValues struct {
	Values map[string]any `json:"values"`
}

// sheetRows is the raw-grid output shape every no-argument, "return every
// row" gsheets function uses (spend_breakdown_all, monthly_costs,
// company_customers.accounts): {"rows": [][]any, ...}.
type sheetRows struct {
	Rows [][]any `json:"rows"`
}

func numberField(m map[string]any, key string) (float64, bool) {
	f, ok := m[key].(float64)
	return f, ok
}

// cellString/cellFloat read one row's column by index, tolerating a short
// row or the wrong JSON type rather than panicking — a row from a sheet
// the owner is still editing may simply be missing a trailing cell.
func cellString(row []any, i int) (string, bool) {
	if i < 0 || i >= len(row) {
		return "", false
	}
	s, ok := row[i].(string)
	return s, ok
}

func cellFloat(row []any, i int) (float64, bool) {
	if i < 0 || i >= len(row) {
		return 0, false
	}
	switch v := row[i].(type) {
	case float64:
		return v, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil
	}
	return 0, false
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// ---- exported reuse for Phase 5a's workspace control-room tiles ----
//
// docs/slices/UI.md Phase 5a reuses this package's own gated, cached,
// classified fetch (fetchJSON/classify above) for the Finance and Clients
// workspace tiles, rather than inventing a second fetch/cache/classify
// mechanism in internal/gateway: Rows gives the same raw-grid shape
// financeSpendBreakdown/clientsAccounts/clientsSilentAccountCallout above
// already fetch (company_finance.outstanding_invoices,
// company_customers.accounts, ...), and Issues gives the same
// linear.list_issues read deliveryIssues above already fetches (so a
// project workspace's Linear-based tiles share the Delivery dashboard's own
// Cache entry, not a second gated call). Both are read-only: neither writes
// anything and neither can create a decision.

// Rows fetches fn's output as raw sheet rows (the shape every no-argument,
// "return every row" gsheets function uses — sheetRows above), through the
// same Gate.Invoke/Cache/classify path fetchJSON gives this package's own
// metric/breakdown/callout functions.
func Rows(ctx context.Context, co *Compute, fn string) ([][]any, TileState, error) {
	v, err := fetchJSON[sheetRows](ctx, co, fn)
	return v.Rows, classify(err), err
}

// Issues fetches linear.list_issues the same cached, gated way
// deliveryIssues does, so a Phase 5a project workspace's Linear-based tiles
// (progress share, building-now, blocked issues) share one cached call with
// any open Delivery dashboard within Cache.TTL, rather than doubling the
// gated read.
func Issues(ctx context.Context, co *Compute) ([]linear.Issue, TileState, error) {
	v, err := deliveryIssues(ctx, co)
	return v, classify(err), err
}

// ---- Finance ----

// financeSpendColumns documents this phase's best-effort column mapping
// for the "Spend by Application" tab (gsheets.lookups["spend_breakdown"]/
// ["spend_breakdown_all"], A5:H11) — real column positions are unconfirmed
// pending live sheet access, the same limitation gsheets.go's own package
// doc comment already states for spend_breakdown's per-application lookup.
// Column A application name, column E total monthly spend (USD), column F
// total monthly revenue (USD). Correct these indices against the owner's
// real sheet's header row once it exists (see docs/google-setup.md).
const (
	financeColApplication = 0
	financeColSpend       = 4 // column E
	financeColRevenue     = 5 // column F
)

// monthlyCostColumns documents the assumed column mapping for the
// "Monthly P&L" tab (gsheets.lookups["monthly_costs"], A5:D16): column A
// month label, column B revenue, column C cost, column D profit. Same
// unconfirmed-pending-real-sheet caveat as financeSpendColumns above.
const (
	monthColLabel = 0
	monthColCost  = 2 // column C
)

func financeCashMetric(field string) func(context.Context, *Compute) MetricTile {
	return func(ctx context.Context, co *Compute) MetricTile {
		v, err := fetchJSON[sheetValues](ctx, co, "company_finance.cash_position")
		if err != nil {
			return MetricTile{State: classify(err)}
		}
		f, ok := numberField(v.Values, field)
		if !ok {
			return MetricTile{State: TileUnavailable}
		}
		return MetricTile{Value: f, State: TileOK}
	}
}

func financeSpendBreakdown(ctx context.Context, co *Compute) BreakdownTile {
	rows, err := fetchJSON[sheetRows](ctx, co, "company_finance.spend_breakdown_all")
	if err != nil {
		return BreakdownTile{State: classify(err)}
	}
	var items []BreakdownItem
	for _, row := range rows.Rows {
		name, ok := cellString(row, financeColApplication)
		if !ok || name == "" {
			continue
		}
		spend, ok := cellFloat(row, financeColSpend)
		if !ok {
			continue
		}
		items = append(items, BreakdownItem{Label: name, Value: spend})
	}
	if items == nil {
		return BreakdownTile{State: TileUnavailable}
	}
	return BreakdownTile{Items: items, State: TileOK}
}

// worstAppMargin returns the application with the largest positive
// spend-revenue gap (an app spending more than it earns), and whether any
// row qualified. Ties (two applications with the same worst gap) are
// broken by earliest row: the first one encountered wins, since a sheet's
// own row order is the only stable, deterministic ordering available here
// (there is no secondary sort key like a timestamp on this tab).
func worstAppMargin(rows [][]any) (app string, gap float64, ok bool) {
	best := -1.0
	for _, row := range rows {
		name, nameOK := cellString(row, financeColApplication)
		spend, spendOK := cellFloat(row, financeColSpend)
		revenue, revOK := cellFloat(row, financeColRevenue)
		if !nameOK || !spendOK || !revOK || name == "" {
			continue
		}
		g := spend - revenue
		if g > 0 && g > best {
			best, app, ok = g, name, true
		}
	}
	return app, best, ok
}

// costSpike returns the month whose cost is more than 1.5x the median cost
// across every month, and the dollar excess over that threshold — the
// single most severe such month if more than one qualifies (ties broken
// the same way worstAppMargin's are: earliest row wins).
func costSpike(rows [][]any) (month string, excess float64, ok bool) {
	var costs []float64
	for _, row := range rows {
		if c, ok := cellFloat(row, monthColCost); ok {
			costs = append(costs, c)
		}
	}
	if len(costs) == 0 {
		return "", 0, false
	}
	threshold := 1.5 * median(costs)
	best := -1.0
	for _, row := range rows {
		label, labelOK := cellString(row, monthColLabel)
		cost, costOK := cellFloat(row, monthColCost)
		if !labelOK || !costOK {
			continue
		}
		if cost > threshold && cost-threshold > best {
			best, month, ok = cost-threshold, label, true
		}
	}
	return month, best, ok
}

// financeWorstAppMarginCallout answers dashboards.go's single
// "worst_app_margin" callout id with whichever of docs/slices/UI.md Phase
// 4's *two* named finance callout checks is more severe: the largest
// spend-revenue gap across applications, or a month whose cost is more
// than 1.5x the median (new function monthly_costs). Phase 2's own
// registry (dashboards.go's validCalloutIDs) names only one finance
// callout id, so rather than widen Spec.Callout to a list (a bigger,
// riskier change touching the YAML shape, the gateway list endpoint and
// the web/Swift allowlists for a single-phase judgment call), both checks
// are always computed and compared — never silently dropped — and the
// more severe one (by dollar magnitude: the margin gap in USD, or the
// spike's excess-over-1.5x-median in USD, so the two are directly
// comparable) fills the one slot. Kind on the returned tile says which
// check won, so the UI can still label it correctly.
func financeWorstAppMarginCallout(ctx context.Context, co *Compute) CalloutTile {
	spendRows, errSpend := fetchJSON[sheetRows](ctx, co, "company_finance.spend_breakdown_all")
	costRows, errCost := fetchJSON[sheetRows](ctx, co, "company_finance.monthly_costs")

	app, gap, haveMargin := "", 0.0, false
	if errSpend == nil {
		app, gap, haveMargin = worstAppMargin(spendRows.Rows)
	}
	month, excess, haveSpike := "", 0.0, false
	if errCost == nil {
		month, excess, haveSpike = costSpike(costRows.Rows)
	}

	switch {
	case haveMargin && haveSpike && excess > gap:
		return CalloutTile{Kind: "cost_spike", Title: month, Value: excess, State: TileOK,
			Detail: fmt.Sprintf("%s cost is more than 1.5x the median month (+$%.0f)", month, excess)}
	case haveMargin:
		return CalloutTile{Kind: "margin_gap", Title: app, Value: gap, State: TileOK,
			Detail: fmt.Sprintf("%s is spending $%.0f more than it earns", app, gap)}
	case haveSpike:
		return CalloutTile{Kind: "cost_spike", Title: month, Value: excess, State: TileOK,
			Detail: fmt.Sprintf("%s cost is more than 1.5x the median month (+$%.0f)", month, excess)}
	}
	// Neither check produced a usable answer: report whichever error is
	// more informative (not_connected beats unavailable — it is the more
	// common, more actionable state today, per docs/slices/UI.md U3), or
	// unavailable for an empty-but-successful read (no qualifying
	// application or month — an empty dataset, not a crash).
	if classify(errSpend) == TileNotConnected || classify(errCost) == TileNotConnected {
		return CalloutTile{State: TileNotConnected}
	}
	return CalloutTile{State: TileUnavailable}
}

// ---- Delivery (Linear) ----

// deliveryOpen/closedStateTypes: Linear's own coarse workflow state.type
// values are triage|backlog|unstarted|started|completed|cancelled. "Open"
// here means anything that is not a finished state.
func isOpenStateType(t string) bool {
	return t != "completed" && t != "cancelled"
}

func deliveryIssues(ctx context.Context, co *Compute) ([]linear.Issue, error) {
	return fetchJSON[[]linear.Issue](ctx, co, "linear.list_issues")
}

func deliveryOpenIssues(ctx context.Context, co *Compute) MetricTile {
	issues, err := deliveryIssues(ctx, co)
	if err != nil {
		return MetricTile{State: classify(err)}
	}
	n := 0
	for _, is := range issues {
		if isOpenStateType(is.StateType) {
			n++
		}
	}
	return MetricTile{Value: float64(n), State: TileOK}
}

// deliveryBlockedIssues counts open issues that have an inverse "blocks"
// relation whose blocking issue is itself still open — an issue is only
// "blocked" while whatever blocks it hasn't been resolved.
func deliveryBlockedIssues(ctx context.Context, co *Compute) MetricTile {
	issues, err := deliveryIssues(ctx, co)
	if err != nil {
		return MetricTile{State: classify(err)}
	}
	n := 0
	for _, is := range issues {
		if !isOpenStateType(is.StateType) {
			continue
		}
		for _, rel := range is.InverseRelations {
			if rel.Type == "blocks" && isOpenStateType(rel.StateType) {
				n++
				break
			}
		}
	}
	return MetricTile{Value: float64(n), State: TileOK}
}

func deliveryUrgentIssues(ctx context.Context, co *Compute) MetricTile {
	issues, err := deliveryIssues(ctx, co)
	if err != nil {
		return MetricTile{State: classify(err)}
	}
	n := 0
	for _, is := range issues {
		if is.PriorityRank == 1 {
			n++
		}
	}
	return MetricTile{Value: float64(n), State: TileOK}
}

// deliveryIssuesByTeam breaks down open issues by team key — the open
// workload per team, the dimension that pairs naturally with the
// open/blocked/urgent metrics above (a team with every issue closed would
// otherwise show up identically to one with none at all).
func deliveryIssuesByTeam(ctx context.Context, co *Compute) BreakdownTile {
	issues, err := deliveryIssues(ctx, co)
	if err != nil {
		return BreakdownTile{State: classify(err)}
	}
	counts := map[string]float64{}
	var order []string
	for _, is := range issues {
		if !isOpenStateType(is.StateType) {
			continue
		}
		team := is.Team
		if team == "" {
			team = "(no team)"
		}
		if _, seen := counts[team]; !seen {
			order = append(order, team)
		}
		counts[team]++
	}
	if len(order) == 0 {
		return BreakdownTile{State: TileUnavailable}
	}
	sort.Strings(order)
	items := make([]BreakdownItem, 0, len(order))
	for _, team := range order {
		items = append(items, BreakdownItem{Label: team, Value: counts[team]})
	}
	return BreakdownTile{Items: items, State: TileOK}
}

// deliveryTopBlockerCallout finds the issue whose own "blocks" relations
// name the most other still-open issues — a small graph computation over
// already-fetched relations, never a second API call. A tie (two issues
// blocking the same number of open issues) is broken alphabetically by
// identifier, for a deterministic, order-independent result regardless of
// the page order Linear happened to return.
func deliveryTopBlockerCallout(ctx context.Context, co *Compute) CalloutTile {
	issues, err := deliveryIssues(ctx, co)
	if err != nil {
		return CalloutTile{State: classify(err)}
	}
	openByIdentifier := map[string]bool{}
	for _, is := range issues {
		openByIdentifier[is.Identifier] = isOpenStateType(is.StateType)
	}
	counts := map[string]int{}
	for _, is := range issues {
		for _, rel := range is.Relations {
			if rel.Type != "blocks" {
				continue
			}
			if open, known := openByIdentifier[rel.Identifier]; known && open {
				counts[is.Identifier]++
			} else if !known && isOpenStateType(rel.StateType) {
				// The blocked issue wasn't itself in this page's result set
				// (e.g. a very large workspace); fall back to the state
				// this relation edge itself reported.
				counts[is.Identifier]++
			}
		}
	}
	best, bestCount := "", 0
	for id, n := range counts {
		if n > bestCount || (n == bestCount && n > 0 && id < best) {
			best, bestCount = id, n
		}
	}
	if bestCount == 0 {
		return CalloutTile{State: TileUnavailable}
	}
	return CalloutTile{Kind: "blocker", Title: best, Value: float64(bestCount), State: TileOK,
		Detail: fmt.Sprintf("%s blocks %d open issue(s)", best, bestCount)}
}

// ---- Clients (company_customers) ----

// accountColumns documents the Accounts tab's row schema, restated from
// customers.go's own package doc comment for this file's easier reference
// (corrected 2026-09-27 against the owner's real Renaissance_Customers.xlsx):
// column B name, column J health, column M last interaction date, column O
// open tickets, column P latest NPS (blank if none). Columns A, C-I, K, L, N
// and Q exist in the real sheet but nothing here reads them.
const (
	accountColName        = 1
	accountColHealth      = 9
	accountColLastContact = 12
	accountColTickets     = 14
	accountColNPS         = 15
)

func clientsAccounts(ctx context.Context, co *Compute) (sheetRows, error) {
	return fetchJSON[sheetRows](ctx, co, "company_customers.accounts")
}

func clientsAccountsAtRisk(ctx context.Context, co *Compute) MetricTile {
	rows, err := clientsAccounts(ctx, co)
	if err != nil {
		return MetricTile{State: classify(err)}
	}
	n, any := 0, false
	for _, row := range rows.Rows {
		name, ok := cellString(row, accountColName)
		if !ok || name == "" {
			continue
		}
		any = true
		health, _ := cellString(row, accountColHealth)
		if !strings.EqualFold(strings.TrimSpace(health), "healthy") {
			n++
		}
	}
	if !any {
		return MetricTile{State: TileUnavailable}
	}
	return MetricTile{Value: float64(n), State: TileOK}
}

func clientsOpenTickets(ctx context.Context, co *Compute) MetricTile {
	rows, err := clientsAccounts(ctx, co)
	if err != nil {
		return MetricTile{State: classify(err)}
	}
	sum, any := 0.0, false
	for _, row := range rows.Rows {
		name, ok := cellString(row, accountColName)
		if !ok || name == "" {
			continue
		}
		if tickets, ok := cellFloat(row, accountColTickets); ok {
			sum += tickets
			any = true
		}
	}
	if !any {
		return MetricTile{State: TileUnavailable}
	}
	return MetricTile{Value: sum, State: TileOK}
}

func clientsNPS(ctx context.Context, co *Compute) MetricTile {
	rows, err := clientsAccounts(ctx, co)
	if err != nil {
		return MetricTile{State: classify(err)}
	}
	var scores []float64
	for _, row := range rows.Rows {
		if s, ok := cellFloat(row, accountColNPS); ok {
			scores = append(scores, s)
		}
	}
	if len(scores) == 0 {
		return MetricTile{State: TileUnavailable}
	}
	sum := 0.0
	for _, s := range scores {
		sum += s
	}
	return MetricTile{Value: sum / float64(len(scores)), State: TileOK}
}

func clientsAccountsByHealth(ctx context.Context, co *Compute) BreakdownTile {
	rows, err := clientsAccounts(ctx, co)
	if err != nil {
		return BreakdownTile{State: classify(err)}
	}
	counts := map[string]float64{}
	var order []string
	for _, row := range rows.Rows {
		name, ok := cellString(row, accountColName)
		if !ok || name == "" {
			continue
		}
		health, _ := cellString(row, accountColHealth)
		health = strings.TrimSpace(health)
		if health == "" {
			health = "(unknown)"
		}
		if _, seen := counts[health]; !seen {
			order = append(order, health)
		}
		counts[health]++
	}
	if len(order) == 0 {
		return BreakdownTile{State: TileUnavailable}
	}
	sort.Strings(order)
	items := make([]BreakdownItem, 0, len(order))
	for _, h := range order {
		items = append(items, BreakdownItem{Label: h, Value: counts[h]})
	}
	return BreakdownTile{Items: items, State: TileOK}
}

// invoiceColumns documents the assumed column mapping for the Invoices tab
// (gsheets.lookups["outstanding_invoices"], A5:I13), consumed only by this
// cross-connector callout: column B (index 1) customer/account name,
// column C (index 2) amount outstanding (USD) — matching the shape of
// gsheets_test.go's own outstanding_invoices fixture
// (["inv-1", "Acme", 5000, "2026-09-01", "Open"]).
const (
	invoiceColAccount = 1
	invoiceColAmount  = 2
)

// clientsSilentAccountCallout joins company_customers.accounts (last
// contact date) with company_finance.outstanding_invoices (amount
// outstanding by account name) to find the account with the longest time
// since contact that also has money outstanding. If either source cannot
// be read, the join cannot be done: this degrades to not_connected (if
// either side is specifically not_connected) or unavailable (any other
// failure), never a crash or a silently incomplete answer. A tie (two
// accounts equally overdue) is broken alphabetically by account name.
func clientsSilentAccountCallout(ctx context.Context, co *Compute) CalloutTile {
	accRows, accErr := clientsAccounts(ctx, co)
	invRows, invErr := fetchJSON[sheetRows](ctx, co, "company_finance.outstanding_invoices")
	if accErr != nil || invErr != nil {
		if classify(accErr) == TileNotConnected || classify(invErr) == TileNotConnected {
			return CalloutTile{State: TileNotConnected}
		}
		return CalloutTile{State: TileUnavailable}
	}

	balances := map[string]float64{}
	for _, row := range invRows.Rows {
		name, ok := cellString(row, invoiceColAccount)
		if !ok {
			continue
		}
		amt, ok := cellFloat(row, invoiceColAmount)
		if !ok {
			continue
		}
		balances[strings.ToLower(strings.TrimSpace(name))] += amt
	}

	now := co.now()
	bestName, bestDays, bestBalance := "", -1.0, 0.0
	for _, row := range accRows.Rows {
		name, ok := cellString(row, accountColName)
		if !ok || name == "" {
			continue
		}
		bal, hasBal := balances[strings.ToLower(strings.TrimSpace(name))]
		if !hasBal || bal <= 0 {
			continue
		}
		lastContact, ok := cellString(row, accountColLastContact)
		if !ok {
			continue
		}
		t, perr := time.Parse("2006-01-02", strings.TrimSpace(lastContact))
		if perr != nil {
			continue
		}
		days := now.Sub(t).Hours() / 24
		if days < 0 {
			continue
		}
		if days > bestDays || (days == bestDays && (bestName == "" || name < bestName)) {
			bestName, bestDays, bestBalance = name, days, bal
		}
	}
	if bestDays < 0 {
		return CalloutTile{State: TileUnavailable}
	}
	return CalloutTile{Kind: "silent_account", Title: bestName, Value: bestDays, State: TileOK,
		Detail: fmt.Sprintf("%s: %.0f days since contact, $%.0f outstanding", bestName, bestDays, bestBalance)}
}

// ---- the closed registries ----
//
// Every key here must exactly match dashboards.go's validMetricIDs/
// validBreakdownIDs/validCalloutIDs (see
// TestComputeRegistriesMatchDashboardsClosedIDSets) — this is Phase 4's
// contract with Phase 2's already-shipped spec loader.

var metricFuncs = map[string]func(context.Context, *Compute) MetricTile{
	"cash_position":    financeCashMetric("cash_usd"),
	"monthly_burn":     financeCashMetric("burn_usd"),
	"runway_months":    financeCashMetric("runway_months"),
	"open_issues":      deliveryOpenIssues,
	"blocked_issues":   deliveryBlockedIssues,
	"urgent_issues":    deliveryUrgentIssues,
	"accounts_at_risk": clientsAccountsAtRisk,
	"open_tickets":     clientsOpenTickets,
	"nps_score":        clientsNPS,
}

var breakdownFuncs = map[string]func(context.Context, *Compute) BreakdownTile{
	"spend_by_application": financeSpendBreakdown,
	"issues_by_team":       deliveryIssuesByTeam,
	"accounts_by_health":   clientsAccountsByHealth,
}

var calloutFuncs = map[string]func(context.Context, *Compute) CalloutTile{
	"worst_app_margin":                    financeWorstAppMarginCallout,
	"top_blocker_issue":                   deliveryTopBlockerCallout,
	"longest_silent_account_with_balance": clientsSilentAccountCallout,
}
