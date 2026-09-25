// Package decider defines a small seam for a future model-backed decision
// helper — something that can choose among options, score an item, or give
// a yes/no probability — without committing to one now. Water ships only
// Null: it always declines. Nothing outward-facing depends on this package
// making a real choice; see classifier.go for the one place it is wired in
// today, as a documented no-op.
package decider

import (
	"context"
	"errors"
	"fmt"
)

// Kind says what shape of answer a Request wants.
type Kind string

const (
	Choice           Kind = "choice"
	Score            Kind = "score"
	YesNoProbability Kind = "yes_no_probability"
)

// Request is one question for a Decider: State is whatever text describes
// the thing being decided, Options is the closed set of valid choices for
// Kind Choice (ignored otherwise).
type Request struct {
	Kind    Kind
	State   string
	Options []string
}

// Answer is a Decider's reply. Only the field matching the Request's Kind
// is meaningful: Choice for KindChoice, Score for KindScore, Probability
// for KindYesNoProbability. Confidence is always optional, informational
// context regardless of Kind.
type Answer struct {
	Choice      string
	Score       float64
	Probability float64
	Confidence  float64
}

// ErrUnavailable is returned by a Decider that has nothing to say for a
// Request — the only error Null ever returns, and the one every caller of
// Decide must treat as "fall back", not "fail".
var ErrUnavailable = errors.New("decider: no provider configured")

// Decider answers a Request. Implementations must be safe for concurrent
// use, since a decider can be called from more than one classification at
// once.
type Decider interface {
	Decide(ctx context.Context, r Request) (Answer, error)
}

// Null is the only Decider Water ships today. It always declines, so any
// caller must already know how to fall back to its own default behavior —
// exactly the posture Wrap (classifier.go) is built around.
type Null struct{}

// Decide implements Decider.
func (Null) Decide(ctx context.Context, r Request) (Answer, error) {
	return Answer{}, ErrUnavailable
}

// New builds the Decider named by provider. Only "none" (Null) is
// supported today; any other value is an error naming the unsupported
// provider, never a panic and never a silent fallback to Null.
func New(provider string) (Decider, error) {
	if provider == "none" {
		return Null{}, nil
	}
	return nil, fmt.Errorf("decider: unsupported provider %q", provider)
}
