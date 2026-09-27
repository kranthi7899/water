package research_test

import (
	"context"
	"os"
	"testing"
	"time"

	"water/internal/connectors/research"
)

// BenchQueries are the live-style questions docs/slices/W.md §15 measured.
var BenchQueries = []string{
	"What's the weather in Dublin right now?",
	"latest AI news",
	"EUR/USD exchange rate today",
	"latest Premier League score",
	"what happened in tech this week",
}

// TestLatencyBenchRealSubscription runs BenchQueries through the real
// CLIRunner (the Claude subscription CLI, never an API key) one after
// another, a few seconds apart as spoken questions are, and logs each
// call's per-phase timing: the first call is cold, later ones use the warm
// spare. Manual only (it spawns claude and reaches the network): skipped
// unless WATER_RESEARCH_BENCH=1. WATER_RESEARCH_MODEL picks the model
// (default research.DefaultModel).
func TestLatencyBenchRealSubscription(t *testing.T) {
	if os.Getenv("WATER_RESEARCH_BENCH") != "1" {
		t.Skip("manual latency bench; set WATER_RESEARCH_BENCH=1")
	}
	defer research.Shutdown()
	r := research.CLIRunner{Model: os.Getenv("WATER_RESEARCH_MODEL")}
	for i, q := range BenchQueries {
		if i > 0 {
			time.Sleep(3 * time.Second)
		}
		s := time.Now()
		a, err := r.Search(context.Background(), q, research.DefaultSources)
		el := time.Since(s).Round(time.Millisecond)
		if err != nil {
			t.Errorf("%q: %v after %s", q, err, el)
			continue
		}
		tm := a.Timing
		t.Logf("BENCH %-40q total=%-8s warm=%-5v setup=%5d first_out=%5d first_tool=%5d search=%5d(%d) fetch=%5d(%d) answer=%5d turns=%d srcs=%d summary=%.70q",
			q, el, tm.Warm, tm.SetupMS, tm.FirstOutputMS, tm.FirstToolMS, tm.SearchMS, tm.Searches, tm.FetchMS, tm.Fetches, tm.AnswerMS, tm.Turns, len(a.Sources), a.Summary)
	}
}
