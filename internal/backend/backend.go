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
	"time"
)

// Request is one model turn. System is the assembled persona context; Prompt is
// the actual turn. Role is for tracing/attribution only and must never affect
// which backend is used.
type Request struct {
	System  string
	Prompt  string
	Role    string
	Timeout time.Duration
}

// Response is the result of a Backend.Run. Raw holds unparsed output for
// debugging. Metered is TRUE if this call cost metered money.
type Response struct {
	Text         string
	Raw          string
	Metered      bool
	Backend      string
	InputTokens  int
	OutputTokens int
	Duration     time.Duration
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
