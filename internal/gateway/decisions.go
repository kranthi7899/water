package gateway

import (
	"net/http"
	"time"

	"water/internal/decisions"
)

// handleListDecisions runs the classification-trigger orchestration over
// today's candidate items and returns every card it built, ranked by
// severity then deadline (decisions.Rank). A nil Decisions (no registry
// configured) reports an empty list rather than an error, matching how a
// twin with no decision types simply has nothing to show.
func (d *Daemon) handleListDecisions(w http.ResponseWriter, r *http.Request) {
	if d.cfg.Decisions == nil {
		writeJSON(w, http.StatusOK, []*decisions.Card{})
		return
	}
	cards, err := d.cfg.Decisions.Run(r.Context(), time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, decisions.Rank(cards))
}
