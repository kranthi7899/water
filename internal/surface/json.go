package surface

import (
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"

	"water/internal/backend"
	"water/internal/orchestrator"
)

func init() {
	Register("json", func(o Options) Surface { return NewJSON(os.Stdout) })
}

// JSON emits one JSON object per event to w (`--output json`). This is the
// machine-readable stream a dashboard consumes later.
type JSON struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func NewJSON(w io.Writer) *JSON { return &JSON{enc: json.NewEncoder(w)} }

func (j *JSON) Name() string { return "json" }

func (j *JSON) emit(v any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	_ = j.enc.Encode(v)
}

type jsonEvent struct {
	Event string    `json:"event"`
	At    time.Time `json:"at"`
	RunID string    `json:"run_id,omitempty"`
	Brief string    `json:"brief,omitempty"`
	Role  string    `json:"role,omitempty"`
	Error string    `json:"error,omitempty"`

	Backend    string `json:"backend,omitempty"`
	Metered    *bool  `json:"metered,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Text       string `json:"text,omitempty"`

	Message *orchestrator.AgentMessage `json:"message,omitempty"`
	Final   string                     `json:"final,omitempty"`
	Stats   *RunStats                  `json:"stats,omitempty"`
}

func (j *JSON) RunStarted(runID, brief string) {
	j.emit(jsonEvent{Event: "run_started", At: time.Now(), RunID: runID, Brief: brief})
}
func (j *JSON) NodeStarted(role string) {
	j.emit(jsonEvent{Event: "node_started", At: time.Now(), Role: role})
}
func (j *JSON) NodeFinished(role string, r backend.Response) {
	m := r.Metered
	j.emit(jsonEvent{Event: "node_finished", At: time.Now(), Role: role, Backend: r.Backend, Metered: &m, DurationMS: r.Duration.Milliseconds(), Text: r.Text})
}
func (j *JSON) NodeFailed(role string, err error) {
	j.emit(jsonEvent{Event: "node_failed", At: time.Now(), Role: role, Error: err.Error()})
}
func (j *JSON) MessageSent(m orchestrator.AgentMessage) {
	mm := m
	j.emit(jsonEvent{Event: "message", At: time.Now(), Message: &mm})
}
func (j *JSON) RunFinished(final string, s RunStats) {
	st := s
	j.emit(jsonEvent{Event: "run_finished", At: time.Now(), RunID: s.RunID, Final: final, Stats: &st})
}
