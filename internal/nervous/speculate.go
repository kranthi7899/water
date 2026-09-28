package nervous

import (
	"context"
	"time"

	"water/internal/nervous/render"
	"water/internal/nervous/tmpl"
	"water/internal/store"
)

// specDebounce bounds how often speculative work actually runs for one
// turn's stream of partials (Design §11.5: "debounced to once per ~150ms
// per turn"). A partial whose normalized tokens exactly match the previous
// speculation run is always skipped, regardless of timing — nothing changed
// to speculate differently about. One whose tokens did change but arrived
// within this window of the last run is still skipped, so a burst of fast,
// distinct partials (voice mid-word revisions) can't run the dry match on
// every single one of them.
const specDebounce = 150 * time.Millisecond

// specReuseWindow bounds how long a cached speculative read answer may be
// reused at final-transcript time (Design §11.5: "reused only if it was
// computed at most 2s before FinalAt").
const specReuseWindow = 2 * time.Second

// Speculation is the in-memory result of the most recent qualifying
// partial's speculative work for one turn. internal/nervous/turn.Turn.Spec
// carries a *Speculation as an opaque `any` — that package's own doc
// comment says it is "owned by speculate.go" and never read or written by
// turn itself.
//
// It is never persisted, and it never carries the partial text itself —
// only normalized tokens (kept only to detect whether a later partial
// actually changed anything worth re-speculating about) and, on a Tier-0
// read-intent hit, that intent's id, resolved slot labels and rendered
// result. It is discarded once the turn's route_log row is written
// (Design's 30s in-memory retention bound is satisfied for free: turn.Table
// itself prunes a terminal turn, Spec included, after its own DoneKeep,
// which is well under a minute).
type Speculation struct {
	tokens     []string
	computedAt time.Time

	summaryOK bool

	intent string
	labels map[string]string
	result render.Result
	hit    bool // true only when DryMatch hit AND RunRead itself succeeded

	// prewarm is "" if never attempted (a dry-match hit made it unnecessary,
	// or SpecDeps.Prewarm was nil), else one of "alive", "started", "busy"
	// or "skipped".
	prewarm string
	// reused is set by Handle's trySpeculationReuse when this Speculation's
	// cached result was actually reused for the final answer, purely so the
	// route_log row can report it truthfully.
	reused bool
}

// SpecDeps is speculate's entire readable, runnable world. It deliberately
// has no backend field and no emitter — there is no way for speculate (or
// anything it calls through these fields) to
// make a model call or answer a turn, by the shape of this struct alone,
// not by a runtime check (Design §1's "one action path enforced by
// construction" applied here to speculation itself: TestSpeculationZeroModelCalls
// proves it in practice, but the guarantee is structural first).
type SpecDeps struct {
	// ReadStore is carried for a caller that wants to build Summary/DryMatch
	// /RunRead directly from it; speculate itself never touches this field —
	// each func field below already closes over whatever store access it
	// needs.
	ReadStore *store.Store
	// Summary precomputes the state summary the main path would eventually
	// build (runtime.StateSummary's own shape) — store reads only.
	Summary func(ctx context.Context) (summary string, tainted bool, err error)
	// DryMatch runs Tier 0's own deterministic template match (matchOnly,
	// tier0.go — never a reimplementation of the match loop) against every
	// live read intent, without ever running a handler.
	DryMatch func(u tmpl.Utterance) (intent string, labels map[string]string, ok bool)
	// RunRead re-resolves a dry match's labels through the same shared
	// Validate path every quick tier's arguments go through, then runs
	// that intent's real reflex handler: store reads only, still no model
	// access.
	RunRead func(ctx context.Context, intent string, labels map[string]string) (render.Result, error)
	// Prewarm tries to warm the backend's subprocess ahead of a real turn,
	// without ever sending it a turn (WarmSession.Prewarm, wrapped by
	// whatever req the daemon's own wiring supplies).
	Prewarm func(ctx context.Context) (state string, err error)
}

// speculate runs one qualifying partial's speculative work against deps and
// returns the resulting Speculation. It is pure with respect to anything
// outside deps: no emitter, no turn-table access, no clock of its own — the
// caller (Nervous.Partial) stamps computedAt using its own configured
// Clock, so tests can control it precisely.
func speculate(ctx context.Context, deps SpecDeps, u tmpl.Utterance) *Speculation {
	spec := &Speculation{tokens: append([]string(nil), u.Tokens...)}

	if deps.Summary != nil {
		_, _, err := deps.Summary(ctx)
		spec.summaryOK = err == nil
	}

	if deps.DryMatch != nil {
		if intent, labels, ok := deps.DryMatch(u); ok {
			spec.intent, spec.labels = intent, labels
			if deps.RunRead != nil {
				if res, err := deps.RunRead(ctx, intent, labels); err == nil {
					spec.result, spec.hit = res, true
				}
			}
		}
	}

	switch {
	case spec.hit:
		// A read answer is already cached; warming the backend buys
		// nothing here (Tier 0 would answer this turn without ever
		// reaching the main path), so Prewarm is deliberately skipped
		// rather than called.
		spec.prewarm = "skipped"
	case deps.Prewarm != nil:
		state, _ := deps.Prewarm(ctx)
		spec.prewarm = state
	default:
		spec.prewarm = "skipped"
	}

	return spec
}

// shouldSpeculate decides whether a qualifying partial should actually run
// speculate() (Design §11.5's debounce): true when there is no previous
// speculation for this turn yet, when its tokens differ from prev's AND at
// least specDebounce has passed since prev was computed. Identical tokens
// never re-run (nothing would come out differently); different tokens still
// wait out the debounce window, so a fast burst of distinct partials can't
// run the dry match on every single one.
func shouldSpeculate(prev *Speculation, tokens []string, now time.Time) bool {
	if prev == nil {
		return true
	}
	if sameTokens(prev.tokens, tokens) {
		return false
	}
	return now.Sub(prev.computedAt) >= specDebounce
}

// sameTokens reports whether a and b are the identical token sequence in
// the identical order — a template match is anchored, so a reorder is a
// different utterance for matching purposes even when the token sets are
// equal.
func sameTokens(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sameLabels reports whether two slot-label maps are identical (Design
// §11.5: "the final match has the same intent and labels" — an exact map
// comparison, not merely equal length).
func sameLabels(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}
