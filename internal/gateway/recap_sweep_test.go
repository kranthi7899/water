package gateway

import (
	"context"
	"net/http"
	"testing"
	"time"

	"water/internal/backend"
)

// TestReadyRecapLeavesNoEntryInMemory is the fix for the adversarial
// review's finding (2026-09-26): a successful (ready) recap used to linger
// in d.recaps for the daemon's whole life. startRecapOnStop now deletes
// the entry the moment the durable store.Meeting write it depends on
// succeeds, since meetingViewOf's own !tracked fallback already reads that
// row correctly with no in-memory state needed. This proves the map is
// left empty afterward, not just that the API still reports "ready".
func TestReadyRecapLeavesNoEntryInMemory(t *testing.T) {
	h := newHarness(t)
	h.fake.Reply = func(backend.Request) string { return "Decisions: shipped it." }

	id := h.startMeeting(t, `{}`)
	if code := h.status(t, "/v1/meetings/"+id+"/segments", `{"at":"2026-09-24T15:00:00Z","channel":"mic","text":"we shipped it"}`); code != http.StatusOK {
		t.Fatalf("segment: status = %d", code)
	}
	if code := h.status(t, "/v1/meetings/"+id+"/stop", `{}`); code != http.StatusOK {
		t.Fatalf("stop: status = %d", code)
	}
	h.d.bg.Wait()

	var mv meetingView
	decodeInto(t, do(t, h.srv.URL, "GET", "/v1/meetings/"+id, "", h.token), http.StatusOK, &mv)
	if mv.Recap != recapReady {
		t.Fatalf("recap = %q, want ready", mv.Recap)
	}

	h.d.recapMu.Lock()
	_, tracked := h.d.recaps[id]
	n := len(h.d.recaps)
	h.d.recapMu.Unlock()
	if tracked {
		t.Fatalf("a ready recap left an entry in d.recaps for %q; want it deleted immediately", id)
	}
	if n != 0 {
		t.Fatalf("len(d.recaps) = %d, want 0 (nothing else should be tracked in this test)", n)
	}
}

// TestSweepRecapsEvictsOnlyStaleTerminalEntries proves sweepRecaps' exact
// eviction rule: a skipped/failed entry older than recapRetention is
// removed; a recent one, and a running one no matter how old, are left
// alone (running is bounded by recapTimeout on its own and must never be
// evicted out from under an in-flight goroutine).
func TestSweepRecapsEvictsOnlyStaleTerminalEntries(t *testing.T) {
	h := newHarness(t)
	now := time.Now()
	stale := now.Add(-recapRetention - time.Minute)
	fresh := now.Add(-time.Minute)

	h.d.recapMu.Lock()
	h.d.recaps["old-skipped"] = recapState{Status: recapSkipped, At: stale}
	h.d.recaps["old-failed"] = recapState{Status: recapFailed, At: stale}
	h.d.recaps["fresh-skipped"] = recapState{Status: recapSkipped, At: fresh}
	h.d.recaps["fresh-failed"] = recapState{Status: recapFailed, At: fresh}
	h.d.recaps["old-running"] = recapState{Status: recapRunning, At: stale}
	h.d.recapMu.Unlock()

	h.d.sweepRecaps(now)

	h.d.recapMu.Lock()
	defer h.d.recapMu.Unlock()
	if _, ok := h.d.recaps["old-skipped"]; ok {
		t.Error("old-skipped survived the sweep, want evicted")
	}
	if _, ok := h.d.recaps["old-failed"]; ok {
		t.Error("old-failed survived the sweep, want evicted")
	}
	if _, ok := h.d.recaps["fresh-skipped"]; !ok {
		t.Error("fresh-skipped was evicted, want kept (not yet past retention)")
	}
	if _, ok := h.d.recaps["fresh-failed"]; !ok {
		t.Error("fresh-failed was evicted, want kept (not yet past retention)")
	}
	if _, ok := h.d.recaps["old-running"]; !ok {
		t.Error("old-running was evicted, want kept (a running entry is never swept)")
	}
}

// TestRunRecapSweepStopsWithContext proves RunRecapSweep's loop actually
// exits promptly once its context is cancelled, mirroring
// internal/nervous/turn.Table.RunSweep's own equivalent test.
func TestRunRecapSweepStopsWithContext(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.d.RunRecapSweep(ctx, time.Millisecond)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunRecapSweep did not return after its context was cancelled")
	}
}
