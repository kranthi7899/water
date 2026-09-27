package research_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"water/internal/connectors/research"
	"water/internal/runtime"
)

// TestWebRecordsTiming: a call whose runner measured its phases logs one
// timing line (no query, no answer text) and records a research.web runtime
// tool span carrying the research_* phases, so route_log can split the
// turn's latency around it. The output the model sees has no timing in it.
func TestWebRecordsTiming(t *testing.T) {
	var logged []string
	old := research.Logf
	research.Logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	defer func() { research.Logf = old }()

	fr := &fakeRunner{fn: func(ctx context.Context, q string, n int) (research.Answer, error) {
		time.Sleep(5 * time.Millisecond)
		return research.Answer{Summary: "Dublin: 14°C.", Sources: []research.Source{{Title: "Met", URL: "https://www.met.ie/"}},
			Timing: &research.Timing{Warm: true, SetupMS: 3, FirstToolMS: 1200, SearchMS: 3000, Searches: 1, AnswerMS: 1500, TotalMS: 5900, Turns: 2}}, nil
	}}
	g := newGate(t, research.New(fr))
	before := time.Now()
	res, _, err := invoke(t, g, map[string]any{"query": "weather in Dublin secretword"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(res.Output), "research_") || strings.Contains(string(res.Output), "timing") {
		t.Fatalf("timing leaked into the model-visible output: %s", res.Output)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "warm=true") || !strings.Contains(logged[0], "search=3000ms(1)") || strings.Contains(logged[0], "secretword") || strings.Contains(logged[0], "Dublin") {
		t.Fatalf("log = %q", logged)
	}
	spans := runtime.ToolSpansWithin(before, time.Now())
	var found *runtime.ToolSpan
	for i := range spans {
		if spans[i].Function == "research.web" && spans[i].Phases["research_total"] == 5900 {
			found = &spans[i]
		}
	}
	if found == nil || found.Phases["research_warm"] != 1 || found.End.Sub(found.Start) < 5*time.Millisecond {
		t.Fatalf("span = %+v (all %+v)", found, spans)
	}

	// A runner that measured nothing (a fake, or an early refusal) logs nothing.
	logged = nil
	g2 := newGate(t, research.New(&fakeRunner{}))
	if _, _, err := invoke(t, g2, map[string]any{"query": "latest AI news"}); err != nil {
		t.Fatal(err)
	}
	if len(logged) != 0 {
		t.Fatalf("logged %q for an unmeasured call", logged)
	}
}
