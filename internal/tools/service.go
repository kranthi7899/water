package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Event is one tool invocation as recorded in the JSONL log: role, tool,
// arguments, permission decision and basis, result (by hash plus a
// truncated preview — never the full contents, which may be sensitive),
// duration, error.
type Event struct {
	At         time.Time      `json:"at"`
	CallID     string         `json:"call_id"` // unique per invocation
	RunID      string         `json:"run_id,omitempty"`
	Role       string         `json:"role"`
	Tool       string         `json:"tool"`
	Args       map[string]any `json:"args"`
	Allowed    bool           `json:"allowed"`
	Basis      string         `json:"basis"`
	ResultSHA  string         `json:"result_sha256,omitempty"`
	ResultSize int            `json:"result_bytes,omitempty"`
	Preview    string         `json:"result_preview,omitempty"`
	DurationMS int64          `json:"duration_ms"`
	Error      string         `json:"error,omitempty"`
}

// PreviewBytes is how much of a result the log keeps inline.
const PreviewBytes = 2048

// Service executes tool calls under a Policy and logs every one. In this
// twin-only shape, every tool it can run is a connector function proxied to
// the daemon's gate; the actual authorization decision (level, taint, an
// approved envelope, rate caps) is made there, not here.
type Service struct {
	Policy *Policy
	Log    func(Event)
	// CallTimeout bounds one invocation (0 = DefaultCallTimeout).
	CallTimeout time.Duration
	mu          sync.Mutex
	events      []Event
}

// NewService binds a policy to a logger (nil logger = collect only).
func NewService(p *Policy, log func(Event)) *Service { return &Service{Policy: p, Log: log} }

// Events returns every invocation this service has seen.
func (s *Service) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events...)
}

// Definition describes a tool for tools/list.
type Definition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Definitions lists the tools the policy exposes, with schemas.
func (s *Service) Definitions() []Definition {
	out := make([]Definition, 0, len(s.Policy.Twin))
	for _, tf := range s.Policy.Twin {
		var schema map[string]any
		_ = json.Unmarshal(tf.Schema, &schema)
		out = append(out, Definition{Name: tf.Tool, Description: tf.Description, InputSchema: schema})
	}
	return out
}

// Call authorises and executes one tool invocation. Denials are returned as
// errors AND logged; the model receives the denial text as the tool result so
// it can adapt without retrying blindly.
func (s *Service) Call(ctx context.Context, tool string, args map[string]any) (string, error) {
	start := time.Now()
	ev := Event{At: start, CallID: NewCallID(s.Policy.Role), Role: s.Policy.Role, Tool: tool, Args: args}
	dec, resolved := s.Policy.Authorize(tool, args)
	ev.Allowed, ev.Basis = dec.Allowed, dec.Basis
	var result string
	var err error
	if !dec.Allowed {
		err = fmt.Errorf("%w: %s", ErrDenied, dec.Basis)
	} else {
		result, err = s.callTwin(ctx, tool, resolved)
	}
	s.recordEvent(ev, result, err)
	return result, err
}

func (s *Service) recordEvent(ev Event, result string, err error) {
	ev.DurationMS = time.Since(ev.At).Milliseconds()
	if err != nil {
		ev.Error = err.Error()
	}
	if result != "" {
		sum := sha256.Sum256([]byte(result))
		ev.ResultSHA = hex.EncodeToString(sum[:])
		ev.ResultSize = len(result)
		if len(result) > PreviewBytes {
			ev.Preview = result[:PreviewBytes] + "…"
		} else {
			ev.Preview = result
		}
	}
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
	if s.Log != nil {
		s.Log(ev)
	}
}

// twinInvokeResponse is what the daemon's POST /v1/tools/invoke returns.
type twinInvokeResponse struct {
	Status     string          `json:"status"` // ok | queued | denied
	Output     json.RawMessage `json:"output,omitempty"`
	ApprovalID string          `json:"approval_id,omitempty"`
	Reason     string          `json:"reason,omitempty"`
}

// callTwin proxies one model-initiated connector call to the daemon's gate
// over the twin socket. The gate there — not this process — decides level,
// taint, approvals and rate caps; an A-level call comes back "queued" rather
// than executing inline.
func (s *Service) callTwin(ctx context.Context, tool string, args map[string]any) (string, error) {
	tf, _ := s.Policy.twinByTool(tool)
	body, err := json.Marshal(map[string]any{"function": tf.ID, "args": args})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://twin/v1/tools/invoke", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.Policy.TwinToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := twinHTTPClient(s.Policy.TwinSocket).Do(req)
	if err != nil {
		return "", fmt.Errorf("twin proxy: %w", err)
	}
	defer resp.Body.Close()
	var out twinInvokeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("twin proxy: bad response: %w", err)
	}
	switch out.Status {
	case "ok":
		return string(out.Output), nil
	case "queued":
		return fmt.Sprintf("queued for approval %s", out.ApprovalID), nil
	default:
		return "", fmt.Errorf("%w: %s", ErrDenied, out.Reason)
	}
}

// NewCallID returns a unique, citeable invocation id.
func NewCallID(role string) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return "call-" + role + "-" + hex.EncodeToString(b)
}

// AppendEvents appends events as JSONL to path (the parent process reads
// this back into Response.ToolEvents after the subprocess exits).
func AppendEvents(path string, evs ...Event) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range evs {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}

// ReadEvents parses a JSONL event file; a missing file yields nil.
func ReadEvents(path string) ([]Event, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Event
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e Event
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	return out, nil
}
