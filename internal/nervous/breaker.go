package nervous

import (
	"fmt"
	"sync"
	"time"

	"water/internal/store"
)

// BreakerState is where a circuit breaker currently sits.
type BreakerState string

const (
	BreakerClosed   BreakerState = "closed"
	BreakerOpen     BreakerState = "open"
	BreakerHalfOpen BreakerState = "half_open"
)

// BreakerConfig bounds one tier's circuit breaker (Design §11.4/§17).
type BreakerConfig struct {
	// Failures is how many consecutive errors or timeouts open the breaker.
	Failures int
	// Cooldown is how long the breaker stays open before allowing one
	// half-open trial.
	Cooldown time.Duration
	// MaxMissRatePct and MissSample bound Tier 0's possible-miss trip: the
	// breaker also opens when the possible-miss rate over the last
	// MissSample answered turns reaches MaxMissRatePct, but only once at
	// least MinMissSamples exist (a handful of misses in a tiny sample must
	// never trip this).
	MaxMissRatePct int
	MissSample     int
	MinMissSamples int
}

// DefaultBreakerConfig matches Design §17's documented defaults.
func DefaultBreakerConfig() BreakerConfig {
	return BreakerConfig{
		Failures:       5,
		Cooldown:       60 * time.Second,
		MaxMissRatePct: 20,
		MissSample:     50,
		MinMissSamples: 20,
	}
}

// Breaker is one tier's circuit breaker: closed (tries the tier), open
// (skips it after a run of trouble), half_open (allows exactly one trial
// once the cooldown has elapsed). It is safe for concurrent use.
type Breaker struct {
	mu    sync.Mutex
	cfg   BreakerConfig
	clock Clock

	state          BreakerState
	reason         string
	since          time.Time
	consecFailures int
	trialInFlight  bool
}

// NewBreaker builds a Breaker, closed, using clock for cooldown timing (a
// fake clock in tests).
func NewBreaker(cfg BreakerConfig, clock Clock) *Breaker {
	if clock == nil {
		clock = realClock{}
	}
	return &Breaker{cfg: cfg, clock: clock, state: BreakerClosed}
}

// Allow reports whether the tier may be tried right now. Open before its
// cooldown elapses: no. Open past cooldown: yes, exactly once (the trial),
// transitioning to half_open — a second concurrent caller sees the trial
// already in flight and is refused until it resolves. Closed or an
// already-in-flight half-open trial (this same caller, effectively serial
// per tier in practice): yes.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case BreakerClosed:
		return true
	case BreakerOpen:
		if b.clock.Now().Sub(b.since) < b.cfg.Cooldown {
			return false
		}
		b.state = BreakerHalfOpen
		b.trialInFlight = true
		return true
	case BreakerHalfOpen:
		if b.trialInFlight {
			return false
		}
		b.trialInFlight = true
		return true
	}
	return false
}

// RecordSuccess resets the consecutive-failure count and, if this was the
// half-open trial, closes the breaker.
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.consecFailures = 0
	if b.state == BreakerHalfOpen {
		b.state = BreakerClosed
		b.reason = ""
		b.trialInFlight = false
	}
}

// RecordFailure counts a failure or timeout. A half-open trial's failure
// reopens immediately; otherwise the breaker opens once cfg.Failures
// consecutive failures accumulate.
func (b *Breaker) RecordFailure(reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state == BreakerHalfOpen {
		b.openLocked(reason)
		return
	}
	b.consecFailures++
	if b.consecFailures >= b.cfg.Failures {
		b.openLocked(reason)
	}
}

// RecordMissRate feeds Tier 0's possible-miss statistics: missCount possible
// misses out of sampleCount recent answered turns. It only ever trips a
// currently-closed breaker (an open or half-open breaker is already being
// handled by the ordinary failure path) and only once at least
// MinMissSamples samples exist.
func (b *Breaker) RecordMissRate(missCount, sampleCount int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state != BreakerClosed {
		return
	}
	if sampleCount < b.cfg.MinMissSamples || sampleCount == 0 {
		return
	}
	pct := missCount * 100 / sampleCount
	if pct >= b.cfg.MaxMissRatePct {
		b.openLocked(fmt.Sprintf("possible_miss_rate:%d%% over %d samples", pct, sampleCount))
	}
}

func (b *Breaker) openLocked(reason string) {
	b.state = BreakerOpen
	b.reason = reason
	b.since = b.clock.Now()
	b.consecFailures = 0
	b.trialInFlight = false
}

// State returns the breaker's current state, its reason (empty when
// closed), and when it entered that state.
func (b *Breaker) State() (BreakerState, string, time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state, b.reason, b.since
}

// LearnedIntentShouldDemote decides, from one learned intent's own recent
// answered route_log rows (store.IntentAnswered, newest first), whether its
// possible-miss rate has crossed the auto-demotion threshold (Design §16:
// disables at a possible-miss rate above maxMissRatePct once at least
// minSamples answered rows exist — below minSamples, never demotes no
// matter how bad the visible rate looks). This is a pure function, not a
// stateful Breaker: nothing calls it yet (the promotion loop that owns
// learned intents and SetIntentState is built in a later task), but its
// logic and thresholds are exercised directly by this task's tests so that
// later task only has to wire a caller, not design the rule.
func LearnedIntentShouldDemote(rows []store.RouteRow, minSamples, maxMissRatePct int) (bool, string) {
	if len(rows) < minSamples {
		return false, ""
	}
	miss := 0
	for _, r := range rows {
		if r.PossibleMiss {
			miss++
		}
	}
	pct := miss * 100 / len(rows)
	if pct > maxMissRatePct {
		return true, fmt.Sprintf("auto: miss %d%% over %d", pct, len(rows))
	}
	return false, ""
}
