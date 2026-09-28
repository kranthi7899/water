package runtime

import (
	"sync"
	"time"
)

// ToolSpan is one tool call's wall-clock span as the connector that ran it
// measured it, with optional per-phase timings (milliseconds). It is a
// latency hook only (docs/slices/W.md §15): route_log reads the spans that
// fall inside a main-path turn to split its latency into "model until the
// tool call", "the tool" and "the tool's result until the answer". Spans
// carry no arguments or output.
type ToolSpan struct {
	Function string
	Start    time.Time
	End      time.Time
	Phases   map[string]int64
}

// toolSpanCap bounds the ring of recent spans (a few turns' worth).
const toolSpanCap = 64

var toolSpans struct {
	mu   sync.Mutex
	ring []ToolSpan
}

// NoteToolSpan records s. It never blocks on anything but a short lock and
// keeps only the most recent toolSpanCap spans.
func NoteToolSpan(s ToolSpan) {
	if s.Function == "" || s.End.Before(s.Start) {
		return
	}
	toolSpans.mu.Lock()
	defer toolSpans.mu.Unlock()
	toolSpans.ring = append(toolSpans.ring, s)
	if over := len(toolSpans.ring) - toolSpanCap; over > 0 {
		toolSpans.ring = append(toolSpans.ring[:0], toolSpans.ring[over:]...)
	}
}

// ToolSpansWithin returns the recorded spans that started at or after from
// and ended at or before to, oldest first. The caller decides whether they
// are really its own (route_log only uses them for a turn whose tool
// attribution stayed unambiguous).
func ToolSpansWithin(from, to time.Time) []ToolSpan {
	toolSpans.mu.Lock()
	defer toolSpans.mu.Unlock()
	var out []ToolSpan
	for _, s := range toolSpans.ring {
		if !s.Start.Before(from) && !s.End.After(to) {
			out = append(out, s)
		}
	}
	return out
}
