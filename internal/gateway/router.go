package gateway

import (
	"net/http"
	"strconv"
	"time"

	"water/internal/nervous"
)

// handleRouterHealth reports Slice R's nervous-system state: the Tier 0
// circuit breaker and any intent the registry knows about but currently
// cannot answer from (a planned write action not yet granted in the
// manifest, or a learned-overlay file that failed validation).
func (d *Daemon) handleRouterHealth(w http.ResponseWriter, r *http.Request) {
	if d.cfg.Nervous == nil {
		writeJSON(w, http.StatusOK, map[string]any{"tier0": map[string]any{"state": "unavailable"}})
		return
	}
	state, reason, since := d.cfg.Nervous.Tier0Breaker()
	out := map[string]any{
		"tier0": map[string]any{
			"state":  string(state),
			"reason": reason,
			"since":  since,
		},
	}
	if reg := d.cfg.Nervous.Registry(); reg != nil {
		var inactive []map[string]string
		for _, it := range reg.Shadow() {
			if it.InactiveReason != "" {
				inactive = append(inactive, map[string]string{"id": it.ID, "reason": it.InactiveReason})
			}
		}
		out["inactive_intents"] = inactive
		var skipped []map[string]string
		for _, s := range reg.LearnedSkipped() {
			skipped = append(skipped, map[string]string{"file": s.File, "reason": s.Reason})
		}
		out["learned_skipped"] = skipped
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRouteReport summarizes recent route_log rows (tier distribution,
// escalation reasons, latency percentiles, possible misses) — the same
// computation `water route report` prints (a later task's CLI command).
// ?since=<Go duration, e.g. "168h"> bounds the lookback; it defaults to 7
// days.
func (d *Daemon) handleRouteReport(w http.ResponseWriter, r *http.Request) {
	lookback := 7 * 24 * time.Hour
	if s := r.URL.Query().Get("since"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			lookback = d
		}
	}
	limit := 10000
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			limit = n
		}
	}
	if d.cfg.Store == nil {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	rows, err := d.cfg.Store.ListRoutes(r.Context(), time.Now().Add(-lookback), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, nervous.BuildReport(rows))
}

// handleVoiceProfile serves the twin's voice/TTS settings (name, tone,
// handoff phrases, TTS voice+rate) from style.yaml, so every client's voice
// output matches regardless of which tier answered (Design's one-voice
// contract). A twin with no style.yaml still answers, from
// render.DefaultStyle().
func (d *Daemon) handleVoiceProfile(w http.ResponseWriter, r *http.Request) {
	if d.cfg.Nervous == nil {
		http.Error(w, "no style configured", http.StatusNotFound)
		return
	}
	v := d.cfg.Nervous.VoiceProfile()
	writeJSON(w, http.StatusOK, map[string]any{
		"name":    v.Name,
		"handoff": v.Handoff,
		"tts": map[string]any{
			"voice":    v.TTS.Voice,
			"rate_wpm": v.TTS.RateWPM,
		},
	})
}
