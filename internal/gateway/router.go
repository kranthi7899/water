package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"water/internal/gate"
	"water/internal/nervous"
	"water/internal/nervous/eval"
	"water/internal/nervous/promote"
	"water/internal/twins"
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

// handleRouteCandidates lists repeated main-agent quick-tool-usage patterns
// that could become a learned Tier 0 intent (Design §16, `promote.Candidates`
// — detection only, R-22). This is deliberately never gated on
// router.promotion.enabled: listing candidates is read-only over route_log
// and always available, unlike drafting or promoting one (R-23).
// ?since=<Go duration, e.g. "720h"> bounds the lookback (default 30 days);
// ?min=<n> sets the minimum distinct-turn repeat count (default
// promote.DefaultMinRepeats).
func (d *Daemon) handleRouteCandidates(w http.ResponseWriter, r *http.Request) {
	lookback := 30 * 24 * time.Hour
	if s := r.URL.Query().Get("since"); s != "" {
		if dur, err := time.ParseDuration(s); err == nil && dur > 0 {
			lookback = dur
		}
	}
	minRepeats := promote.DefaultMinRepeats
	if s := r.URL.Query().Get("min"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			minRepeats = n
		}
	}
	if d.cfg.Store == nil || d.cfg.Nervous == nil {
		writeJSON(w, http.StatusOK, []promote.Candidate{})
		return
	}
	sh := d.cfg.Nervous.Registry().Shared()
	cands, err := promote.Candidates(r.Context(), d.cfg.Store, sh, time.Now().Add(-lookback), minRepeats)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, cands)
}

// intentsDraftRequest is POST /v1/intents/draft's body: the candidate id
// GET /v1/route/candidates (or `water route candidates`) reported.
type intentsDraftRequest struct {
	CandidateID string `json:"candidate_id"`
}

// intentsDraftResponse is what the draft step hands back for the owner to
// review before ever promoting anything (Design §16 item 2): the YAML this
// call wrote to the twin's pending/ directory, and whether it currently
// passes ValidateLearned.
type intentsDraftResponse struct {
	ID              string `json:"id"`
	YAML            string `json:"yaml"`
	Path            string `json:"path"`
	Valid           bool   `json:"valid"`
	ValidationError string `json:"validation_error,omitempty"`
}

// handleIntentsDraft makes one cold, P0 model call proposing a learned
// intent file for a repeated quick-tool phrasing pattern (Design §16 item
// 2), writes it to the twin's pending/ directory for review, and reports
// whether it currently passes ValidateLearned. It never touches the live
// registry — that only happens on POST /v1/intents/reload, after `water
// intent promote`. Requires router.promotion.enabled: drafting is gated
// exactly like promotion itself ("Draft (flag on)").
func (d *Daemon) handleIntentsDraft(w http.ResponseWriter, r *http.Request) {
	if !d.cfg.PromotionEnabled {
		http.Error(w, "the promotion loop is disabled (router.promotion.enabled=false)", http.StatusForbidden)
		return
	}
	if d.cfg.Store == nil || d.cfg.Nervous == nil || d.cfg.Backend == nil || d.cfg.Manifest == nil {
		http.Error(w, "promotion is not available on this daemon", http.StatusServiceUnavailable)
		return
	}
	var body intentsDraftRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.CandidateID) == "" {
		http.Error(w, "bad request: candidate_id is required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	sh := d.cfg.Nervous.Registry().Shared()
	cands, err := promote.Candidates(ctx, d.cfg.Store, sh, time.Now().Add(-30*24*time.Hour), promote.DefaultMinRepeats)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var cand *promote.Candidate
	for i := range cands {
		if cands[i].ID == body.CandidateID {
			cand = &cands[i]
			break
		}
	}
	if cand == nil {
		http.Error(w, "candidate not found (it may no longer qualify at the default --since 30d/--min 5)", http.StatusNotFound)
		return
	}
	tools := strings.Split(cand.Signature, "+")
	if len(tools) != 1 {
		http.Error(w, "candidate names more than one tool; only a single-tool candidate can become one learned intent", http.StatusBadRequest)
		return
	}
	spec, ok := promote.SpecForQuickTool(tools[0])
	if !ok {
		http.Error(w, "candidate's tool is not a recognized reflex handler", http.StatusInternalServerError)
		return
	}

	samples, err := d.sampleUtterances(ctx, cand.SampleTurnIDs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if d.cfg.Gate != nil {
		if err := d.cfg.Gate.ModelCall(gate.P0); err != nil {
			http.Error(w, err.Error(), http.StatusTooManyRequests)
			return
		}
	}
	model := d.cfg.Manifest.ModelFor(twins.TierFast)
	yamlBytes, err := promote.Draft(ctx, d.cfg.Backend, model, spec, *cand, samples)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	path, err := promote.WritePending(d.cfg.Home, d.cfg.Manifest.ID, yamlBytes, cand.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := intentsDraftResponse{ID: promote.ExtractID(yamlBytes), YAML: string(yamlBytes), Path: path}
	if err := promote.ValidateLearned(yamlBytes, d.cfg.Nervous.Registry(), eval.Negatives(), d.cfg.MaxLearned); err != nil {
		resp.ValidationError = err.Error()
	} else {
		resp.Valid = true
	}
	writeJSON(w, http.StatusOK, resp)
}

// sampleUtterances resolves a candidate's sample turn ids back to their
// original utterances (route_log.utterance), for Draft's prompt.
func (d *Daemon) sampleUtterances(ctx context.Context, turnIDs []string) ([]string, error) {
	if len(turnIDs) == 0 {
		return nil, nil
	}
	rows, err := d.cfg.Store.RoutesByTurnIDs(ctx, turnIDs)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Utterance != "" {
			out = append(out, r.Utterance)
		}
	}
	return out, nil
}

// handleIntentsReload rebuilds the twin's intents registry from its current
// on-disk state and atomically swaps it into Nervous (Design §5.4's last
// paragraph, R-23): the embedded intent files, the learned overlay
// directory (only when router.promotion.enabled), and the store's
// intent_state table. `water intent promote`/`demote`/`enable` (R-26) all
// call this after writing their own change; it is also safe to call with
// no prior write (a same-state no-op reload).
func (d *Daemon) handleIntentsReload(w http.ResponseWriter, r *http.Request) {
	if d.cfg.ReloadIntents == nil {
		http.Error(w, "intents reload is not available on this daemon", http.StatusServiceUnavailable)
		return
	}
	if err := d.cfg.ReloadIntents(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := map[string]any{"reloaded": true}
	if d.cfg.Nervous != nil {
		reg := d.cfg.Nervous.Registry()
		out["hash"] = reg.Hash()
		out["candidates"] = len(reg.Candidates())
		var skipped []map[string]string
		for _, s := range reg.LearnedSkipped() {
			skipped = append(skipped, map[string]string{"file": s.File, "reason": s.Reason})
		}
		out["learned_skipped"] = skipped
	}
	writeJSON(w, http.StatusOK, out)
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
