// Package backend defines the model-call seam (extension point 1).
//
// A Backend turns an assembled Request into a Response. The design goal is to
// route every call through a subscription CLI (quota already paid for) and to
// make metered billing impossible to fall into silently: every Response carries
// a Metered flag, and selection (Select) refuses metered backends unless the
// user has opted in.
package backend

import (
	"context"
	"errors"
	"strings"
	"time"

	"water/internal/tools"
)

// Attachment is a file the user explicitly handed to a role (Part 5.4). It
// is delivered as a structured content block where the backend supports it,
// and is ALWAYS untrusted content from the role's point of view.
type Attachment struct {
	Name      string // display name
	MediaType string // image/png, application/pdf, text/plain …
	Kind      string // image | document | text
	Data      []byte
}

// Request is one model turn. System is the assembled persona context; Prompt is
// the actual turn. Role is for tracing/attribution only and must never affect
// which backend is used.
type Request struct {
	System  string
	Prompt  string
	Role    string
	Timeout time.Duration

	// Model optionally pins a model for this call ("" = the CLI's default).
	// Set from role.yaml model: (Part 8).
	Model string

	// Attachments are delivered as content blocks when the backend can.
	Attachments []Attachment

	// Tools, when non-nil and non-empty, makes Water's MCP tool server
	// available to the subprocess under this policy (Part 5). ToolLog is the
	// JSONL path the child appends invocation events to.
	Tools   *tools.Policy
	ToolLog string
}

// Response is the result of a Backend.Run. Raw holds unparsed output for
// debugging. Metered is TRUE if this call cost metered money.
type Response struct {
	Text         string
	Raw          string
	Metered      bool
	Backend      string
	Model        string
	InputTokens  int
	OutputTokens int
	Duration     time.Duration

	// ToolEvents are the traced tool invocations made during this call.
	ToolEvents []tools.Event
	// AttachmentsDelivered reports whether attachments reached the model as
	// structured blocks (false = they were dropped or inlined as text).
	AttachmentsDelivered bool
	// RateLimit is the subscription window state the backend reported during
	// this call, when it exposes one (Part 5 follow-up).
	RateLimit *RateLimit
	// ContextWindow is the model's context size in tokens when the backend
	// reports it (0 = unknown). InputTokens is the context consumed this call.
	ContextWindow int
}

// RateLimit is what a subscription CLI reports about its usage windows. Under
// subscription billing this, not tokens, is the binding budget.
type RateLimit struct {
	Backend        string    `json:"backend"`
	ObservedAt     time.Time `json:"observed_at"`
	Status         string    `json:"status"` // allowed | limited | …
	WindowType     string    `json:"window_type,omitempty"`
	FiveHourUsed   float64   `json:"five_hour_utilization"` // 0..1
	FiveHourResets time.Time `json:"five_hour_resets_at,omitempty"`
	SevenDayUsed   float64   `json:"seven_day_utilization"`
	SevenDayResets time.Time `json:"seven_day_resets_at,omitempty"`
	Message        string    `json:"message,omitempty"`
}

// ErrRateLimited marks a call refused because the subscription window is
// exhausted. Callers say so distinctly and point at --resume.
var ErrRateLimited = errors.New("subscription rate limit reached")

// IsRateLimitText recognises the CLI's limit messages.
func IsRateLimitText(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "session limit") || strings.Contains(l, "usage limit") || strings.Contains(l, "rate limit") || strings.Contains(l, "rate_limit") || strings.Contains(l, "limit reached") || strings.Contains(l, "resets ")
}

// ConsumedUntrusted reports whether external content entered the model's
// context during this call (tool reads or attachments).
func (r Response) ConsumedUntrusted() bool {
	for _, e := range r.ToolEvents {
		if e.Allowed && e.Error == "" {
			return true
		}
	}
	return r.AttachmentsDelivered
}

// Availability describes whether a backend can be used right now, as shown by
// `water doctor`.
type Availability struct {
	Installed bool
	Authed    bool
	Metered   bool   // does using this bill per token?
	Detail    string // human-readable
}

// Usable reports whether the backend can service a request now.
func (a Availability) Usable() bool { return a.Installed && a.Authed }

// Backend is the model-call interface. Implementations must be safe for
// concurrent use: parallel graph nodes share one Backend.
type Backend interface {
	Name() string
	Available(ctx context.Context) Availability
	Run(ctx context.Context, req Request) (Response, error)
}

// Capabilities is optionally implemented by backends to report what they can
// carry (Investigation 2 results, per backend).
type Capabilities interface {
	SupportsAttachments() bool
	SupportsTools() bool
}

// Streamer is optionally implemented by backends that can stream a reply as
// it is generated. onDelta is called with each new chunk of assistant text,
// in order, before RunStream returns the final Response. A backend without
// streaming support is simply not a Streamer; callers fall back to Run.
type Streamer interface {
	RunStream(ctx context.Context, req Request, onDelta func(string)) (Response, error)
}
