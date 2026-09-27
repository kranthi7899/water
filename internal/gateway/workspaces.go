package gateway

import "net/http"

// workspaceListItem is one entry of GET /v1/workspaces: a workspace's
// identity, its template (which page layout the UI would render for it, a
// later phase's job) and its primary data source, straight from the loaded
// twins/<id>/workspaces/*.yaml spec (internal/workspaces.Spec). No
// membership or computed data — that is Phase 5's job.
type workspaceListItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Template string `json:"template"`
	Source   string `json:"source"`
}

// handleListWorkspaces serves GET /v1/workspaces: the sidebar's Workspaces
// disclosure (docs/slices/UI.md Phase 2) lists every workspace this twin
// loaded at startup, in the registry's own (id) order. d.cfg.Workspaces is
// optional, like d.cfg.NeedsYou and d.cfg.Decisions: a nil registry answers
// an empty list rather than erroring.
func (d *Daemon) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	out := []workspaceListItem{}
	if d.cfg.Workspaces != nil {
		for _, s := range d.cfg.Workspaces.Specs() {
			out = append(out, workspaceListItem{ID: s.ID, Name: s.Name, Template: s.Template, Source: s.Source})
		}
	}
	writeJSON(w, http.StatusOK, out)
}
