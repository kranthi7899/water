package nervous

import (
	"sort"
	"strings"
	"sync"
)

// quickToolPrefix is the id prefix every sous-chef quick.* tool carries
// (reflex.FunctionSpec.QuickTool, e.g. "quick.calendar" — the dotted
// function id gateway/quick.go's handleQuickInvoke actually records via
// RecordToolUse, not the "quick__" MCP-safe tool name a model-facing
// definition uses). It is the prefix routelog.go's finish() passes to
// QuickOnlySignature when deciding a main turn's quick_only/tool_signature
// fields.
const quickToolPrefix = "quick."

// ToolTracer attributes a tool call to whichever main-path turn(s) are
// currently in flight (Design §14, "Tool attribution"). The warm session
// serializes main-path turns in practice, so exactly one is normally in
// flight — but nothing here assumes that, since a future multi-conversation
// scheduler could change it, and the rule already covers 0/1/2+ explicitly.
//
// RecordUse has had a real caller since R-16 (gateway/quick.go's
// handleQuickInvoke, via Nervous.RecordToolUse), but until this task
// (R-22) nothing ever called BeginMain/EndMain to register a turn as in
// flight — so every RecordUse call found tt.inFlight empty and silently
// dropped, and no real route_log row ever got a populated tools_used,
// tools_attributed, quick_only or tool_signature. mainpath.go's answerMain
// now brackets the main path with BeginMain/EndMain, closing that gap.
type ToolTracer struct {
	mu         sync.Mutex
	inFlight   map[string]bool
	used       map[string][]string
	attributed map[string]bool
	// attempted holds every twin function a main turn asked the gateway to
	// run (/v1/tools/invoke), whatever happened next: executed, queued for
	// approval, or denied (Slice W, D6). used only ever sees executed calls,
	// so a turn whose one call was a queued gmail.send_message would look
	// tool-free there and be misclassed "general".
	attempted map[string][]string
	// ended parks a finished turn's attempted set between EndMain (run from
	// answerMain's defer) and the route_log write (routeRecorder.finish,
	// run later from Handle's own defer), which pops it with TakeAttempts.
	ended map[string][]string
}

// maxEndedTraces bounds ended so a caller that runs EndMain without a
// matching TakeAttempts (a unit test driving the tracer directly) can never
// grow it without limit; the map is simply reset past this size.
const maxEndedTraces = 256

// NewToolTracer builds an empty tracer.
func NewToolTracer() *ToolTracer {
	return &ToolTracer{
		inFlight:   map[string]bool{},
		used:       map[string][]string{},
		attributed: map[string]bool{},
		attempted:  map[string][]string{},
		ended:      map[string][]string{},
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
	if tt.inFlight[turnID] {
		if len(tt.ended) >= maxEndedTraces {
			tt.ended = map[string][]string{}
		}
		tt.ended[turnID] = tt.attempted[turnID]
	}
	delete(tt.inFlight, turnID)
	delete(tt.used, turnID)
	delete(tt.attributed, turnID)
	delete(tt.attempted, turnID)
	return used, attributed
}

// RecordAttempt attributes an attempted twin function call to every
// main-path turn in flight, under exactly RecordUse's rules (zero: dropped;
// two or more: recorded on each and each marked unattributed).
func (tt *ToolTracer) RecordAttempt(tool string) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	if len(tt.inFlight) == 0 {
		return
	}
	ambiguous := len(tt.inFlight) > 1
	for id := range tt.inFlight {
		tt.attempted[id] = append(tt.attempted[id], tool)
		if ambiguous {
			tt.attributed[id] = false
		}
	}
}

// TakeAttempts returns (and forgets) the attempted calls EndMain parked for
// turnID. A turn that never reached the main path, or was already taken,
// reports nil.
func (tt *ToolTracer) TakeAttempts(turnID string) []string {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	a := tt.ended[turnID]
	delete(tt.ended, turnID)
	return a
}

// RecordToolAttempt records that the in-flight main turn asked the gateway
// to run fn (internal/gateway's handleToolInvoke calls it for every request
// body that decodes, before the gate decides anything). route_log's class
// column reads it: a turn that attempted any company function is "company"
// even when that call was only queued or denied (Slice W, D6).
func (n *Nervous) RecordToolAttempt(fn string) {
	if fn == "" {
		return
	}
	n.toolTracer.RecordAttempt(fn)
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
