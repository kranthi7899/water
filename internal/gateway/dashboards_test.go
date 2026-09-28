package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"water/internal/dashboards"
	"water/internal/gate"
)

// TestGetDashboardsListsTheLoadedRegistryInOrder: the real CEO twin's three
// twins/ceo/dashboards/*.yaml specs (clients, delivery, finance, in id
// order) come back id, name and a human-readable source label, never the
// raw metrics/breakdown/callout ids (Phase 4's job, not this one's).
func TestGetDashboardsListsTheLoadedRegistryInOrder(t *testing.T) {
	srv, tok := newRegistryTestDaemon(t)

	if resp := do(t, srv.URL, "GET", "/v1/dashboards", "", ""); statusOf(resp) != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", statusOf(resp))
	}

	var out []dashboardListItem
	decodeInto(t, do(t, srv.URL, "GET", "/v1/dashboards", "", tok), http.StatusOK, &out)

	want := []dashboardListItem{
		{ID: "clients", Name: "Clients", Source: "Customers"},
		{ID: "delivery", Name: "Delivery", Source: "Linear (all teams)"},
		{ID: "finance", Name: "Finance", Source: "Finance"},
	}
	if len(out) != len(want) {
		t.Fatalf("dashboards = %+v, want %d entries", out, len(want))
	}
	for i, w := range want {
		if out[i] != w {
			t.Errorf("dashboards[%d] = %+v, want %+v", i, out[i], w)
		}
	}
}

// TestGetDashboardsWithNoRegistryReportsEmpty mirrors
// TestGetWorkspacesWithNoRegistryReportsEmpty: a daemon with no Dashboards
// registry configured answers an empty list, not an error.
func TestGetDashboardsWithNoRegistryReportsEmpty(t *testing.T) {
	h := newHarness(t)
	var out []dashboardListItem
	decodeInto(t, do(t, h.srv.URL, "GET", "/v1/dashboards", "", h.token), http.StatusOK, &out)
	if len(out) != 0 {
		t.Fatalf("dashboards = %+v, want empty with no registry configured", out)
	}
}

// stubInvoker answers every gate call with a fixed JSON payload, so a test
// can wire a real dashboards.Compute without a real gate/connector stack —
// internal/dashboards' own compute_test.go and compute_integration_test.go
// already cover the compute arithmetic itself in depth; this is only about
// proving the HTTP route wires Compute.Dashboard's result through.
type stubInvoker struct{ body string }

func (s stubInvoker) Invoke(context.Context, gate.Call) (gate.Result, error) {
	return gate.Result{Output: json.RawMessage(s.body)}, nil
}

// TestGetDashboardDetailReturnsComputedTiles: GET /v1/dashboards/{id} with
// a Compute configured serves real tile values, not just the list route's
// identity/name/source.
func TestGetDashboardDetailReturnsComputedTiles(t *testing.T) {
	srv, tok := newRegistryTestDaemonWithCompute(t, `{"values":{"cash_usd":250000,"burn_usd":40000,"runway_months":6.25}}`)

	if resp := do(t, srv.URL, "GET", "/v1/dashboards/finance", "", ""); statusOf(resp) != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", statusOf(resp))
	}

	var out dashboards.DashboardView
	decodeInto(t, do(t, srv.URL, "GET", "/v1/dashboards/finance", "", tok), http.StatusOK, &out)
	if out.ID != "finance" || out.Source != "Finance" {
		t.Fatalf("view = %+v", out)
	}
	if len(out.Metrics) != 3 {
		t.Fatalf("metrics = %+v, want 3", out.Metrics)
	}
	byID := map[string]dashboards.MetricTile{}
	for _, m := range out.Metrics {
		byID[m.ID] = m
	}
	if got := byID["cash_position"]; got.State != dashboards.TileOK || got.Value != 250000 {
		t.Fatalf("cash_position = %+v", got)
	}
}

// TestGetDashboardDetailUnknownIDIs404 mirrors handleGetThread/
// handleGetMeeting's own "unknown id" convention.
func TestGetDashboardDetailUnknownIDIs404(t *testing.T) {
	srv, tok := newRegistryTestDaemon(t)
	if resp := do(t, srv.URL, "GET", "/v1/dashboards/no-such-dashboard", "", tok); statusOf(resp) != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", statusOf(resp))
	}
}

// TestGetDashboardDetailWithNoComputeIsAllUnavailable: Compute is optional
// like Dashboards itself — a daemon with the registry but no Compute wired
// still answers every tile, just as "unavailable", rather than erroring.
func TestGetDashboardDetailWithNoComputeIsAllUnavailable(t *testing.T) {
	srv, tok := newRegistryTestDaemon(t)
	var out dashboards.DashboardView
	decodeInto(t, do(t, srv.URL, "GET", "/v1/dashboards/finance", "", tok), http.StatusOK, &out)
	if out.ID != "finance" || out.Source != "Finance" {
		t.Fatalf("view = %+v", out)
	}
	if len(out.Metrics) != 3 {
		t.Fatalf("metrics = %+v, want 3", out.Metrics)
	}
	for _, m := range out.Metrics {
		if m.State != dashboards.TileUnavailable {
			t.Errorf("metric %s state = %v, want unavailable with no Compute configured", m.ID, m.State)
		}
	}
	if out.Breakdown.State != dashboards.TileUnavailable || out.Callout.State != dashboards.TileUnavailable {
		t.Fatalf("breakdown/callout = %+v / %+v, want unavailable", out.Breakdown, out.Callout)
	}
}

// TestDashboardSourceLabelMapsEveryKnownSourceKind pins this task's own
// wording choices for every source kind internal/dashboards.ValidSource
// accepts, plus the fallback for an unrecognized one (which the registry
// itself would already have refused to load).
func TestDashboardSourceLabelMapsEveryKnownSourceKind(t *testing.T) {
	cases := map[string]string{
		"company_finance":    "Finance",
		"company_customers":  "Customers",
		"roster":             "Roster",
		"research":           "Research",
		"linear_all":         "Linear (all teams)",
		"linear_team:WAT":    "Linear: WAT",
		"github:acme/water":  "GitHub: acme/water",
		"some_unknown_thing": "some_unknown_thing",
	}
	for source, want := range cases {
		if got := dashboardSourceLabel(source); got != want {
			t.Errorf("dashboardSourceLabel(%q) = %q, want %q", source, got, want)
		}
	}
}
