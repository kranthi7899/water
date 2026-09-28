package gateway

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"water/internal/connectors/display"
	"water/internal/runtime"
)

// Caps on an artifact event's contents: every string is cut to at most
// artifactMaxBytes bytes (on a UTF-8 boundary) and every list to at most
// artifactMaxItems entries, so one draft can never flood a turn stream.
const (
	artifactMaxBytes = 20000
	artifactMaxItems = 50
)

// displayFunction is the display connector's one function: what the model
// asks to put on the CEO's screen during a turn (a list, figures, steps),
// shown to the client as a display artifact event.
const displayFunction = "display.show"

// turnArtifact is the artifact event payload for a successful call to fn
// with args, whose connector returned output: an email draft, a display, a
// note, or nil for every other function. It is the one builder
// handleToolInvoke uses after a clean call.
//
// A note is checked before the draft fallback and independently of fn: any
// connector's result may flag itself relay: true (docs/slices/UI.md Phase
// 6, U1-A), not only a specific function's, so a future connector can reuse
// the same convention without a change here.
func turnArtifact(fn string, args map[string]any, output json.RawMessage) *runtime.Artifact {
	if fn == displayFunction {
		return displayArtifact(args)
	}
	if a := noteArtifact(output); a != nil {
		return a
	}
	return draftArtifact(fn, args)
}

// relayedCommentPrefix is the code-added prefix a relayed comment's body
// must start with (docs/slices/UI.md U4: "a relayed comment must start
// with a code-added '(simulated) Relayed:' prefix, enforced in the
// connector, not the prompt"). noteArtifact derives Simulated from this
// prefix directly, rather than trusting a connector-reported field, so a
// connector bug can never under- or over-report the badge (the "gaps are
// never hidden" posture docs/slices/UI.md §4 invariant 8 already keeps for
// the decision card's own simulated signals).
//
// linear.create_comment (internal/connectors/linear, U4, 2026-09-28) is now
// the first real producer: every successful comment it posts sets
// relay: true (worth surfacing as a note, same as a drafted email already
// is), and Simulated only lights up for the specific call that set
// simulated_relay: true, since that's the only path whose body carries this
// exact prefix. It has its own identical copy of this string (there is no
// shared import between the two packages) -- see that package's own doc
// comment on why they must stay in sync.
const relayedCommentPrefix = "(simulated) Relayed:"

// noteArtifact builds a note artifact from a tool result's own JSON output,
// only when that result explicitly flags itself relay: true. Absent that
// flag, an unparseable output, or a missing/blank title or body, this
// returns nil: an ordinary tool result is never shown as a note.
func noteArtifact(output json.RawMessage) *runtime.Artifact {
	if len(output) == 0 {
		return nil
	}
	var v struct {
		Relay  bool   `json:"relay"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(output, &v); err != nil || !v.Relay {
		return nil
	}
	title, body := capRunes(strings.TrimSpace(v.Title), display.MaxTitle), capBytes(strings.TrimSpace(v.Body), artifactMaxBytes)
	if title == "" || body == "" {
		return nil
	}
	return &runtime.Artifact{
		Type:      runtime.ArtifactNote,
		Title:     title,
		Body:      body,
		Source:    capBytes(strings.TrimSpace(v.Source), artifactMaxBytes),
		Simulated: strings.HasPrefix(strings.TrimSpace(v.Body), relayedCommentPrefix),
	}
}

// displayArtifact builds a display artifact from display.show's own
// arguments: title and body trimmed and capped to display.MaxTitle /
// display.MaxBody characters (the connector already refused anything
// larger; the caps here keep a stray path from ever flooding a stream).
// A missing, blank or non-string title or body yields nil.
func displayArtifact(args map[string]any) *runtime.Artifact {
	title, _ := args["title"].(string)
	body, _ := args["body"].(string)
	title, body = capRunes(strings.TrimSpace(title), display.MaxTitle), capRunes(strings.TrimSpace(body), display.MaxBody)
	if title == "" || body == "" {
		return nil
	}
	return &runtime.Artifact{Type: runtime.ArtifactDisplay, Title: title, Body: body}
}

// capRunes cuts s to at most n runes.
func capRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}

// draftArtifactFunctions are the level-D draft functions whose successful
// execution during a turn is shown to the client as an artifact event.
var draftArtifactFunctions = map[string]bool{
	"gmail.draft_message":    true,
	"gmail.draft_for_review": true,
}

// draftArtifact builds the artifact event payload for a successful call to
// fn with args, or nil when fn is not a draft function this daemon shows.
// The values are the call's own arguments (to, cc, subject, body), read
// defensively: to and cc may be one string or a list, anything of another
// type is dropped, and every value is capped.
func draftArtifact(fn string, args map[string]any) *runtime.Artifact {
	if !draftArtifactFunctions[fn] {
		return nil
	}
	return &runtime.Artifact{
		Type:    runtime.ArtifactEmailDraft,
		To:      artifactStrings(args["to"]),
		Cc:      artifactStrings(args["cc"]),
		Subject: artifactString(args["subject"]),
		Body:    artifactString(args["body"]),
	}
}

// artifactString is v capped to artifactMaxBytes when it is a string, and
// "" otherwise.
func artifactString(v any) string {
	s, _ := v.(string)
	return capBytes(s, artifactMaxBytes)
}

// artifactStrings reads v as one string or a list of strings, skipping
// empty and non-string entries, capping each entry and the list.
func artifactStrings(v any) []string {
	var out []string
	add := func(x any) {
		if s, ok := x.(string); ok && s != "" && len(out) < artifactMaxItems {
			out = append(out, capBytes(s, artifactMaxBytes))
		}
	}
	switch t := v.(type) {
	case string:
		add(t)
	case []any:
		for _, x := range t {
			add(x)
		}
	case []string:
		for _, x := range t {
			add(x)
		}
	}
	return out
}

// capBytes cuts s to at most n bytes without splitting a UTF-8 sequence.
func capBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	// A cut rune leaves at most UTFMax-1 bytes of an incomplete sequence.
	for i := 0; i < utf8.UTFMax-1 && len(s) > 0; i++ {
		if r, size := utf8.DecodeLastRuneInString(s); r != utf8.RuneError || size != 1 {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}
