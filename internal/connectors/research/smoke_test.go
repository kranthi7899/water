package research_test

import (
	"context"
	"os"
	"testing"
	"time"

	"water/internal/connectors/research"
	"water/internal/gate"
)

// TestSmokeRealSubscription runs one real research.web call through the
// gate with the real CLIRunner (a `claude --print` on the Claude
// subscription). It is manual only: it spawns claude and reaches the
// network, so it is skipped unless WATER_RESEARCH_SMOKE=1, and never runs
// in the gates. WATER_RESEARCH_MODEL picks the model (default haiku).
func TestSmokeRealSubscription(t *testing.T) {
	if os.Getenv("WATER_RESEARCH_SMOKE") != "1" {
		t.Skip("manual smoke test; set WATER_RESEARCH_SMOKE=1")
	}
	model := os.Getenv("WATER_RESEARCH_MODEL")
	if model == "" {
		model = "haiku"
	}
	g := newGate(t, research.New(research.CLIRunner{Model: model}))
	start := time.Now()
	res, err := g.Invoke(context.Background(), gate.Call{Function: "research.web",
		Args: map[string]any{"query": "current weather in Dublin, Ireland", "max_sources": 3}, Origin: gate.P0, Taint: gate.Clean})
	t.Logf("elapsed %s untrusted=%v", time.Since(start).Round(time.Millisecond), res.Untrusted)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("output: %s", res.Output)
}
