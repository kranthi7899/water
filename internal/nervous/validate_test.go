package nervous

import (
	"testing"
	"testing/fstest"
	"time"

	"water/internal/nervous/intents"
	"water/internal/nervous/slots"
	"water/internal/nervous/tmpl"
	"water/internal/twins"
)

const validateManifestYAML = `
id: testtwin
name: Test twin
usage: {window: 5h, model_calls: 200, auto_model_calls: 40}
models: {fast: haiku, strong: ""}
connectors:
  - name: gcal
    functions:
      - {name: list_events, level: R}
`

const validateSharedYAML = `
rules: {}
skip_words: [please, hey]
deny_words: [cancel]
escalate_words: [should]
clause_joiners: ["and then"]
corrections: ["that's wrong"]
`

const validateScheduleYAML = `
id: schedule.on_date
description: Events on a given day
function: store.calendar_events
slots:
  when: {type: daterange, default: today}
templates:
  - "(what's|what is) on [my] (calendar|schedule) [for] {when}"
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
tests:
  - {utterance: "what's on tomorrow", intent: schedule.on_date, slots: {when: tomorrow}}
  - {utterance: "cancel my 3pm meeting", intent: "none"}
`

const validateLatestFromYAML = `
id: mail.latest_from
description: Latest message from someone
function: store.latest_from
slots:
  who: {type: person, required: true}
templates:
  - "latest [message] from {who}"
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
tests:
  - {utterance: "latest from alex chen", intent: mail.latest_from, slots: {who: "alex chen <alex.chen@x.com>"}}
  - {utterance: "cancel my 3pm meeting", intent: "none"}
`

func validateFixtureRegistry(t *testing.T) *intents.Registry {
	t.Helper()
	m, err := twins.Parse([]byte(validateManifestYAML))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	fns := intents.Functions{
		Read: map[string]intents.FunctionSpec{
			"store.calendar_events": {
				ID: "store.calendar_events", Args: map[string]slots.Type{"when": slots.TypeDateRange},
				ReadOnly: true, Deterministic: true, Class: intents.ClassLookup,
			},
			"store.latest_from": {
				ID: "store.latest_from", Args: map[string]slots.Type{"who": slots.TypePerson}, Required: []string{"who"},
				ReadOnly: true, Deterministic: true, Class: intents.ClassLookup,
			},
		},
	}
	fsys := fstest.MapFS{
		"twins/testtwin/intents/_shared.yaml":          {Data: []byte(validateSharedYAML)},
		"twins/testtwin/intents/schedule_on_date.yaml": {Data: []byte(validateScheduleYAML)},
		"twins/testtwin/intents/mail_latest_from.yaml": {Data: []byte(validateLatestFromYAML)},
	}
	reg, err := intents.LoadRegistry(fsys, m, fns, intents.LoadOptions{})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	return reg
}

func validateFixtureEnts() slots.Entities {
	return slots.Entities{People: []slots.Person{
		{Name: "Alex Chen", Email: "alex.chen@x.com"},
		{Name: "Alex Rivera", Email: "alex.rivera@x.com"},
		{Name: "Jordan Lee", Email: "jordan@x.com"},
	}}
}

var validateFixedNow = time.Date(2026, 9, 24, 9, 0, 0, 0, time.FixedZone("PT", -7*3600))

func TestValidateNoMatch(t *testing.T) {
	reg := validateFixtureRegistry(t)
	_, reason, ok := Validate(reg, Proposal{Intent: "no.such.intent"}, validateFixedNow, validateFixtureEnts())
	if ok || reason != "no_match" {
		t.Fatalf("got ok=%v reason=%q, want ok=false reason=no_match", ok, reason)
	}
}

func TestValidateUndeclaredSlotCapture(t *testing.T) {
	reg := validateFixtureRegistry(t)
	p := Proposal{Intent: "schedule.on_date", Captures: []tmpl.Capture{{Slot: "bogus_slot", Tokens: []string{"x"}, Raw: "x"}}}
	_, reason, ok := Validate(reg, p, validateFixedNow, validateFixtureEnts())
	if ok || reason != "slot_unresolved" {
		t.Fatalf("got ok=%v reason=%q, want ok=false reason=slot_unresolved", ok, reason)
	}
}

func TestValidateUnresolvedSlot(t *testing.T) {
	reg := validateFixtureRegistry(t)
	p := Proposal{Intent: "mail.latest_from", Captures: []tmpl.Capture{{Slot: "who", Tokens: []string{"nosuchperson"}, Raw: "nosuchperson"}}}
	_, reason, ok := Validate(reg, p, validateFixedNow, validateFixtureEnts())
	if ok || reason != "slot_unresolved" {
		t.Fatalf("got ok=%v reason=%q, want ok=false reason=slot_unresolved (unresolved person)", ok, reason)
	}
}

func TestValidateAmbiguousSlot(t *testing.T) {
	reg := validateFixtureRegistry(t)
	p := Proposal{Intent: "mail.latest_from", Captures: []tmpl.Capture{{Slot: "who", Tokens: []string{"alex"}, Raw: "alex"}}}
	_, reason, ok := Validate(reg, p, validateFixedNow, validateFixtureEnts())
	if ok || reason != "slot_unresolved" {
		t.Fatalf("got ok=%v reason=%q, want ok=false reason=slot_unresolved (ambiguous person)", ok, reason)
	}
}

func TestValidateRequiredMissingNoDefault(t *testing.T) {
	reg := validateFixtureRegistry(t)
	p := Proposal{Intent: "mail.latest_from"} // "who" required, no default, not captured
	_, reason, ok := Validate(reg, p, validateFixedNow, validateFixtureEnts())
	if ok || reason != "slot_unresolved" {
		t.Fatalf("got ok=%v reason=%q, want ok=false reason=slot_unresolved (missing required)", ok, reason)
	}
}

func TestValidateSuccessWithDefault(t *testing.T) {
	reg := validateFixtureRegistry(t)
	p := Proposal{Intent: "schedule.on_date"} // "when" uncaptured, has default "today"
	v, reason, ok := Validate(reg, p, validateFixedNow, validateFixtureEnts())
	if !ok || reason != "" {
		t.Fatalf("got ok=%v reason=%q, want ok=true", ok, reason)
	}
	if v.Labels["when"] != "today" {
		t.Fatalf("Labels[when] = %q, want today", v.Labels["when"])
	}
	if v.Intent.ID != "schedule.on_date" {
		t.Fatalf("Intent.ID = %q", v.Intent.ID)
	}
}

func TestValidateSuccessWithCapture(t *testing.T) {
	reg := validateFixtureRegistry(t)
	p := Proposal{Intent: "schedule.on_date", Captures: []tmpl.Capture{{Slot: "when", Tokens: []string{"tomorrow"}, Raw: "tomorrow"}}}
	v, reason, ok := Validate(reg, p, validateFixedNow, validateFixtureEnts())
	if !ok || reason != "" {
		t.Fatalf("got ok=%v reason=%q, want ok=true", ok, reason)
	}
	if v.Labels["when"] != "tomorrow" {
		t.Fatalf("Labels[when] = %q, want tomorrow", v.Labels["when"])
	}
	if v.Spoken["when"] == "" {
		t.Fatalf("Spoken[when] is empty, want a header-echo string")
	}
}

func TestValidateResolvedPersonCapture(t *testing.T) {
	reg := validateFixtureRegistry(t)
	p := Proposal{Intent: "mail.latest_from", Captures: []tmpl.Capture{{Slot: "who", Tokens: []string{"jordan"}, Raw: "jordan"}}}
	v, reason, ok := Validate(reg, p, validateFixedNow, validateFixtureEnts())
	if !ok || reason != "" {
		t.Fatalf("got ok=%v reason=%q, want ok=true (unique surname/first-name match)", ok, reason)
	}
	if v.Args["who"].Person.Email != "jordan@x.com" {
		t.Fatalf("Args[who].Person.Email = %q, want jordan@x.com", v.Args["who"].Person.Email)
	}
}
