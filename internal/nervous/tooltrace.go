package nervous

import (
	"sort"
	"strings"
	"sync"
)

// ToolTracer attributes a tool call to whichever main-path turn(s) are
// currently in flight (Design §14, "Tool attribution"). The warm session
// serializes main-path turns in practice, so exactly one is normally in
// flight — but nothing here assumes that, since a future multi-conversation
// scheduler could change it, and the rule already covers 0/1/2+ explicitly.
//
// Nothing calls RecordUse yet: the model's tool-call path isn't wired to
// report into this until quick tools exist (task R-16). The attribution
// rule itself doesn't depend on that caller, so it's built and tested here;
// R-16 only has to wire a call site, not design this.
type ToolTracer struct {
	mu         sync.Mutex
	inFlight   map[string]bool
	used       map[string][]string
	attributed map[string]bool
}

// NewToolTracer builds an empty tracer.
func NewToolTracer() *ToolTracer {
	return &ToolTracer{
		inFlight:   map[string]bool{},
		used:       map[string][]string{},
		attributed: map[string]bool{},
	}
}

// BeginMain marks a main-path turn as in flight, ready to have tool uses
// attributed to it.
func (tt *ToolTracer) BeginMain(turnID string) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	tt.inFlight[turnID] = true
	tt.attributed[turnID] = true
}

// EndMain marks a main-path turn as finished, returning the tools it used
// (possibly none) and whether attribution stayed unambiguous for it the
// whole time it was in flight.
func (tt *ToolTracer) EndMain(turnID string) (used []string, attributed bool) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	used = tt.used[turnID]
	attributed = tt.attributed[turnID]
	delete(tt.inFlight, turnID)
	delete(tt.used, turnID)
	delete(tt.attributed, turnID)
	return used, attributed
}

// RecordUse attributes tool to every main-path turn currently in flight.
// Zero in flight: dropped, there is nothing to attribute to. Exactly one:
// attributed cleanly. Two or more: appended to every one of them, and each
// is marked unattributed from that point on — even if it's later the only
// one still in flight, an already-ambiguous turn stays ambiguous, since we
// can no longer tell which turn actually made which earlier call.
func (tt *ToolTracer) RecordUse(tool string) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	if len(tt.inFlight) == 0 {
		return
	}
	ambiguous := len(tt.inFlight) > 1
	for id := range tt.inFlight {
		tt.used[id] = append(tt.used[id], tool)
		if ambiguous {
			tt.attributed[id] = false
		}
	}
}

// QuickOnlySignature reports whether every tool name in used carries
// quickPrefix (a main turn that only ever reached for the sous chef's own
// quick.* tools, never a real connector), and if so, a canonical signature
// string — sorted, deduplicated, joined with "+" — for grouping repeated
// turns by which tools they used (the promotion loop's candidate detection,
// a later task). An empty used, or any non-quick tool present, reports
// quickOnly=false and an empty signature.
func QuickOnlySignature(used []string, quickPrefix string) (quickOnly bool, signature string) {
	if len(used) == 0 {
		return false, ""
	}
	seen := make(map[string]bool, len(used))
	names := make([]string, 0, len(used))
	for _, u := range used {
		if !strings.HasPrefix(u, quickPrefix) {
			return false, ""
		}
		if !seen[u] {
			seen[u] = true
			names = append(names, u)
		}
	}
	sort.Strings(names)
	return true, strings.Join(names, "+")
}
