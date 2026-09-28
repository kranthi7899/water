package gateway

import (
	"net/http"
	"strings"

	"water/internal/dashboards"
)

// dashboardListItem is one entry of GET /v1/dashboards: a dashboard's
// identity, its display name, and a short human-readable label for its data
// source, straight from the loaded twins/<id>/dashboards/*.yaml spec
// (internal/dashboards.Spec). No metrics, breakdown or callout values —
// those need Phase 4's compute registry, which does not exist yet.
type dashboardListItem struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Source string `json:"source"`
}

// dashboardSourceLabel turns an internal/dashboards source-kind string
// (internal/dashboards.ValidSource's closed set: every
// internal/workspaces.ValidSource kind, plus "linear_all") into a short
// label a person reads, rather than the raw connector-shaped id. This is
// this task's own judgment call on wording, not something docs/slices/UI.md
// spells out verbatim:
//   - the three fixed company-wide sources get a plain noun ("Finance",
//     "Customers", "Roster", "Research");
//   - "linear_all" (Delivery's cross-team source) reads "Linear (all teams)";
//   - "linear_team:<KEY>" reads "Linear: <KEY>" and "github:<owner/repo>"
//     reads "GitHub: <owner/repo>", both keeping the identifying suffix
//     since that is the only thing distinguishing one team or repo from
//     another;
//   - anything else (a source kind the registry would already have refused
//     at load time) falls back to the raw string, so the UI always has
//     something to show rather than an empty label.
func dashboardSourceLabel(source string) string {
	switch source {
	case "company_finance":
		return "Finance"
	case "company_customers":
		return "Customers"
	case "roster":
		return "Roster"
	case "research":
		return "Research"
	case "linear_all":
		return "Linear (all teams)"
	}
	if key, ok := strings.CutPrefix(source, "linear_team:"); ok {
		return "Linear: " + key
	}
	if repo, ok := strings.CutPrefix(source, "github:"); ok {
		return "GitHub: " + repo
	}
	return source
}

// handleListDashboards serves GET /v1/dashboards: the sidebar's Dashboards
// page lists every dashboard this twin loaded at startup, in the registry's
// own (id) order. d.cfg.Dashboards is optional, like d.cfg.Workspaces: a nil
// registry answers an empty list rather than erroring.
func (d *Daemon) handleListDashboards(w http.ResponseWriter, r *http.Request) {
	out := []dashboardListItem{}
	if d.cfg.Dashboards != nil {
		for _, s := range d.cfg.Dashboards.Specs() {
			out = append(out, dashboardListItem{ID: s.ID, Name: s.Name, Source: dashboardSourceLabel(s.Source)})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetDashboard serves GET /v1/dashboards/{id}: one dashboard's actual
// computed tiles (docs/slices/UI.md Phase 4) — its three metrics, one
// breakdown and one callout, each carrying a value and a tile state
// (internal/dashboards.TileOK/TileNotConnected/TileUnavailable/
// TileIllustrative). A dashboard id not in the loaded registry (or no
// registry at all) is a 404, matching handleGetThread/handleGetMeeting's
// own convention for an unknown {id}. d.cfg.Compute is optional like
// d.cfg.Dashboards itself: a nil Compute answers every tile as
// "unavailable" instead of erroring — the same "missing optional
// dependency degrades, never crashes" posture every other Config field
// here already has (NeedsYou, Decisions, Workspaces, Dashboards).
func (d *Daemon) handleGetDashboard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if d.cfg.Dashboards == nil {
		http.Error(w, "no such dashboard", http.StatusNotFound)
		return
	}
	spec, ok := d.cfg.Dashboards.Spec(id)
	if !ok {
		http.Error(w, "no such dashboard", http.StatusNotFound)
		return
	}
	var view dashboards.DashboardView
	if d.cfg.Compute == nil {
		view = dashboards.DashboardView{
			ID: spec.ID, Name: spec.Name,
			Breakdown: dashboards.BreakdownTile{ID: spec.Breakdown, State: dashboards.TileUnavailable},
			Callout:   dashboards.CalloutTile{ID: spec.Callout, State: dashboards.TileUnavailable},
		}
		for _, m := range spec.Metrics {
			view.Metrics = append(view.Metrics, dashboards.MetricTile{ID: m, State: dashboards.TileUnavailable})
		}
	} else {
		view = d.cfg.Compute.Dashboard(r.Context(), spec)
	}
	view.Source = dashboardSourceLabel(spec.Source)
	writeJSON(w, http.StatusOK, view)
}
