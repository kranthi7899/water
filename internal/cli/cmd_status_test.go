package cli

import (
	"strings"
	"testing"
	"time"
)

// TestRouterStatusLineHappyPath: a fast, successful fetch renders the
// health summary well within the timeout budget.
func TestRouterStatusLineHappyPath(t *testing.T) {
	old := routerStatusTimeout
	routerStatusTimeout = 50 * time.Millisecond
	defer func() { routerStatusTimeout = old }()

	h := RouterHealth{PromotionEnabled: true}
	h.Tier0.State = "closed"
	h.InactiveIntents = []struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	}{{ID: "control.stop", Reason: "not granted"}}

	got := routerStatusLine(func() (RouterHealth, error) { return h, nil })
	for _, want := range []string{"tier0=closed", "inactive=1", "learned_skipped=0", "promotion=on"} {
		if !strings.Contains(got, want) {
			t.Errorf("routerStatusLine = %q, missing %q", got, want)
		}
	}
}

// TestRouterStatusLineNoDaemonFallsBackImmediately: a fetch that fails fast
// (the ordinary "daemon not running" case) reports the fallback without
// waiting out the timeout budget.
func TestRouterStatusLineNoDaemonFallsBackImmediately(t *testing.T) {
	old := routerStatusTimeout
	routerStatusTimeout = 2 * time.Second // would make the test slow if this path blocked on it
	defer func() { routerStatusTimeout = old }()

	start := time.Now()
	got := routerStatusLine(func() (RouterHealth, error) { return RouterHealth{}, errDaemonNotRunning })
	elapsed := time.Since(start)

	if got != "(daemon not running)" {
		t.Fatalf("routerStatusLine = %q, want the fallback", got)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("a fast-failing fetch took %v to report the fallback, want near-instant (budget was %v)", elapsed, routerStatusTimeout)
	}
}

// TestRouterStatusLineTimesOut: a fetch that never returns in time (a
// daemon that accepted the connection but never answers) is bounded by
// routerStatusTimeout, not left to hang.
func TestRouterStatusLineTimesOut(t *testing.T) {
	old := routerStatusTimeout
	routerStatusTimeout = 30 * time.Millisecond
	defer func() { routerStatusTimeout = old }()

	block := make(chan struct{})
	defer close(block) // let the leaked goroutine finish after the test

	start := time.Now()
	got := routerStatusLine(func() (RouterHealth, error) {
		<-block
		return RouterHealth{}, nil
	})
	elapsed := time.Since(start)

	if got != "(daemon not running)" {
		t.Fatalf("routerStatusLine = %q, want the fallback", got)
	}
	if elapsed < routerStatusTimeout {
		t.Fatalf("returned in %v, before the %v timeout even elapsed", elapsed, routerStatusTimeout)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("took %v to time out a %v budget, want it tightly bounded", elapsed, routerStatusTimeout)
	}
}

func TestRenderRouterStatusLine(t *testing.T) {
	var h RouterHealth
	h.Tier0.State = "open"
	h.Tier0.Reason = "3 consecutive errors"
	h.PromotionEnabled = false
	got := renderRouterStatusLine(h)
	for _, want := range []string{"tier0=open", "inactive=0", "learned_skipped=0", "promotion=off"} {
		if !strings.Contains(got, want) {
			t.Errorf("renderRouterStatusLine = %q, missing %q", got, want)
		}
	}
}
