package gateway

import (
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
// with args: an email draft, a display, or nil for every other function.
// It is the one builder handleToolInvoke uses after a clean call.
func turnArtifact(fn string, args map[string]any) *runtime.Artifact {
	if fn == displayFunction {
		return displayArtifact(args)
	}
	return draftArtifact(fn, args)
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
