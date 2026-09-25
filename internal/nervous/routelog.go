package nervous

import (
	"context"
	"strings"
	"sync"
	"time"

	"water/internal/nervous/tmpl"
	"water/internal/runtime"
	"water/internal/store"
)

// DefaultMissWindow bounds how long after a Tier-0 answer a rephrase or
// correction on the same channel still counts as a possible miss (Design
// §11.4/§17: 60s).
const DefaultMissWindow = 60 * time.Second

// reflexRingWindow bounds how far back the recent-reflex ring reaches when
// building the main path's "## Recent quick answers" context (Design
// §11.4: "the last 3 quick exchanges from the last 10 minutes").
const reflexRingWindow = 10 * time.Minute

func ms(d time.Duration) int64 { return d.Milliseconds() }

// routeRecorder accumulates one turn's routing facts as Handle progresses,
// then writes exactly one store.RouteRow when finish runs — on the normal
// path, on a handler error, on context cancellation, or (having set
// outcome="error" first) on a recovered panic. Fields that depend on
// mechanisms later tasks build (partials/speculation: R-19; action/decision
// fields: R-20/R-21) are simply left at their zero value here — there is
// nothing yet to populate them with, and fabricating a value would be worse
// than an honest zero. Quick-tool attribution (tools_used/tools_attributed/
// quick_only/tool_signature) is populated for real as of this task (R-22):
// answerMain (mainpath.go) sets toolsUsed/toolsAttributed/quickOnly/
// toolSignature on this recorder before finish ever runs.
type routeRecorder struct {
	n         *Nervous
	turnID    string
	channel   runtime.Channel
	utterance string
	start     time.Time
	tierStart time.Time

	tiersAttempted   []string
	owner            string
	answeredBy       string
	intent           string
	intentKind       string
	intentOrigin     string
	slots            map[string]string
	escalationReason string
	latency          map[string]int64
	outcome          string
	warnings         []string
	ackMS            *int64
	firstSentenceMS  *int64

	// toolsUsed/toolsAttributed/quickOnly/toolSignature are set only for a
	// main-path turn, by answerMain's BeginMain/EndMain bracket (tooltrace.go,
	// R-14/R-16 built the mechanism; this task, R-22, is the first real
	// caller). A quick-tier turn (owner=quick) never touches these, so they
	// stay at their zero value for it — which is correct, since a quick
	// answer never calls a main-agent tool at all.
	toolsUsed       []string
	toolsAttributed bool
	quickOnly       bool
	toolSignature   string
}

func (n *Nervous) newRouteRecorder(id string, ch runtime.Channel, utterance string, at time.Time) *routeRecorder {
	return &routeRecorder{n: n, turnID: id, channel: ch, utterance: utterance, start: at, latency: map[string]int64{}}
}

// beginTier records that tier was attempted and starts its latency clock.
func (r *routeRecorder) beginTier(tier string) {
	r.tiersAttempted = append(r.tiersAttempted, tier)
	r.tierStart = r.n.cfg.Clock.Now()
}

// endTier closes tier's latency clock. Call it once per matching beginTier.
func (r *routeRecorder) endTier(tier string) {
	r.latency[tier] = ms(r.n.cfg.Clock.Now().Sub(r.tierStart))
}

func (r *routeRecorder) recordAck(at time.Time) {
	v := ms(at.Sub(r.start))
	r.ackMS = &v
}

func (r *routeRecorder) recordFirstSentence(at time.Time) {
	if r.firstSentenceMS != nil {
		return
	}
	v := ms(at.Sub(r.start))
	r.firstSentenceMS = &v
}

func (r *routeRecorder) addWarnings(w []string) {
	r.warnings = append(r.warnings, w...)
}

// finish writes exactly one route_log row for this turn, then runs the
// possible-miss check against the previous row on the same channel and
// updates the recent-reflex ring. It is safe to call with a nil
// Config.Store (some unit tests build a Nervous without one): logging is
// simply skipped rather than panicking.
func (r *routeRecorder) finish(ctx context.Context) {
	if r.n.cfg.Store == nil {
		return
	}
	if r.outcome == "" {
		if ctx.Err() != nil {
			r.outcome = "cancelled"
		} else {
			r.outcome = "answered"
		}
	}
	// The turn's own ctx may already be cancelled (that's exactly what
	// "outcome=cancelled" means) or, on a panic, in an unknown state — but
	// the log write itself must still go through, or a cancelled turn would
	// be the one turn that never gets logged. WithoutCancel keeps any
	// deadline/values but drops the cancellation signal for this write.
	logCtx := context.WithoutCancel(ctx)

	now := r.n.cfg.Clock.Now()
	partials, firstPartialLeadMS, speculation := r.n.captureSpeculationFacts(r.turnID, r.start)
	row := store.RouteRow{
		TurnID:             r.turnID,
		At:                 r.start,
		Channel:            string(r.channel),
		Utterance:          r.utterance,
		TiersAttempted:     r.tiersAttempted,
		Owner:              r.owner,
		AnsweredBy:         r.answeredBy,
		Intent:             r.intent,
		IntentKind:         r.intentKind,
		IntentOrigin:       r.intentOrigin,
		Slots:              r.slots,
		EscalationReason:   r.escalationReason,
		LatencyMS:          r.latency,
		TotalMS:            ms(now.Sub(r.start)),
		Outcome:            r.outcome,
		Warnings:           r.warnings,
		Voice:              r.channel == runtime.ChannelVoice,
		Partials:           partials,
		FirstPartialLeadMS: firstPartialLeadMS,
		Speculation:        speculation,
		// SpeculationModelCalls is always 0: SpecDeps (speculate.go) has no
		// backend or Tier 1 field at all, so speculative work can never make
		// a model call by construction — there is nothing to count here,
		// ever (TestSpeculationZeroModelCalls asserts this in practice too).
		SpeculationModelCalls: 0,
		AckMS:                 r.ackMS,
		FirstSentenceMS:       r.firstSentenceMS,
		ToolsUsed:             r.toolsUsed,
		ToolsAttributed:       r.toolsAttributed,
		QuickOnly:             r.quickOnly,
		ToolSignature:         r.toolSignature,
	}

	// The "previous row" must be found BEFORE this one is inserted, or it
	// would just find itself.
	prev, hasPrev := r.n.previousRouteOnChannel(logCtx, r.channel, r.start)

	id, err := r.n.cfg.Store.InsertRoute(logCtx, row)
	if err == nil && hasPrev && r.n.isPossibleMiss(prev, r) {
		_ = r.n.cfg.Store.MarkPossibleMiss(logCtx, prev.ID)
	}
	_ = id

	r.updateRing()
	r.n.pruneRoutesOncePerDay(logCtx, now)
}

// captureSpeculationFacts reads whatever turn.Table still knows about
// partials and cached speculative work for id, at logging time (R-19).
// Partial transcripts and speculation results live only in memory (Design
// §11.5: "partial text is never stored") — only these counts, timings and a
// small speculation summary ever reach route_log. id is looked up after
// Done() has run, so this always sees the turn's final state; turn.Table's
// own DoneKeep (10 minutes) comfortably outlives the deferred call that
// reaches here.
func (n *Nervous) captureSpeculationFacts(id string, at time.Time) (partials int, firstPartialLeadMS *int64, speculation map[string]any) {
	tn, ok := n.turns.Get(id)
	if !ok {
		return 0, nil, nil
	}
	partials = tn.Partials
	if partials > 0 && !tn.FirstPartialAt.IsZero() {
		v := ms(at.Sub(tn.FirstPartialAt))
		firstPartialLeadMS = &v
	}
	if spec, ok := tn.Spec.(*Speculation); ok && spec != nil {
		speculation = map[string]any{
			"summary": spec.summaryOK,
			"dry":     spec.intent,
			"prewarm": spec.prewarm,
			"reused":  spec.reused,
		}
	}
	return partials, firstPartialLeadMS, speculation
}

func (r *routeRecorder) updateRing() {
	if r.answeredBy != "t0" && r.answeredBy != "t1" {
		return
	}
	summary := r.intent
	if summary == "" {
		summary = r.outcome
	}
	r.n.ring.add(reflexExchange{at: r.start, utterance: r.utterance, answerSummary: summary})
}

// previousRouteOnChannel returns the most recent route_log row on ch that
// was written strictly before at, if any exists within DefaultMissWindow
// (or Config.MissWindow, if set).
func (n *Nervous) previousRouteOnChannel(ctx context.Context, ch runtime.Channel, at time.Time) (store.RouteRow, bool) {
	window := n.cfg.MissWindow
	if window <= 0 {
		window = DefaultMissWindow
	}
	rows, err := n.cfg.Store.ListRoutes(ctx, at.Add(-window), 20)
	if err != nil {
		return store.RouteRow{}, false
	}
	for _, row := range rows {
		if row.Channel == string(ch) && row.At.Before(at) {
			return row, true
		}
	}
	return store.RouteRow{}, false
}

// isPossibleMiss decides whether prev (a Tier-0-answered row) should be
// flagged, given the turn that just followed it on the same channel
// (Design §11.4/§17: within the miss window, and either a correction
// phrase or — when the new turn escalated to the main path — at least 0.5
// token-Jaccard similarity to the previous utterance).
func (n *Nervous) isPossibleMiss(prev store.RouteRow, cur *routeRecorder) bool {
	if prev.AnsweredBy != "t0" {
		return false
	}
	window := n.cfg.MissWindow
	if window <= 0 {
		window = DefaultMissWindow
	}
	if cur.start.Before(prev.At) || cur.start.Sub(prev.At) > window {
		return false
	}

	reg := n.cfg.Registry()
	if isCorrectionPhrase(cur.utterance, reg.Shared().Corrections) {
		return true
	}
	if cur.owner == "main" && jaccard(tokenSet(prev.Utterance), tokenSet(cur.utterance)) >= 0.5 {
		return true
	}
	return false
}

func isCorrectionPhrase(utterance string, corrections []string) bool {
	lower := strings.ToLower(utterance)
	for _, c := range corrections {
		if c != "" && strings.Contains(lower, strings.ToLower(c)) {
			return true
		}
	}
	return false
}

func tokenSet(s string) map[string]bool {
	toks := tmpl.Normalize(s, nil).Tokens
	set := make(map[string]bool, len(toks))
	for _, t := range toks {
		set[t] = true
	}
	return set
}

// jaccard is |a∩b| / |a∪b|, 0 when both sets are empty.
func jaccard(a, b map[string]bool) float64 {
	union := map[string]bool{}
	inter := 0
	for k := range a {
		union[k] = true
		if b[k] {
			inter++
		}
	}
	for k := range b {
		union[k] = true
	}
	if len(union) == 0 {
		return 0
	}
	return float64(inter) / float64(len(union))
}

// pruneRoutesOncePerDay deletes route_log rows older than the retention
// window, at most once per local calendar day (Design §11.4 step 9). A
// prune failure is silently skipped — pruning is housekeeping, never a
// reason to fail a turn — and the next turn simply tries again.
func (n *Nervous) pruneRoutesOncePerDay(ctx context.Context, now time.Time) {
	retention := n.cfg.Retention
	if retention <= 0 {
		retention = 90 * 24 * time.Hour
	}
	day := now.Format("2006-01-02")

	n.pruneMu.Lock()
	if n.lastPruneDay == day {
		n.pruneMu.Unlock()
		return
	}
	n.lastPruneDay = day
	n.pruneMu.Unlock()

	_, _ = n.cfg.Store.PruneRoutes(ctx, now.Add(-retention))
}

// reflexExchange is one Tier-0/Tier-1-answered turn, kept in the
// recent-reflex ring as context for the main path only (Design §11.4).
type reflexExchange struct {
	at            time.Time
	utterance     string
	answerSummary string
}

// reflexRing holds the last 3 quick exchanges from the last 10 minutes, for
// one *Nervous instance (not per-turn — every turn shares and contributes
// to the same ring). It never grows past 3 entries regardless of how old
// they are; recency filtering happens at read time (Prompt), not at write
// time, so a quiet period doesn't need any separate eviction pass.
type reflexRing struct {
	mu    sync.Mutex
	items []reflexExchange
}

func newReflexRing() *reflexRing { return &reflexRing{} }

func (r *reflexRing) add(e reflexExchange) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, e)
	if len(r.items) > 3 {
		r.items = r.items[len(r.items)-3:]
	}
}

// prompt formats whichever ring entries are still within window of now as
// a "## Recent quick answers" block, or "" if none qualify (an empty ring,
// or every entry now stale).
func (r *reflexRing) prompt(now time.Time) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var lines []string
	for _, it := range r.items {
		if now.Sub(it.at) > reflexRingWindow {
			continue
		}
		lines = append(lines, "- \""+it.utterance+"\" -> "+it.answerSummary)
	}
	if len(lines) == 0 {
		return ""
	}
	return "## Recent quick answers\n" + strings.Join(lines, "\n")
}
