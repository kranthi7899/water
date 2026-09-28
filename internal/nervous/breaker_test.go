package nervous

import (
	"testing"
	"time"

	"water/internal/store"
)

func testBreakerConfig() BreakerConfig {
	return BreakerConfig{
		Failures:       5,
		Cooldown:       60 * time.Second,
		MaxMissRatePct: 20,
		MissSample:     50,
		MinMissSamples: 20,
	}
}

func TestBreakerOpensAfterConsecutiveFailures(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	b := NewBreaker(testBreakerConfig(), clock)

	for i := 0; i < 4; i++ {
		if !b.Allow() {
			t.Fatalf("iteration %d: expected Allow() true before the breaker opens", i)
		}
		b.RecordFailure("boom")
	}
	if st, _, _ := b.State(); st != BreakerClosed {
		t.Fatalf("state after 4 failures = %s, want closed", st)
	}

	if !b.Allow() {
		t.Fatal("expected Allow() true on the 5th attempt")
	}
	b.RecordFailure("boom")

	st, reason, _ := b.State()
	if st != BreakerOpen {
		t.Fatalf("state after 5 consecutive failures = %s, want open", st)
	}
	if reason != "boom" {
		t.Fatalf("reason = %q, want %q", reason, "boom")
	}
	if b.Allow() {
		t.Fatal("Allow() should be false while open and before cooldown")
	}
}

func TestBreakerCooldownThenHalfOpenTrial(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := testBreakerConfig()
	b := NewBreaker(cfg, clock)

	for i := 0; i < cfg.Failures; i++ {
		b.Allow()
		b.RecordFailure("boom")
	}
	if st, _, _ := b.State(); st != BreakerOpen {
		t.Fatalf("state = %s, want open", st)
	}

	clock.Advance(cfg.Cooldown - time.Second)
	if b.Allow() {
		t.Fatal("Allow() should still be false 1s before cooldown elapses")
	}

	clock.Advance(2 * time.Second)
	if !b.Allow() {
		t.Fatal("Allow() should be true once the cooldown has elapsed")
	}
	if st, _, _ := b.State(); st != BreakerHalfOpen {
		t.Fatalf("state after cooldown = %s, want half_open", st)
	}

	// A second, concurrent-in-spirit caller must not also get the trial.
	if b.Allow() {
		t.Fatal("a second Allow() while the half-open trial is in flight must be false")
	}
}

func TestBreakerHalfOpenTrialSuccessCloses(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := testBreakerConfig()
	b := NewBreaker(cfg, clock)
	for i := 0; i < cfg.Failures; i++ {
		b.Allow()
		b.RecordFailure("boom")
	}
	clock.Advance(cfg.Cooldown)
	if !b.Allow() {
		t.Fatal("expected the half-open trial to be allowed")
	}
	b.RecordSuccess()

	st, reason, _ := b.State()
	if st != BreakerClosed {
		t.Fatalf("state after a successful trial = %s, want closed", st)
	}
	if reason != "" {
		t.Fatalf("reason after closing = %q, want empty", reason)
	}
	if !b.Allow() {
		t.Fatal("breaker should allow again once closed")
	}
}

func TestBreakerHalfOpenTrialFailureReopens(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := testBreakerConfig()
	b := NewBreaker(cfg, clock)
	for i := 0; i < cfg.Failures; i++ {
		b.Allow()
		b.RecordFailure("boom")
	}
	clock.Advance(cfg.Cooldown)
	if !b.Allow() {
		t.Fatal("expected the half-open trial to be allowed")
	}
	b.RecordFailure("still broken")

	st, reason, since := b.State()
	if st != BreakerOpen {
		t.Fatalf("state after a failed trial = %s, want open", st)
	}
	if reason != "still broken" {
		t.Fatalf("reason = %q, want %q", reason, "still broken")
	}
	if !since.Equal(clock.Now()) {
		t.Fatalf("since = %v, want the reopen time %v", since, clock.Now())
	}
	if b.Allow() {
		t.Fatal("Allow() should be false immediately after reopening")
	}
}

func TestBreakerMissRateRequiresMinimumSamples(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := testBreakerConfig() // MinMissSamples: 20, MaxMissRatePct: 20
	b := NewBreaker(cfg, clock)

	// 90% miss rate, but only 10 samples: must not trip.
	b.RecordMissRate(9, 10)
	if st, _, _ := b.State(); st != BreakerClosed {
		t.Fatalf("state with only 10 samples = %s, want closed (below MinMissSamples)", st)
	}

	// Same 90% rate at 20 samples: must trip.
	b.RecordMissRate(18, 20)
	st, reason, _ := b.State()
	if st != BreakerOpen {
		t.Fatalf("state with 20 samples at 90%% miss rate = %s, want open", st)
	}
	if reason == "" {
		t.Fatal("expected a non-empty reason")
	}
}

func TestBreakerMissRateBelowThresholdNeverTrips(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	b := NewBreaker(testBreakerConfig(), clock)
	b.RecordMissRate(5, 50) // 10%, below the 20% threshold
	if st, _, _ := b.State(); st != BreakerClosed {
		t.Fatalf("state = %s, want closed", st)
	}
}

func TestBreakerMissRateDoesNotFightAnAlreadyOpenBreaker(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	cfg := testBreakerConfig()
	b := NewBreaker(cfg, clock)
	for i := 0; i < cfg.Failures; i++ {
		b.Allow()
		b.RecordFailure("boom")
	}
	before, _, since := b.State()
	if before != BreakerOpen {
		t.Fatalf("setup: state = %s, want open", before)
	}
	b.RecordMissRate(50, 50) // would trip a closed breaker; must be a no-op here
	after, _, sinceAfter := b.State()
	if after != BreakerOpen || !sinceAfter.Equal(since) {
		t.Fatalf("RecordMissRate must not disturb an already-open breaker: state=%s since=%v (was %v)", after, sinceAfter, since)
	}
}

func TestLearnedIntentShouldDemote(t *testing.T) {
	rows := func(n, miss int) []store.RouteRow {
		out := make([]store.RouteRow, n)
		for i := 0; i < miss; i++ {
			out[i] = store.RouteRow{PossibleMiss: true}
		}
		return out
	}

	if demote, _ := LearnedIntentShouldDemote(rows(9, 8), 10, 20); demote {
		t.Fatal("9 samples must never demote regardless of miss rate (below minSamples=10)")
	}
	if demote, reason := LearnedIntentShouldDemote(rows(10, 3), 10, 20); !demote {
		t.Fatalf("30%% miss rate over 10 samples must demote (threshold 20%%); reason=%q", reason)
	}
	if demote, _ := LearnedIntentShouldDemote(rows(10, 2), 10, 20); demote {
		t.Fatal("20%% miss rate exactly at threshold must NOT demote (rule is strictly greater than)")
	}
	if demote, reason := LearnedIntentShouldDemote(rows(20, 5), 10, 20); !demote {
		t.Fatalf("25%% miss rate over 20 samples must demote; reason=%q", reason)
	}
}
