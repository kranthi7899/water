package nervous

import (
	"testing"
	"time"

	"water/internal/backend"
	"water/internal/runtime"
	"water/internal/store"
)

// TestToolSpanLatency (docs/slices/W.md §15): a main-path turn's latency is
// split around the spans of tools it used: the model's time to the call,
// the tool, the tool's result to done and (only without a preamble) to the
// first sentence, plus the tool's own research_* phases. Spans of tools the
// turn did not use are ignored, and no used span means no keys.
func TestToolSpanLatency(t *testing.T) {
	base := time.Unix(1_900_000_000, 0)
	at := func(ms int) time.Time { return base.Add(time.Duration(ms) * time.Millisecond) }
	spans := []runtime.ToolSpan{
		{Function: "research.web", Start: at(2000), End: at(9000), Phases: map[string]int64{"research_total": 6900, "research_warm": 1}},
		{Function: "gmail.search", Start: at(2500), End: at(3000)}, // another turn's call
	}
	first := at(10500)
	got := toolSpanLatency(at(0), at(13000), &first, []string{"research.web"}, spans)
	want := map[string]int64{"main_to_tool": 2000, "tool": 7000, "tool_to_done": 4000, "tool_to_first_sentence": 1500,
		"research_total": 6900, "research_warm": 1}
	if len(got) != len(want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %d, want %d (all %v)", k, got[k], v, got)
		}
	}
	// A preamble spoken before the call: no tool_to_first_sentence.
	early := at(800)
	if got := toolSpanLatency(at(0), at(13000), &early, []string{"research.web"}, spans); got["tool_to_first_sentence"] != 0 || got["tool_to_done"] != 4000 {
		t.Fatalf("preamble turn = %v", got)
	}
	if _, ok := toolSpanLatency(at(0), at(13000), &early, []string{"research.web"}, spans)["tool_to_first_sentence"]; ok {
		t.Fatal("tool_to_first_sentence set for a preamble turn")
	}
	// Two used calls: from the first start to the last end, durations summed.
	two := append(spans, runtime.ToolSpan{Function: "research.web", Start: at(9500), End: at(11000)})
	if got := toolSpanLatency(at(0), at(13000), nil, []string{"research.web"}, two); got["main_to_tool"] != 2000 || got["tool"] != 8500 || got["tool_to_done"] != 2000 {
		t.Fatalf("two calls = %v", got)
	}
	if got := toolSpanLatency(at(0), at(13000), &first, nil, spans); got != nil {
		t.Fatalf("no used tool = %v, want nil", got)
	}
}

// TestRouteLogResearchTimingKeys: end to end through Handle, a main turn
// that used research.web gets the tool-span keys in route_log latency_ms
// (new keys in the existing JSON column), next to the tier's own "main".
func TestRouteLogResearchTimingKeys(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule": tier0ScheduleYAML})
	env, ctx, fk := nervousTestEnv(t)
	n := nervousWithLoggingFor(t, reg, env.Store, nil)
	fk.Reply = func(req backend.Request) string {
		n.RecordToolAttempt("research.web")
		now := n.cfg.Clock.Now()
		runtime.NoteToolSpan(runtime.ToolSpan{Function: "research.web", Start: now, End: now,
			Phases: map[string]int64{"research_total": 6100, "research_warm": 1, "research_search": 3200}})
		n.RecordToolUse("research.web")
		return "It is 14 degrees in Dublin."
	}
	start := tier0FixedNow.Add(-time.Second)
	handleAndWait(n, ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "what's the weather in dublin right now", TaskID: "t-research-timing"})
	row := lastRoute(t, env.Store, start)
	if row.Owner != "main" {
		t.Fatalf("Owner = %q, want main", row.Owner)
	}
	for _, k := range []string{"main", "main_to_tool", "tool", "tool_to_done", "research_total", "research_warm", "research_search"} {
		if _, ok := row.LatencyMS[k]; !ok {
			t.Fatalf("latency_ms lacks %q: %v", k, row.LatencyMS)
		}
	}
	if row.LatencyMS["research_total"] != 6100 || row.LatencyMS["research_warm"] != 1 {
		t.Fatalf("latency_ms = %v", row.LatencyMS)
	}
	if row.Class != store.RouteClassGeneral {
		t.Fatalf("Class = %q, want general (class logic unchanged)", row.Class)
	}
}

// A main turn that used no tool keeps its latency_ms exactly as before.
func TestRouteLogNoToolNoTimingKeys(t *testing.T) {
	reg := tier0FixtureRegistry(t, map[string]string{"schedule": tier0ScheduleYAML})
	env, ctx, fk := nervousTestEnv(t)
	n := nervousWithLoggingFor(t, reg, env.Store, nil)
	fk.Reply = func(req backend.Request) string {
		// Another turn's span inside this turn's window, but not a tool this
		// turn used: ignored.
		now := n.cfg.Clock.Now()
		runtime.NoteToolSpan(runtime.ToolSpan{Function: "research.web", Start: now, End: now, Phases: map[string]int64{"research_total": 1}})
		return "Sure."
	}
	start := tier0FixedNow.Add(-time.Second)
	handleAndWait(n, ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "what should i prioritize today", TaskID: "t-no-tool-timing"})
	row := lastRoute(t, env.Store, start)
	for k := range row.LatencyMS {
		if k != "main" && k != "t0" {
			t.Fatalf("unexpected latency key %q: %v", k, row.LatencyMS)
		}
	}
}
