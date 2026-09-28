package research

import (
	"encoding/json"
	"time"
)

// Timing is one research call's per-phase latency (docs/slices/W.md §15).
// Every duration is in milliseconds from the start of Search; a phase that
// never happened is 0.
type Timing struct {
	Warm          bool  // the call used the pre-started spare
	SetupMS       int64 // until the process was ready and had the question (spawn on a cold call)
	FirstOutputMS int64 // until the process's first output line
	FirstToolMS   int64 // until the model's first tool call (its first turn)
	SearchMS      int64 // total time inside WebSearch calls
	Searches      int
	FetchMS       int64 // total time inside WebFetch calls
	Fetches       int
	AnswerMS      int64 // from the last tool result to the final result (the answering turn)
	TotalMS       int64
	Turns         int // the CLI's num_turns
}

// Phases is t as route_log latency keys (research_*), for runtime tool spans.
func (t Timing) Phases() map[string]int64 {
	warm := int64(0)
	if t.Warm {
		warm = 1
	}
	return map[string]int64{
		"research_warm":         warm,
		"research_setup":        t.SetupMS,
		"research_first_output": t.FirstOutputMS,
		"research_first_tool":   t.FirstToolMS,
		"research_search":       t.SearchMS,
		"research_searches":     int64(t.Searches),
		"research_fetch":        t.FetchMS,
		"research_fetches":      int64(t.Fetches),
		"research_answer":       t.AnswerMS,
		"research_total":        t.TotalMS,
		"research_turns":        int64(t.Turns),
	}
}

// tracker reads the research process's stream-json output line by line,
// times its phases and keeps the final result line. It is pure (the caller
// supplies each line's arrival time) so tests can drive it.
type tracker struct {
	t0          time.Time
	ready       time.Time
	firstOut    time.Time
	firstTool   time.Time
	lastToolEnd time.Time
	open        map[string]openTool
	search      time.Duration
	fetch       time.Duration
	searches    int
	fetches     int
	turns       int
	result      []byte
}

type openTool struct {
	name string
	at   time.Time
}

func newTracker(t0 time.Time) *tracker {
	return &tracker{t0: t0, open: map[string]openTool{}}
}

// streamLine is the part of one stream-json event the tracker reads.
type streamLine struct {
	Type    string `json:"type"`
	NumTurn int    `json:"num_turns"`
	Message *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolUseID string `json:"tool_use_id"`
}

// line consumes one output line that arrived at at. It reports true once
// the final result event has been read (t.result then holds it).
func (t *tracker) line(at time.Time, raw []byte) bool {
	if t.firstOut.IsZero() {
		t.firstOut = at
	}
	var ev streamLine
	if json.Unmarshal(raw, &ev) != nil {
		return false
	}
	switch ev.Type {
	case "result":
		t.result = append([]byte(nil), raw...)
		t.turns = ev.NumTurn
		return true
	case "assistant", "user":
		if ev.Message == nil {
			return false
		}
		var blocks []contentBlock
		if json.Unmarshal(ev.Message.Content, &blocks) != nil {
			return false // a plain-string content (the echoed question) carries no tools
		}
		for _, b := range blocks {
			switch {
			case b.Type == "tool_use" && b.ID != "":
				if t.firstTool.IsZero() {
					t.firstTool = at
				}
				t.open[b.ID] = openTool{name: b.Name, at: at}
			case b.Type == "tool_result":
				o, ok := t.open[b.ToolUseID]
				if !ok {
					continue
				}
				delete(t.open, b.ToolUseID)
				d := at.Sub(o.at)
				switch o.name {
				case "WebSearch":
					t.search += d
					t.searches++
				case "WebFetch":
					t.fetch += d
					t.fetches++
				}
				t.lastToolEnd = at
			}
		}
	}
	return false
}

// timing closes the clock at end.
func (t *tracker) timing(warm bool, end time.Time) Timing {
	since := func(x time.Time) int64 {
		if x.IsZero() {
			return 0
		}
		return x.Sub(t.t0).Milliseconds()
	}
	tm := Timing{
		Warm:          warm,
		SetupMS:       since(t.ready),
		FirstOutputMS: since(t.firstOut),
		FirstToolMS:   since(t.firstTool),
		SearchMS:      t.search.Milliseconds(),
		Searches:      t.searches,
		FetchMS:       t.fetch.Milliseconds(),
		Fetches:       t.fetches,
		TotalMS:       end.Sub(t.t0).Milliseconds(),
		Turns:         t.turns,
	}
	from := t.lastToolEnd
	if from.IsZero() {
		from = t.ready
	}
	if !from.IsZero() && t.result != nil {
		tm.AnswerMS = end.Sub(from).Milliseconds()
	}
	return tm
}
