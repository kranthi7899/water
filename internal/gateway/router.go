package gateway

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"water/internal/nervous"
)

// turnPartialIDPattern is the client-generated turn id's required shape
// (Design §11.5).
var turnPartialIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

const (
	// maxPartialTextLen bounds one partial's text (Design §11.5).
	maxPartialTextLen = 2000
	// maxPartialsPerSecond bounds how many partials one turn id may post in
	// a given wall-clock second (Design §11.5). Over the limit, a partial
	// is silently dropped rather than rejected: a client streaming
	// partials as fast as speech recognition produces them should never
	// have to treat this as an error to handle, and dropping one partial
	// out of a fast burst costs nothing — the next one that gets through
	// still carries the latest (superset) text.
	maxPartialsPerSecond = 20
)

// handleTurnPartial accepts one partial transcript for speculative prefetch
// only (Design §11.5): it never answers, and the text itself is never
// stored anywhere beyond this request's own in-memory speculative work (see
// nervous.Nervous.Partial) — not in route_log, not logged, nothing beyond a
// count and a timestamp reaching that turn's eventual row. It always
// replies once id, text and channel are well-formed; a turn already past
// StateListening (final or later) is reported as a conflict rather than
// silently accepted, since posting a partial for it can no longer do
// anything.
func (d *Daemon) handleTurnPartial(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !turnPartialIDPattern.MatchString(id) {
		http.Error(w, "invalid turn id (want ^[A-Za-z0-9_-]{8,64}$)", http.StatusBadRequest)
		return
	}
	var body struct {
		Text    string `json:"text"`
		Seq     int    `json:"seq"`
		Channel string `json:"channel"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(body.Text) > maxPartialTextLen {
		http.Error(w, "text too long (max 2000 characters)", http.StatusBadRequest)
		return
	}
	ch, ok := parseChannel(body.Channel)
	if !ok {
		http.Error(w, "unknown channel (want cli|voice|text-bar)", http.StatusBadRequest)
		return
	}

	if d.partialLimiter.allow(id, time.Now()) && d.cfg.Nervous != nil {
		if _, err := d.cfg.Nervous.Partial(r.Context(), id, ch, body.Text, body.Seq); err != nil {
			http.Error(w, "turn is no longer listening", http.StatusConflict)
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"state": "listening"})
}

// partialLimiter is a tiny per-turn-id fixed-window rate limiter (Design
// §11.5's "at most 20 partials/second per turn"): one counter per id,
// reset whenever the wall-clock second changes. It is deliberately not a
// precise sliding window — a coarse per-second cap is enough to bound how
// often speculate() can run for one turn, which is all this guards against.
type partialLimiter struct {
	mu  sync.Mutex
	win map[string]partialWindow
}

type partialWindow struct {
	second int64
	count  int
}

func newPartialLimiter() *partialLimiter {
	return &partialLimiter{win: make(map[string]partialWindow)}
}

// allow reports whether id may post one more partial this second, and
// records that it did. It also opportunistically forgets windows more than
// a few seconds stale, so the map doesn't grow without bound across the
// daemon's lifetime as ever-new turn ids come and go.
func (p *partialLimiter) allow(id string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	sec := now.Unix()
	if len(p.win) > 64 {
		for k, w := range p.win {
			if sec-w.second > 5 {
				delete(p.win, k)
			}
		}
	}
	st := p.win[id]
	if st.second != sec {
		st = partialWindow{second: sec}
	}
	st.count++
	p.win[id] = st
	return st.count <= maxPartialsPerSecond
}

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
