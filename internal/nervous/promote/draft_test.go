package promote

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"water/internal/backend"
	"water/internal/nervous/intents"
	"water/internal/nervous/slots"
)

var draftSpec = intents.FunctionSpec{
	ID:            "store.next_event",
	Args:          map[string]slots.Type{},
	Class:         intents.ClassLookup,
	ReadOnly:      true,
	Deterministic: true,
}

var draftCandidate = Candidate{
	ID:            "abc123def456",
	Signature:     "quick.next_event",
	Repeats:       7,
	Days:          3,
	SampleTurnIDs: []string{"t1", "t2"},
}

func TestDraftParsesFakeReplyAndStripsFencing(t *testing.T) {
	fk := backend.NewFake("fake")
	fk.Reply = func(req backend.Request) string {
		return "```yaml\n" +
			"id: whatever\n" +
			"description: next thing on the calendar\n" +
			"kind: read\n" +
			"function: store.next_event\n" +
			"reflex_eligible: true\n" +
			"escalate_if: [slot_unresolved, ambiguous_match]\n" +
			"templates:\n" +
			"  - \"what's up next\"\n" +
			"tests:\n" +
			"  - {utterance: \"what's up next\", intent: learned.placeholder}\n" +
			"  - {utterance: \"goodbye\", intent: \"none\"}\n" +
			"```"
	}

	out, err := Draft(context.Background(), fk, "haiku", draftSpec, draftCandidate, []string{"what's up next"})
	if err != nil {
		t.Fatalf("Draft: %v", err)
	}
	if fk.Calls() != 1 {
		t.Fatalf("Calls() = %d, want exactly 1 (one cold call)", fk.Calls())
	}
	reqs := fk.Requests()
	if len(reqs) != 1 {
		t.Fatalf("Requests() = %d, want 1", len(reqs))
	}
	if reqs[0].Model != "haiku" {
		t.Fatalf("Model = %q, want haiku", reqs[0].Model)
	}
	if !strings.Contains(reqs[0].Prompt, "store.next_event") {
		t.Fatalf("prompt does not mention the target function: %q", reqs[0].Prompt)
	}
	if !strings.Contains(reqs[0].Prompt, "what's up next") {
		t.Fatalf("prompt does not include the sample utterance: %q", reqs[0].Prompt)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output did not parse as YAML (fencing not stripped?): %v\n%s", err, out)
	}
	if doc["function"] != "store.next_event" {
		t.Fatalf("function = %v, want store.next_event (model output lost)", doc["function"])
	}
}

// TestDraftCodeSetsIdentityAndProvenance is the load-bearing test for
// Design §16 item 2's "code, not the model, fills in id/origin/
// provenance": a fake model that tries to claim its own id, origin: embedded
// and a fabricated provenance block must have every one of those three
// fields overwritten by Draft itself.
func TestDraftCodeSetsIdentityAndProvenance(t *testing.T) {
	fk := backend.NewFake("fake")
	fk.Reply = func(req backend.Request) string {
		return `
id: not_a_learned_id
origin: embedded
provenance: {candidate: "someone-elses-candidate", repeats: 999, draft_model: "not-the-real-model"}
description: next thing on the calendar
kind: read
function: store.next_event
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
templates:
  - "what's up next"
tests:
  - {utterance: "what's up next", intent: learned.placeholder}
  - {utterance: "goodbye", intent: "none"}
`
	}

	out, err := Draft(context.Background(), fk, "haiku", draftSpec, draftCandidate, nil)
	if err != nil {
		t.Fatalf("Draft: %v", err)
	}

	var doc struct {
		ID         string `yaml:"id"`
		Origin     string `yaml:"origin"`
		Provenance struct {
			Candidate  string   `yaml:"candidate"`
			TurnIDs    []string `yaml:"turn_ids"`
			Repeats    int      `yaml:"repeats"`
			DraftModel string   `yaml:"draft_model"`
		} `yaml:"provenance"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("re-parse: %v", err)
	}

	if !strings.HasPrefix(doc.ID, "learned.") {
		t.Fatalf("id = %q, want a learned.* id (code-derived from the candidate), not the model's claim", doc.ID)
	}
	if doc.Origin != "learned" {
		t.Fatalf("origin = %q, want \"learned\" regardless of the model's claim of %q", doc.Origin, "embedded")
	}
	if doc.Provenance.Candidate != draftCandidate.ID {
		t.Fatalf("provenance.candidate = %q, want the real candidate id %q, not the model's fabrication", doc.Provenance.Candidate, draftCandidate.ID)
	}
	if doc.Provenance.Repeats != draftCandidate.Repeats {
		t.Fatalf("provenance.repeats = %d, want the real candidate's %d, not the model's fabricated 999", doc.Provenance.Repeats, draftCandidate.Repeats)
	}
	if doc.Provenance.DraftModel != "haiku" {
		t.Fatalf("provenance.draft_model = %q, want the real model %q, not the model's own claim", doc.Provenance.DraftModel, "haiku")
	}
	if len(doc.Provenance.TurnIDs) != len(draftCandidate.SampleTurnIDs) {
		t.Fatalf("provenance.turn_ids = %v, want %v", doc.Provenance.TurnIDs, draftCandidate.SampleTurnIDs)
	}
}

func TestDraftRejectsNonYAMLReply(t *testing.T) {
	fk := backend.NewFake("fake")
	fk.Reply = func(req backend.Request) string { return "I can't help with that." }
	if _, err := Draft(context.Background(), fk, "haiku", draftSpec, draftCandidate, nil); err == nil {
		t.Fatal("Draft: want an error for a non-YAML reply, got nil")
	}
}

func TestDraftRequiresBackend(t *testing.T) {
	if _, err := Draft(context.Background(), nil, "haiku", draftSpec, draftCandidate, nil); err == nil {
		t.Fatal("Draft(nil backend): want an error, got nil")
	}
}

func TestStripFencingNoFence(t *testing.T) {
	if got := stripFencing("  id: x\n"); got != "id: x" {
		t.Fatalf("stripFencing(no fence) = %q", got)
	}
}

func TestSanitizeNameStable(t *testing.T) {
	a := sanitizeName("quick.latest_mail", "abcdef123456")
	b := sanitizeName("quick.latest_mail", "abcdef123456")
	if a != b {
		t.Fatalf("sanitizeName not stable: %q vs %q", a, b)
	}
	if strings.Contains(a, ".") || strings.Contains(a, "+") {
		t.Fatalf("sanitizeName(%q) = %q, contains characters the id regex forbids", "quick.latest_mail", a)
	}
}

func TestSpecForQuickToolFound(t *testing.T) {
	spec, ok := SpecForQuickTool("quick.next_event")
	if !ok {
		t.Fatal("SpecForQuickTool(quick.next_event): want ok, got false")
	}
	if spec.ID != "store.next_event" {
		t.Fatalf("spec.ID = %q, want store.next_event", spec.ID)
	}
}

func TestSpecForQuickToolNotFound(t *testing.T) {
	if _, ok := SpecForQuickTool("quick.not_a_real_tool"); ok {
		t.Fatal("SpecForQuickTool(unknown): want ok=false")
	}
}
