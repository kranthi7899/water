// Package trace writes per-run JSONL traces and aggregates run statistics.
// Nothing here ever leaves the machine.
package trace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"water/internal/backend"
	"water/internal/orchestrator"
	"water/internal/tools"
)

// Event is one JSONL line.
type Event struct {
	At         time.Time                  `json:"at"`
	Type       string                     `json:"type"` // run_started, node_started, node_finished, backend_call, message, tool_call, prompt_assembled, checkpoint, run_finished, error
	RunID      string                     `json:"run_id"`
	Role       string                     `json:"role,omitempty"`
	Backend    string                     `json:"backend,omitempty"`
	Metered    *bool                      `json:"metered,omitempty"`
	DurationMS int64                      `json:"duration_ms,omitempty"`
	InTokens   int                        `json:"input_tokens,omitempty"`
	OutTokens  int                        `json:"output_tokens,omitempty"`
	Message    *orchestrator.AgentMessage `json:"message,omitempty"`
	Text       string                     `json:"text,omitempty"`
	Error      string                     `json:"error,omitempty"`
	Tool       *tools.Event               `json:"tool,omitempty"`
	Skills     []string                   `json:"skills,omitempty"`
	MemoryIDs  []string                   `json:"memory_ids,omitempty"`
	InboxIDs   []string                   `json:"inbox_ids,omitempty"`
	Step       int                        `json:"step,omitempty"`
	RateLimit  *backend.RateLimit         `json:"rate_limit,omitempty"`
}

// RoleTiming is per-role wall time and call count.
type RoleTiming struct {
	Role     string        `json:"role"`
	Calls    int           `json:"calls"`
	Duration time.Duration `json:"duration_ns"`
}

// Stats is the end-of-run summary.
type Stats struct {
	RunID         string             `json:"run_id"`
	Backends      []string           `json:"backends"`
	Calls         int                `json:"calls"`
	MeteredCalls  int                `json:"metered_calls"`
	InputTokens   int                `json:"input_tokens"`
	OutputTokens  int                `json:"output_tokens"`
	Wall          time.Duration      `json:"wall_ns"`
	Messages      int                `json:"messages"`
	ToolCalls     int                `json:"tool_calls"`
	ToolDenials   int                `json:"tool_denials"`
	RateLimited   int                `json:"rate_limited"`
	LastRateLimit *backend.RateLimit `json:"last_rate_limit,omitempty"`
	Roles         []RoleTiming       `json:"roles"`
	TracePath     string             `json:"trace_path,omitempty"`
}

// Recorder writes events and accumulates Stats. Safe for concurrent use.
type Recorder struct {
	mu       sync.Mutex
	f        *os.File
	enc      *json.Encoder
	path     string
	runID    string
	started  time.Time
	backends map[string]bool
	stats    Stats
	roles    map[string]*RoleTiming
	calls    map[string]tools.Event
	seq      int
}

// CallRecord is the exact request a node sent and what came back, written to
// <trace_dir>/<run-id>.calls/<seq>-<role>.json so one node's decision can be
// replayed in isolation (water replay). It holds the full persona, memory and
// inbox text for that call; it stays local, like checkpoints.
type CallRecord struct {
	Seq          int       `json:"seq"`
	RunID        string    `json:"run_id"`
	Role         string    `json:"role"`
	At           time.Time `json:"at"`
	Backend      string    `json:"backend"`
	Model        string    `json:"model,omitempty"`
	System       string    `json:"system"`
	Prompt       string    `json:"prompt"`
	Attachments  []string  `json:"attachments,omitempty"`
	Tools        []string  `json:"tools,omitempty"`
	Skills       []string  `json:"skills,omitempty"`
	MemoryIDs    []string  `json:"memory_ids,omitempty"`
	InboxIDs     []string  `json:"inbox_ids,omitempty"`
	Response     string    `json:"response"`
	Error        string    `json:"error,omitempty"`
	DurationMS   int64     `json:"duration_ms"`
	InputTokens  int       `json:"input_tokens,omitempty"`
	OutputTokens int       `json:"output_tokens,omitempty"`
	ReplayOf     int       `json:"replay_of,omitempty"`
}

// CallsDir is where call records for runID live ("" when not writing).
func (r *Recorder) CallsDir() string {
	if r.path == "" {
		return ""
	}
	return strings.TrimSuffix(r.path, ".jsonl") + ".calls"
}

// CallsDirFor returns the call-record directory for a run under traceDir.
func CallsDirFor(traceDir, runID string) string {
	return filepath.Join(traceDir, runID+".calls")
}

// RecordCall assigns the next sequence number, writes the record, and emits a
// call_recorded event linking the trace to the file.
func (r *Recorder) RecordCall(rec CallRecord) int {
	r.mu.Lock()
	r.seq++
	rec.Seq = r.seq
	rec.RunID = r.runID
	dir := r.CallsDir()
	r.mu.Unlock()
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err == nil {
			if b, err := json.MarshalIndent(rec, "", "  "); err == nil {
				_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%03d-%s.json", rec.Seq, rec.Role)), b, 0o600)
			}
		}
	}
	r.mu.Lock()
	r.emit(Event{Type: "call_recorded", Role: rec.Role, Step: rec.Seq, Backend: rec.Backend})
	r.mu.Unlock()
	return rec.Seq
}

// RunID returns the run this recorder belongs to.
func (r *Recorder) RunID() string { return r.runID }

// Resolve looks up a traced tool invocation. It resolves ONLY references to
// this recorder's own run: a reference into another run is not found even if
// such a call id exists elsewhere (the COO's scope is the current run).
func (r *Recorder) Resolve(ref orchestrator.TraceRef) (tools.Event, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ref.RunID != "" && ref.RunID != r.runID {
		return tools.Event{}, false
	}
	ev, ok := r.calls[ref.CallID]
	return ev, ok && ev.Allowed && ev.Error == ""
}

// New opens <dir>/<runID>.jsonl. If dir is empty, events are counted but not
// written.
func New(dir, runID string) (*Recorder, error) {
	r := &Recorder{runID: runID, started: time.Now(), backends: map[string]bool{}, roles: map[string]*RoleTiming{}, calls: map[string]tools.Event{}}
	r.stats.RunID = runID
	if dir == "" {
		return r, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	r.path = filepath.Join(dir, runID+".jsonl")
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	r.f = f
	r.enc = json.NewEncoder(f)
	r.stats.TracePath = r.path
	return r, nil
}

// Path returns the trace file path ("" when not writing).
func (r *Recorder) Path() string { return r.path }

func (r *Recorder) emit(e Event) {
	e.At = time.Now()
	e.RunID = r.runID
	if r.enc != nil {
		_ = r.enc.Encode(e)
	}
}

// RunStarted records the start of a run.
func (r *Recorder) RunStarted(brief string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emit(Event{Type: "run_started", Text: brief})
}

// NodeStarted records a node beginning.
func (r *Recorder) NodeStarted(role string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emit(Event{Type: "node_started", Role: role})
}

// NodeFinished records a node ending.
func (r *Recorder) NodeFinished(role string, dur time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := Event{Type: "node_finished", Role: role, DurationMS: dur.Milliseconds()}
	if err != nil {
		e.Error = err.Error()
	}
	r.emit(e)
}

// BackendCall records one model call and updates stats.
func (r *Recorder) BackendCall(role, backend string, metered bool, dur time.Duration, in, out int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := metered
	e := Event{Type: "backend_call", Role: role, Backend: backend, Metered: &m, DurationMS: dur.Milliseconds(), InTokens: in, OutTokens: out}
	if err != nil {
		e.Error = err.Error()
	}
	r.emit(e)
	r.stats.Calls++
	if metered {
		r.stats.MeteredCalls++
	}
	r.stats.InputTokens += in
	r.stats.OutputTokens += out
	r.backends[backend] = true
	rt := r.roles[role]
	if rt == nil {
		rt = &RoleTiming{Role: role}
		r.roles[role] = rt
	}
	rt.Calls++
	rt.Duration += dur
}

// Message records an AgentMessage.
func (r *Recorder) Message(m orchestrator.AgentMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	mm := m
	r.emit(Event{Type: "message", Role: m.From, Message: &mm})
	r.stats.Messages++
}

// Error records a run-level error.
func (r *Recorder) Error(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emit(Event{Type: "error", Error: err.Error()})
}

// Finish writes run_finished, closes the file, and returns the Stats.
func (r *Recorder) Finish() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stats.Wall = time.Since(r.started)
	r.stats.Backends = r.stats.Backends[:0]
	for b := range r.backends {
		r.stats.Backends = append(r.stats.Backends, b)
	}
	sort.Strings(r.stats.Backends)
	r.stats.Roles = r.stats.Roles[:0]
	for _, rt := range r.roles {
		r.stats.Roles = append(r.stats.Roles, *rt)
	}
	sort.Slice(r.stats.Roles, func(i, j int) bool { return r.stats.Roles[i].Role < r.stats.Roles[j].Role })
	r.emit(Event{Type: "run_finished", DurationMS: r.stats.Wall.Milliseconds()})
	if r.f != nil {
		_ = r.f.Close()
		r.f, r.enc = nil, nil
	}
	return r.stats
}

// Stats returns the current aggregate without finishing.
func (r *Recorder) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats
}

// ToolCall records one traced tool invocation (Part 5.6).
func (r *Recorder) ToolCall(ev tools.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := ev
	if ev.CallID != "" {
		r.calls[ev.CallID] = ev
	}
	r.emit(Event{Type: "tool_call", Role: ev.Role, Tool: &e, DurationMS: ev.DurationMS, Error: ev.Error})
	r.stats.ToolCalls++
	if !ev.Allowed {
		r.stats.ToolDenials++
	}
}

// PromptAssembled records the traceability inputs of one prompt (`/why`).
func (r *Recorder) PromptAssembled(role string, skills, memoryIDs, inboxIDs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emit(Event{Type: "prompt_assembled", Role: role, Skills: skills, MemoryIDs: memoryIDs, InboxIDs: inboxIDs})
}

// RateLimit records the window state a backend reported, and — when limited —
// that the run was interrupted by the subscription budget rather than a
// generic failure. diagnose counts these.
func (r *Recorder) RateLimit(role string, rl *backend.RateLimit, limited bool) {
	if rl == nil && !limited {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e := Event{Type: "rate_limit", Role: role, RateLimit: rl}
	if limited {
		e.Type = "rate_limited"
		r.stats.RateLimited++
	}
	r.emit(e)
	if rl != nil {
		r.stats.LastRateLimit = rl
	}
}

// Checkpoint records that a durable checkpoint was written.
func (r *Recorder) Checkpoint(step int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emit(Event{Type: "checkpoint", Step: step})
}
