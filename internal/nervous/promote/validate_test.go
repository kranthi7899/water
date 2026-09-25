package promote

import (
	"strings"
	"testing"
	"testing/fstest"

	"water"
	"water/internal/nervous/eval"
	"water/internal/nervous/intents"
	"water/internal/nervous/propose"
	"water/internal/nervous/reflex"
	"water/internal/twins"
)

func mapFSOf(name, content string) fstest.MapFS {
	return fstest.MapFS{name: {Data: []byte(content)}}
}

// loadCEORegistry loads the real, as-shipped ceo twin registry, the same
// way internal/cli/twin.go's buildTwinDepsFS and
// internal/nervous/intents/embedded_test.go's loadEmbeddedCEO do — real
// data beats a bespoke fixture here, since ValidateLearned's job is
// specifically to reason about ambiguity against the twin's ACTUAL
// templates and eval negatives.
func loadCEORegistry(t *testing.T) *intents.Registry {
	t.Helper()
	m, err := twins.Load(water.TwinsFS(), "ceo")
	if err != nil {
		t.Fatalf("twins.Load(ceo): %v", err)
	}
	reg, err := intents.LoadRegistry(water.TwinsFS(), m, intents.Functions{Read: reflex.Specs(), Write: propose.Specs()}, intents.LoadOptions{})
	if err != nil {
		t.Fatalf("LoadRegistry(ceo): %v", err)
	}
	return reg
}

const validDraftYAML = `
id: whatever-the-model-called-it
description: whether I've heard from priya recently
kind: read
function: store.latest_from
slots:
  who: {type: person, required: true}
templates:
  - "have i heard from {who}"
  - "did {who} reach out to me"
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
origin: learned
tests:
  - {utterance: "have i heard from priya", intent: learned.mail_from_did, slots: {who: "priya nair <priya@example.com>"}}
  - {utterance: "what's the weather like", intent: "none"}
`

func mustSetID(t *testing.T, yamlSrc, id string) []byte {
	t.Helper()
	return []byte(strings.Replace(yamlSrc, "id: whatever-the-model-called-it", "id: "+id, 1))
}

func TestValidateLearnedAcceptsAValidDraft(t *testing.T) {
	reg := loadCEORegistry(t)
	file := mustSetID(t, validDraftYAML, "learned.mail_from_did")
	if err := ValidateLearned(file, reg, eval.Negatives(), DefaultMaxLearned); err != nil {
		t.Fatalf("ValidateLearned rejected a valid draft: %v", err)
	}
}

// TestLearnedCannotReferenceIneligible covers control, approvals, status,
// text-slot and write targets: every one of them must be rejected, whatever
// specific rule catches it (Learnable(), the read/text-slot rule, or the
// kind: read requirement).
func TestLearnedCannotReferenceIneligible(t *testing.T) {
	reg := loadCEORegistry(t)
	negs := eval.Negatives()

	cases := map[string]string{
		"control (control.cancel_tasks, has side effects)": `
id: learned.stop_it
description: stop background tasks
kind: read
function: control.cancel_tasks
templates: ["please halt everything"]
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
origin: learned
tests:
  - {utterance: "please halt everything", intent: learned.stop_it}
  - {utterance: "goodbye", intent: "none"}
`,
		"approvals (approvals.bind_pending, has side effects)": `
id: learned.approve_it
description: decide the one pending approval
kind: read
function: approvals.bind_pending
templates: ["go ahead with it"]
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
origin: learned
tests:
  - {utterance: "go ahead with it", intent: learned.approve_it}
  - {utterance: "goodbye", intent: "none"}
`,
		"status (status.overview, not Deterministic)": `
id: learned.status_check
description: a status overview
kind: read
function: status.overview
templates: ["give me the full rundown"]
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
origin: learned
tests:
  - {utterance: "give me the full rundown", intent: learned.status_check}
  - {utterance: "goodbye", intent: "none"}
`,
		"text slot (read intents may never declare one)": `
id: learned.text_slot
description: an intent with a forbidden text slot
kind: read
function: store.next_event
slots:
  note: {type: text}
templates: ["remember {note}"]
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
origin: learned
tests:
  - {utterance: "remember call bob back", intent: learned.text_slot}
  - {utterance: "goodbye", intent: "none"}
`,
		"write target (kind: write is always rejected)": `
id: learned.write_it
description: create a calendar event
kind: write
action: gcal.create_event
proposer: calendar.create
slots:
  when: {type: date, required: true}
  at: {type: time, required: true}
templates: ["book something {when} at {at}"]
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
origin: learned
tests:
  - {utterance: "book something tomorrow at 3pm", intent: learned.write_it, slots: {when: tomorrow, at: "3pm"}}
  - {utterance: "goodbye", intent: "none"}
`,
	}

	for name, y := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateLearned([]byte(y), reg, negs, DefaultMaxLearned); err == nil {
				t.Fatalf("ValidateLearned accepted an ineligible target (%s), want a rejection", name)
			}
		})
	}
}

// TestLearnedNoAmbiguity: a draft whose template overlaps an embedded
// intent's own test utterance (schedule.on_date's real, shipped
// "what's on [my] (calendar|schedule)" template, reused verbatim) is
// rejected — the utterance ties between the embedded intent and the draft.
func TestLearnedNoAmbiguity(t *testing.T) {
	reg := loadCEORegistry(t)
	y := `
id: learned.overlap_with_on_date
description: overlaps schedule.on_date's own template
kind: read
function: store.next_event
templates:
  - "what's on [my] (calendar|schedule)"
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
origin: learned
tests:
  - {utterance: "what's on my calendar", intent: learned.overlap_with_on_date}
  - {utterance: "some other unrelated phrase entirely", intent: "none"}
`
	err := ValidateLearned([]byte(y), reg, eval.Negatives(), DefaultMaxLearned)
	if err == nil {
		t.Fatal("ValidateLearned accepted a draft that overlaps schedule.on_date's own template, want a rejection")
	}
	if !strings.Contains(err.Error(), "ambiguous") && !strings.Contains(err.Error(), "ties") {
		t.Fatalf("error = %v, want it to mention ambiguity/ties", err)
	}
}

// TestLearnedNoFalseAccepts: a draft whose template matches one of the
// embedded eval's class: reasoning negatives verbatim is rejected.
func TestLearnedNoFalseAccepts(t *testing.T) {
	reg := loadCEORegistry(t)
	y := `
id: learned.inbox_recap
description: a recap of the inbox
kind: read
function: store.latest_messages
slots:
  n: {type: count, default: "5"}
templates:
  - "which of these emails is more urgent"
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
origin: learned
tests:
  - {utterance: "which of these emails is more urgent", intent: learned.inbox_recap}
  - {utterance: "some other unrelated phrase entirely", intent: "none"}
`
	negs := eval.Negatives()
	foundReasoningCase := false
	for _, n := range negs {
		if n.Utterance == "which of these emails is more urgent" {
			foundReasoningCase = true
		}
	}
	if !foundReasoningCase {
		t.Fatal("test setup: \"which of these emails is more urgent\" is not in eval.Negatives(); the eval set may have changed")
	}

	err := ValidateLearned([]byte(y), reg, negs, DefaultMaxLearned)
	if err == nil {
		t.Fatal("ValidateLearned accepted a draft matching a reasoning negative, want a rejection")
	}
	if !strings.Contains(err.Error(), "false-accept") {
		t.Fatalf("error = %v, want it to mention false-accept", err)
	}
}

func TestLearnedRejectsDenyWordLiteral(t *testing.T) {
	reg := loadCEORegistry(t)
	y := `
id: learned.cancel_stuff
description: uses a deny word as a literal
kind: read
function: store.next_event
templates:
  - "cancel my next thing please"
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
origin: learned
tests:
  - {utterance: "cancel my next thing please", intent: learned.cancel_stuff}
  - {utterance: "goodbye", intent: "none"}
`
	if err := ValidateLearned([]byte(y), reg, eval.Negatives(), DefaultMaxLearned); err == nil {
		t.Fatal("ValidateLearned accepted a template with a deny-word literal, want a rejection")
	}
}

func TestLearnedIDMustHavePrefixAndNotCollide(t *testing.T) {
	reg := loadCEORegistry(t)

	t.Run("missing learned. prefix", func(t *testing.T) {
		y := strings.Replace(validDraftYAML, "id: whatever-the-model-called-it", "id: not_learned.mail_from_did", 1)
		if err := ValidateLearned([]byte(y), reg, eval.Negatives(), DefaultMaxLearned); err == nil {
			t.Fatal("ValidateLearned accepted an id without the learned. prefix")
		}
	})

	t.Run("collides with an embedded id", func(t *testing.T) {
		y := strings.Replace(validDraftYAML, "id: whatever-the-model-called-it", "id: schedule.on_date", 1)
		if err := ValidateLearned([]byte(y), reg, eval.Negatives(), DefaultMaxLearned); err == nil {
			t.Fatal("ValidateLearned accepted an id colliding with an embedded intent")
		}
	})
}

func TestLearnedCapEnforced(t *testing.T) {
	m, err := twins.Load(water.TwinsFS(), "ceo")
	if err != nil {
		t.Fatalf("twins.Load(ceo): %v", err)
	}
	learnedAlready := `
id: learned.already_active
description: an already-active learned intent
kind: read
function: store.next_event
templates: ["what's coming up right after this"]
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
origin: learned
tests:
  - {utterance: "what's coming up right after this", intent: learned.already_active}
  - {utterance: "goodbye", intent: "none"}
`
	reg, err := intents.LoadRegistry(water.TwinsFS(), m,
		intents.Functions{Read: reflex.Specs(), Write: propose.Specs()},
		intents.LoadOptions{Learned: mapFSOf("already.yaml", learnedAlready)},
	)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if _, ok := reg.Lookup("learned.already_active"); !ok {
		t.Fatal("test setup: learned.already_active did not load")
	}

	file := mustSetID(t, validDraftYAML, "learned.mail_from_did")
	if err := ValidateLearned(file, reg, eval.Negatives(), 1); err == nil {
		t.Fatal("ValidateLearned accepted a second learned intent with maxLearned=1 and one already active")
	}
	if err := ValidateLearned(file, reg, eval.Negatives(), 2); err != nil {
		t.Fatalf("ValidateLearned rejected a second learned intent with maxLearned=2: %v", err)
	}
}
