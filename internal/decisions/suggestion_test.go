package decisions

import (
	"strings"
	"testing"
)

// TestBuildActionSuggestionsSentenceMapping checks the per-function template
// table: a known function gets its own fixed sentence, an unmapped one
// (e.g. a future connector not yet added to suggestionTemplates) falls back
// to the generic sentence rather than failing or improvising from the
// payload.
func TestBuildActionSuggestionsSentenceMapping(t *testing.T) {
	cases := []struct {
		function string
		sentence string
	}{
		{"gmail.send_message", "Send an email"},
		{"gmail.draft_message", "Draft an email"},
		{"linear.set_issue_priority", "Change an issue's priority"},
		{"linear.create_comment", "Add a comment"},
		{"gcal.create_event", "Create a calendar event"},
		{"gcal.move_event", "Move a calendar event"},
		{"some_future_connector.do_thing", "Take this action"},
	}
	for _, tc := range cases {
		out := buildActionSuggestions("card-1", []StagedAction{{Function: tc.function, Actionable: true}})
		if len(out) != 1 || out[0].Sentence != tc.sentence {
			t.Errorf("%s: sentence = %q, want %q", tc.function, safeSentence(out), tc.sentence)
		}
	}
}

func safeSentence(out []Suggestion) string {
	if len(out) == 0 {
		return "<none>"
	}
	return out[0].Sentence
}

// TestActionSuggestionSentenceNeverContainsPayloadValues is the hard
// invariant docs/slices/UI.md Phase 1c asks for: a staged action's sentence
// must never interpolate a recipient, address or other payload value, even
// when the payload contains a real-looking email address. The sentence
// comes only from the per-function template, never from Payload.
func TestActionSuggestionSentenceNeverContainsPayloadValues(t *testing.T) {
	payload := map[string]any{
		"to":      "elena.park@meridian-example.com",
		"subject": "Renewal terms",
		"body":    "Hi Elena, following up on the extension...",
	}
	out := buildActionSuggestions("card-1", []StagedAction{{Function: "gmail.send_message", Payload: payload, Actionable: true}})
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}
	s := out[0]
	if strings.Contains(s.Sentence, "@") {
		t.Fatalf("Sentence = %q, must not contain an address", s.Sentence)
	}
	if strings.Contains(s.Sentence, "elena") || strings.Contains(strings.ToLower(s.Sentence), "meridian") {
		t.Fatalf("Sentence = %q, must not contain any payload value", s.Sentence)
	}
	if s.Sentence != "Send an email" {
		t.Fatalf("Sentence = %q, want the fixed template", s.Sentence)
	}
	// The payload itself is still carried on the suggestion (for Review),
	// just never folded into the sentence text.
	if s.Payload["to"] != "elena.park@meridian-example.com" {
		t.Fatal("Payload should still be carried on the suggestion for Review")
	}
}

func TestSuggestionIDIsDeterministicPerCardAndIndex(t *testing.T) {
	out := buildActionSuggestions("card-1", []StagedAction{
		{Function: "gmail.send_message", Actionable: true},
		{Function: "linear.set_issue_priority", Actionable: true},
	})
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	if out[0].ID == "" || out[1].ID == "" {
		t.Fatal("IDs must not be empty")
	}
	if out[0].ID == out[1].ID {
		t.Fatal("two actions on one card must have distinct suggestion ids (finding 20: same-function suggestions must be distinguishable)")
	}
	if !strings.HasPrefix(out[0].ID, "sugg-") {
		t.Fatalf("ID = %q, want the sugg- prefix matching cardID's card- convention", out[0].ID)
	}
	// Same card id and index must hash the same way every time (a rebuild
	// of the same card must not change its suggestions' identities).
	again := buildActionSuggestions("card-1", []StagedAction{{Function: "gmail.send_message", Actionable: true}})
	if again[0].ID != out[0].ID {
		t.Fatalf("suggestion id is not deterministic: %q vs %q", again[0].ID, out[0].ID)
	}
}

func TestBuildActionSuggestionsEmptyForNoStagedActions(t *testing.T) {
	if out := buildActionSuggestions("card-1", nil); out != nil {
		t.Fatalf("got %+v, want nil", out)
	}
}
