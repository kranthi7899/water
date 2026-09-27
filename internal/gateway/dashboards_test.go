package gateway

import (
	"net/http"
	"testing"
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
