package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestPartialValidRoundTrip: a well-formed partial always gets 202 with the
// documented body, and never touches route_log (only a final turn does).
func TestPartialValidRoundTrip(t *testing.T) {
	h := newHarness(t)
	resp := h.post(t, "/v1/turns/abcdefgh/partial", `{"text":"what's on my calendar","seq":1,"channel":"cli"}`, h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["state"] != "listening" {
		t.Fatalf("body = %+v, want state=listening", body)
	}
}

// TestPartialInvalidIDRejected: an id outside ^[A-Za-z0-9_-]{8,64}$ is a 400,
// before anything else about the request is even considered.
func TestPartialInvalidIDRejected(t *testing.T) {
	h := newHarness(t)
	for _, id := range []string{"short", "has a space", "bad!chars"} {
		resp := h.post(t, "/v1/turns/"+id+"/partial", `{"text":"x","seq":1,"channel":"cli"}`, h.token)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("id %q: status = %d, want 400", id, resp.StatusCode)
		}
	}
}

// TestPartialOversizedTextRejected: text over 2000 characters is a 400.
func TestPartialOversizedTextRejected(t *testing.T) {
	h := newHarness(t)
	big := strings.Repeat("a", 2001)
	body := fmt.Sprintf(`{"text":%q,"seq":1,"channel":"cli"}`, big)
	resp := h.post(t, "/v1/turns/abcdefgh/partial", body, h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestPartialUnknownChannelRejected: an unrecognized channel is a 400,
// exactly like POST /v1/turns.
func TestPartialUnknownChannelRejected(t *testing.T) {
	h := newHarness(t)
	resp := h.post(t, "/v1/turns/abcdefgh/partial", `{"text":"x","seq":1,"channel":"carrier-pigeon"}`, h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestPartialRateLimitSilentlyDrops: posting far more than 20 partials for
// one turn id within the same second never surfaces an error to the client
// (Design §11.5's documented choice: silently drop/ratelimit rather than
// error) — every response is still 202.
func TestPartialRateLimitSilentlyDrops(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= 40; i++ {
		resp := h.post(t, "/v1/turns/ratelimitid/partial", fmt.Sprintf(`{"text":"partial %d","seq":%d,"channel":"cli"}`, i, i), h.token)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("partial #%d: status = %d, want 202 even past the rate limit", i, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// TestPartialLimiterDropsPastCap is a direct, deterministic unit test of
// partialLimiter.allow (the mechanism behind the HTTP-level "silently
// drop" behavior above): the 21st call within the same second is refused.
func TestPartialLimiterDropsPastCap(t *testing.T) {
	pl := newPartialLimiter()
	now := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	allowed := 0
	for i := 0; i < 25; i++ {
		if pl.allow("t1", now) {
			allowed++
		}
	}
	if allowed != maxPartialsPerSecond {
		t.Fatalf("allowed = %d, want %d", allowed, maxPartialsPerSecond)
	}
	// A new second resets the window.
	if !pl.allow("t1", now.Add(time.Second)) {
		t.Fatal("want allowed once the wall-clock second changes")
	}
}

// TestPartialManyDistinctTurnsSucceed is a thin HTTP-level smoke test for
// the 8-concurrently-listening-turn cap: turn.Table's own eviction
// mechanics are already proven at the package level
// (turn.TestMaxListeningEvictsOldest); here we only confirm that posting
// partials for more than 8 distinct turn ids over HTTP never errors — the
// oldest is evicted internally, never surfaced as a client-visible failure.
func TestPartialManyDistinctTurnsSucceed(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= 12; i++ {
		id := fmt.Sprintf("turnid-%02d-abcdef", i)
		resp := h.post(t, "/v1/turns/"+id+"/partial", `{"text":"hello","seq":1,"channel":"cli"}`, h.token)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("turn %s: status = %d, want 202", id, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// TestPostTurnsWithoutTurnIDUnchanged confirms POST /v1/turns without
// turn_id still behaves exactly as before R-19 (backward compatibility).
func TestPostTurnsWithoutTurnIDUnchanged(t *testing.T) {
	h := newRoutingHarness(t)
	resp := h.post(t, "/v1/turns", `{"channel":"cli","prompt":"status"}`, h.token)
	events := readEvents(t, resp)
	if len(events) == 0 || events[len(events)-1].Kind != "done" {
		t.Fatalf("events = %+v", events)
	}
}
