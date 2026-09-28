// Package display is the display connector: one level-R function,
// display.show, that the model calls to put something on the CEO's screen
// (a list, figures, a draft, steps) while it answers out loud. It has no
// network, no credential and no side effect beyond the UI: the call only
// validates its title and body and acknowledges; the daemon then sends the
// active turn's client an artifact event {type: "display", title, body}
// (internal/gateway/artifact.go), only after the call ran cleanly.
package display

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

// Limits on display.show's arguments, in characters (runes).
const (
	MaxTitle = 120
	MaxBody  = 4000
)

// Connector implements connectors.Connector for display.
type Connector struct{}

// New builds the display connector.
func New() *Connector { return &Connector{} }

func (*Connector) Name() string                 { return "display" }
func (*Connector) Credential() (string, string) { return "", "" }

func (*Connector) Functions() []connectors.Function {
	return []connectors.Function{{
		Name: "show",
		Description: fmt.Sprintf("Show the CEO something on screen, briefly, as plain text: a title (at most %d characters) "+
			"and a body (at most %d characters) such as a list, figures, a draft or steps. Use it when seeing it helps "+
			"more than hearing it; still answer briefly out loud. It sends and changes nothing.", MaxTitle, MaxBody),
		Level: twins.R, Risk: connectors.RiskLow,
		Activity: "Showing you this",
		Schema: connectors.Schema{
			Properties: map[string]connectors.Property{
				"title": {Type: "string", Description: fmt.Sprintf("a short heading, at most %d characters", MaxTitle)},
				"body":  {Type: "string", Description: fmt.Sprintf("plain text to show, at most %d characters; newlines allowed", MaxBody)},
			},
			Required: []string{"title", "body"},
		},
	}}
}

// Content reads display.show's arguments: title and body must each be a
// string that is non-blank once trimmed and within MaxTitle / MaxBody
// characters. It returns them trimmed.
func Content(args map[string]any) (title, body string, err error) {
	title, err = field(args, "title", MaxTitle)
	if err != nil {
		return "", "", err
	}
	body, err = field(args, "body", MaxBody)
	if err != nil {
		return "", "", err
	}
	return title, body, nil
}

func field(args map[string]any, name string, max int) (string, error) {
	v, ok := args[name]
	if !ok {
		return "", fmt.Errorf("display: missing %s", name)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("display: %s must be a string", name)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("display: %s is empty", name)
	}
	if n := utf8.RuneCountInString(s); n > max {
		return "", fmt.Errorf("display: %s is %d characters, at most %d allowed", name, n, max)
	}
	return s, nil
}

// Invoke validates the call's title and body and acknowledges it. Nothing
// else happens here; the daemon shows the content to the active turn.
func (*Connector) Invoke(_ context.Context, p permit.Permit) (json.RawMessage, error) {
	call, err := p.Open()
	if err != nil {
		return nil, err
	}
	if call.Function != "show" {
		return nil, fmt.Errorf("display: unknown function %q", call.Function)
	}
	if _, _, err := Content(call.Args); err != nil {
		return nil, err
	}
	return json.RawMessage(`{"shown":true}`), nil
}

// Normalize returns no records: shown text is the model's own output for
// the CEO's eyes, not data to index.
func (*Connector) Normalize(string, json.RawMessage) ([]store.Record, error) { return nil, nil }

var _ connectors.Connector = (*Connector)(nil)
