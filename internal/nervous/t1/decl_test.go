package t1

import (
	"sort"
	"testing"
	"testing/fstest"

	"water/internal/nervous/intents"
	"water/internal/nervous/slots"
	"water/internal/twins"
)

const declManifestYAML = `
id: testtwin
name: Test twin
usage: {window: 5h, model_calls: 200, auto_model_calls: 40}
connectors:
  - name: gcal
    functions:
      - {name: list_events, level: R}
      - {name: create_event, level: A}
`

const declSharedYAML = `
rules: {}
skip_words: [please, hey]
deny_words: [cancel]
escalate_words: [should]
clause_joiners: ["and then"]
`

const declReadEligibleYAML = `
id: schedule.on_date
description: Events on a given day
function: store.calendar_events
slots:
  when: {type: daterange, default: today}
templates:
  - "what's on {when}"
escalate_if: [slot_unresolved, ambiguous_match]
reflex_eligible: true
tests:
  - {utterance: "what's on tomorrow", intent: schedule.on_date, slots: {when: tomorrow}}
  - {utterance: "gibberish", intent: "none"}
`

const declReadNotEligibleYAML = `
id: status.overview
description: A quick status snapshot
function: status.overview
templates:
  - "status check"
tests:
  - {utterance: "status check", intent: status.overview}
  - {utterance: "gibberish", intent: "none"}
`

const declPendingYAML = `
id: approvals.respond
description: Approve or deny the one pending request
function: approvals.bind_pending
requires_pending_approval: true
escalate_if: [slot_unresolved, ambiguous_match]
reflex_eligible: true
templates:
  - "yes"
tests:
  - {utterance: "yes", intent: approvals.respond, pending: 1}
  - {utterance: "yes", intent: "none", pending: 0}
  - {utterance: "gibberish", intent: "none"}
`

const declWriteEligibleYAML = `
id: calendar.create_event
description: Create a calendar event
kind: write
action: gcal.create_event
proposer: calendar.create
slots:
  when: {type: date, required: true}
  at: {type: time, required: true}
  title: {type: text}
templates:
  - "book a meeting {when} at {at} [called {title}]"
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
tests:
  - {utterance: "book a meeting tomorrow at 3pm", intent: calendar.create_event, slots: {when: tomorrow, at: "3pm"}}
  - {utterance: "gibberish", intent: "none"}
`

func declFixtureRegistry(t *testing.T) *intents.Registry {
	t.Helper()
	m, err := twins.Parse([]byte(declManifestYAML))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	fns := intents.Functions{
		Read: map[string]intents.FunctionSpec{
			"store.calendar_events":  {ID: "store.calendar_events", Args: map[string]slots.Type{"when": slots.TypeDateRange}, ReadOnly: true, Deterministic: true, Class: intents.ClassLookup},
			"status.overview":        {ID: "status.overview", Args: map[string]slots.Type{}, ReadOnly: true, Class: intents.ClassLookup},
			"approvals.bind_pending": {ID: "approvals.bind_pending", Args: map[string]slots.Type{}, ReadOnly: true, Class: intents.ClassAction, SideEffects: []string{"approval_surface"}},
		},
		Write: map[string]intents.FunctionSpec{
			"calendar.create": {
				ID:       "calendar.create",
				Args:     map[string]slots.Type{"when": slots.TypeDate, "at": slots.TypeTime, "title": slots.TypeText},
				Required: []string{"when", "at"},
				Class:    intents.ClassAction,
				Emits:    []string{"when", "at"},
			},
		},
	}
	schema := func(action string) (intents.SchemaInfo, bool) {
		if action != "gcal.create_event" {
			return intents.SchemaInfo{}, false
		}
		return intents.SchemaInfo{Required: []string{"when", "at"}, Properties: []string{"when", "at", "title"}}, true
	}
	fsys := fstest.MapFS{
		"twins/testtwin/intents/_shared.yaml":               {Data: []byte(declSharedYAML)},
		"twins/testtwin/intents/schedule_on_date.yaml":      {Data: []byte(declReadEligibleYAML)},
		"twins/testtwin/intents/status_overview.yaml":       {Data: []byte(declReadNotEligibleYAML)},
		"twins/testtwin/intents/approvals_respond.yaml":     {Data: []byte(declPendingYAML)},
		"twins/testtwin/intents/calendar_create_event.yaml": {Data: []byte(declWriteEligibleYAML)},
	}
	reg, err := intents.LoadRegistry(fsys, m, fns, intents.LoadOptions{Schema: schema})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	return reg
}

func TestDeclarationsOnlyReflexEligibleNotPending(t *testing.T) {
	reg := declFixtureRegistry(t)
	decls := Declarations(reg)

	var names []string
	for _, d := range decls {
		names = append(names, d.Name)
	}
	sort.Strings(names)

	want := []string{"calendar_create_event", "schedule_on_date"}
	if len(names) != len(want) {
		t.Fatalf("Declarations() names = %v, want %v", names, want)
	}
	for i, n := range names {
		if n != want[i] {
			t.Fatalf("Declarations() names = %v, want %v", names, want)
		}
	}

	// status.overview is a candidate but not reflex_eligible: excluded.
	// approvals.respond is reflex_eligible but requires_pending_approval:
	// excluded.
	for _, d := range decls {
		if d.Name == "status_overview" || d.Name == "approvals_respond" {
			t.Fatalf("Declarations() unexpectedly included %q", d.Name)
		}
	}
}

func TestDeclarationsIntentIDRoundTrips(t *testing.T) {
	reg := declFixtureRegistry(t)
	decls := Declarations(reg)
	byName := map[string]Decl{}
	for _, d := range decls {
		byName[d.Name] = d
	}
	d, ok := byName["schedule_on_date"]
	if !ok {
		t.Fatalf("schedule_on_date not declared")
	}
	if d.IntentID() != "schedule.on_date" {
		t.Fatalf("IntentID() = %q, want schedule.on_date", d.IntentID())
	}
	if d.Description == "" {
		t.Fatal("Description must not be empty")
	}
	p, ok := d.Params["when"]
	if !ok {
		t.Fatalf("Params = %+v, want a \"when\" param", d.Params)
	}
	if p.Type != "string" {
		t.Fatalf("Params[when].Type = %q, want \"string\"", p.Type)
	}
}

func TestDeclarationsWriteIntentIncludesRequiredParams(t *testing.T) {
	reg := declFixtureRegistry(t)
	decls := Declarations(reg)
	for _, d := range decls {
		if d.Name != "calendar_create_event" {
			continue
		}
		if !d.Params["when"].Required || !d.Params["at"].Required {
			t.Fatalf("Params = %+v, want when/at required", d.Params)
		}
		if d.Params["title"].Required {
			t.Fatal("title must not be required")
		}
		return
	}
	t.Fatal("calendar_create_event not declared")
}
