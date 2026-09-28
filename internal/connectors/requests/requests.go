// Package requests is the internal, non-outward connector U15 (docs/slices/
// UI.md, "Owner decisions needed before building") approved for person
// requests: one level-A function, requests.respond, that records the CEO's
// answer to something a person asked for (a budget reallocation, a
// signature, ...) once the CEO approves it. It has no network, no
// credential, and no side effect beyond validating and acknowledging its
// arguments -- exactly like internal/connectors/display, whose structure
// this mirrors.
//
// The record of "what was asked and what the CEO answered" is the approval
// envelope itself: approvals.Queue.ProposeRequest builds it with Payload
// already holding {request_id, answer, note}, so by the time this Invoke
// ever runs (only after the CEO approved that exact payload), there is
// nothing left to write anywhere else -- the approval row (its payload,
// status, decided_at) and the gate's own audit trail (KindExecute) are
// already the durable record. Invoke's only job is to validate the shape of
// what it's being asked to "send" (which is really just an acknowledgement)
// and confirm it ran, the same division of labour display.show has between
// validating/acking here and the daemon doing something with the result
// elsewhere.
//
// The CEO stays the only path to executing this: like every level-A
// function, requests.respond runs only through Gate.Invoke against an
// approved envelope (gate.NeedsEnvelope), so "recording the answer" can
// never happen without the CEO having approved that exact answer text first.
package requests

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"water/internal/connectors"
	"water/internal/gate/permit"
	"water/internal/store"
	"water/internal/twins"
)

// Limits on requests.respond's arguments, in characters (runes).
const (
	MaxAnswer = 4000
	MaxNote   = 2000
)

// Connector implements connectors.Connector for requests.
type Connector struct{}

// New builds the requests connector.
func New() *Connector { return &Connector{} }

func (*Connector) Name() string                 { return "requests" }
func (*Connector) Credential() (string, string) { return "", "" }

func (*Connector) Functions() []connectors.Function {
	return []connectors.Function{{
		Name: "respond",
		Description: fmt.Sprintf("Record the CEO's answer to a person's request once approved (a budget reallocation ask, "+
			"a signature ask, and similar). It sends nothing and calls nothing outward: the answer is the approval itself, "+
			"already reviewed and approved word for word. request_id names the request being answered; answer is the "+
			"decision text (at most %d characters); note is an optional aside for the record (at most %d characters).",
			MaxAnswer, MaxNote),
		Level: twins.A, Risk: connectors.RiskLow,
		Activity: "Recording your answer",
		Schema: connectors.Schema{
			Properties: map[string]connectors.Property{
				"request_id": {Type: "string", Description: "the id of the request being answered"},
				"answer":     {Type: "string", Description: fmt.Sprintf("the decision text, at most %d characters", MaxAnswer)},
				"note":       {Type: "string", Description: fmt.Sprintf("optional, at most %d characters", MaxNote)},
			},
			Required: []string{"request_id", "answer"},
		},
	}}
}

// respondOutput is requests.respond's JSON output: an acknowledgement that
// echoes what was recorded, mirroring gmail's writeMessageOutput convention
// of echoing the content actually acted on rather than an opaque ack.
type respondOutput struct {
	Recorded  bool   `json:"recorded"`
	RequestID string `json:"request_id"`
	Answer    string `json:"answer"`
	Note      string `json:"note,omitempty"`
}

// Content reads requests.respond's arguments: request_id and answer must
// each be a non-blank string within their limits; note is optional and,
// when present, must be within its own limit. It returns all three trimmed.
func Content(args map[string]any) (requestID, answer, note string, err error) {
	requestID, err = field(args, "request_id", 0, true)
	if err != nil {
		return "", "", "", err
	}
	answer, err = field(args, "answer", MaxAnswer, true)
	if err != nil {
		return "", "", "", err
	}
	if v, ok := args["note"]; ok {
		if _, isString := v.(string); !isString {
			return "", "", "", fmt.Errorf("requests: note must be a string")
		}
		note, err = field(args, "note", MaxNote, false)
		if err != nil {
			return "", "", "", err
		}
	}
	return requestID, answer, note, nil
}

func field(args map[string]any, name string, max int, required bool) (string, error) {
	v, ok := args[name]
	if !ok {
		if required {
			return "", fmt.Errorf("requests: missing %s", name)
		}
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("requests: %s must be a string", name)
	}
	s = strings.TrimSpace(s)
	if required && s == "" {
		return "", fmt.Errorf("requests: %s is empty", name)
	}
	if max > 0 {
		if n := utf8.RuneCountInString(s); n > max {
			return "", fmt.Errorf("requests: %s is %d characters, at most %d allowed", name, n, max)
		}
	}
	return s, nil
}

// Invoke validates requests.respond's arguments and acknowledges them. It
// does nothing else: no send, no external call, no store write of its own
// (see the package doc for why the approval row is already the record).
func (*Connector) Invoke(_ context.Context, p permit.Permit) (json.RawMessage, error) {
	call, err := p.Open()
	if err != nil {
		return nil, err
	}
	if call.Function != "respond" {
		return nil, fmt.Errorf("requests: unknown function %q", call.Function)
	}
	requestID, answer, note, err := Content(call.Args)
	if err != nil {
		return nil, err
	}
	return json.Marshal(respondOutput{Recorded: true, RequestID: requestID, Answer: answer, Note: note})
}

// Normalize returns no records: the approval row already holds the durable
// record (see the package doc), and there is nothing else here to index.
func (*Connector) Normalize(string, json.RawMessage) ([]store.Record, error) { return nil, nil }

var _ connectors.Connector = (*Connector)(nil)
