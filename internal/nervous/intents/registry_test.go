package intents

import (
	"strings"
	"testing"
	"testing/fstest"

	"water/internal/nervous/slots"
	"water/internal/twins"
)

const baseManifestYAML = `
id: testtwin
name: Test twin
usage: {window: 5h, model_calls: 200, auto_model_calls: 40}
models: {fast: haiku, strong: ""}
connectors:
  - name: gcal
    functions:
      - {name: list_events, level: R}
  - name: gmail
    functions:
      - {name: list_messages, level: R}
`

func mustManifest(t *testing.T, y string) *twins.Manifest {
	t.Helper()
	m, err := twins.Parse([]byte(y))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	return m
}

func testFunctions() Functions {
	return Functions{
		Read: map[string]FunctionSpec{
			"store.calendar_events": {
				ID: "store.calendar_events", Args: map[string]slots.Type{"when": slots.TypeDateRange},
				ReadOnly: true, Deterministic: true, Class: ClassLookup, QuickTool: "quick.calendar",
			},
			"store.next_event": {
				ID: "store.next_event", Args: map[string]slots.Type{},
				ReadOnly: true, Deterministic: true, Class: ClassLookup,
			},
			"control.cancel_tasks": {
				ID: "control.cancel_tasks", Args: map[string]slots.Type{},
				ReadOnly: true, Class: ClassControl, SideEffects: []string{"process_control"},
			},
			"approvals.bind_pending": {
				ID: "approvals.bind_pending", Args: map[string]slots.Type{},
				ReadOnly: true, Class: ClassAction, SideEffects: []string{"approval_surface"},
			},
			"mail.write_only": {
				// Not ReadOnly: used to test the reflex_eligible-requires-ReadOnly rule.
				ID: "mail.write_only", Args: map[string]slots.Type{}, ReadOnly: false, Class: ClassLookup,
			},
			// Deliberately collides with the manifest's gcal.list_events,
			// to exercise the namespace-disjointness check: a real reflex
			// handler would never be registered under a manifest function's
			// own id, but the loader must still catch it if one ever were.
			"gcal.list_events": {
				ID: "gcal.list_events", Args: map[string]slots.Type{}, ReadOnly: true, Deterministic: true, Class: ClassLookup,
			},
		},
		Write: map[string]FunctionSpec{
			"calendar.create": {
				ID: "calendar.create",
				Args: map[string]slots.Type{
					"when": slots.TypeDate, "at": slots.TypeTime, "title": slots.TypeText,
				},
				Required: []string{"when", "at"},
				Class:    ClassAction,
				Emits:    []string{"title", "start", "end"},
			},
		},
	}
}

const sharedYAML = `
rules: {}
skip_words: [please, hey]
deny_words: [cancel, delete]
escalate_words: [should, prioritize, "which is better"]
clause_joiners: ["and then"]
corrections: ["that's wrong"]
`

const readIntentYAML = `
id: schedule.on_date
description: Events on a given day
function: store.calendar_events
slots:
  when: {type: daterange, default: today}
templates:
  - "(what's|what is) on [my] (calendar|schedule) [for] {when}"
escalate_if: [slot_unresolved, ambiguous_match]
reflex_eligible: true
tests:
  - {utterance: "what's on tomorrow", intent: schedule.on_date, slots: {when: tomorrow}}
  - {utterance: "cancel my 3pm meeting", intent: "none"}
`

const writeIntentYAML = `
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
  - {utterance: "what's on my calendar", intent: "none"}
`

func wellFormedFS() fstest.MapFS {
	return fstest.MapFS{
		"twins/testtwin/intents/_shared.yaml":               {Data: []byte(sharedYAML)},
		"twins/testtwin/intents/schedule_on_date.yaml":      {Data: []byte(readIntentYAML)},
		"twins/testtwin/intents/calendar_create_event.yaml": {Data: []byte(writeIntentYAML)},
	}
}

func TestLoadRegistryWellFormed(t *testing.T) {
	m := mustManifest(t, baseManifestYAML)
	r, err := LoadRegistry(wellFormedFS(), m, testFunctions(), LoadOptions{})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if len(r.Intents()) != 2 {
		t.Fatalf("Intents() = %d, want 2", len(r.Intents()))
	}
	read, ok := r.Lookup("schedule.on_date")
	if !ok || !read.Active {
		t.Fatalf("schedule.on_date: got %+v, want active", read)
	}
	write, ok := r.Lookup("calendar.create_event")
	if !ok {
		t.Fatalf("calendar.create_event: not found")
	}
	// gcal.create_event is not in the base manifest and is a planned
	// action, so it should be inactive, not an error.
	if write.Active {
		t.Fatalf("calendar.create_event: got active, want inactive (gcal.create_event not granted)")
	}
	if !strings.Contains(write.InactiveReason, "gcal.create_event") || !strings.Contains(write.InactiveReason, "planned") {
		t.Fatalf("InactiveReason = %q, want it to mention the action and \"planned\"", write.InactiveReason)
	}
	cands := r.Candidates()
	if len(cands) != 1 || cands[0].ID != "schedule.on_date" {
		t.Fatalf("Candidates() = %+v, want only schedule.on_date", cands)
	}
	shadow := r.Shadow()
	if len(shadow) != 1 || shadow[0].ID != "calendar.create_event" {
		t.Fatalf("Shadow() = %+v, want only calendar.create_event", shadow)
	}
	if r.Hash() == "" {
		t.Fatalf("Hash() is empty")
	}
	if len(r.Templates("schedule.on_date")) != 1 {
		t.Fatalf("Templates(schedule.on_date) = %d, want 1", len(r.Templates("schedule.on_date")))
	}
}

func TestLoadRegistryMissingDirectoryIsEmpty(t *testing.T) {
	m := mustManifest(t, baseManifestYAML)
	r, err := LoadRegistry(fstest.MapFS{}, m, testFunctions(), LoadOptions{})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if len(r.Intents()) != 0 {
		t.Fatalf("Intents() = %d, want 0", len(r.Intents()))
	}
}

func TestLoadRegistryRequiresSharedFileWhenDirExists(t *testing.T) {
	m := mustManifest(t, baseManifestYAML)
	fsys := fstest.MapFS{
		"twins/testtwin/intents/schedule_on_date.yaml": {Data: []byte(readIntentYAML)},
	}
	_, err := LoadRegistry(fsys, m, testFunctions(), LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "_shared.yaml") {
		t.Fatalf("LoadRegistry error = %v, want a _shared.yaml complaint", err)
	}
}

func TestLoadRegistryRejectsReservedConnectorName(t *testing.T) {
	m := mustManifest(t, `
id: testtwin
name: Test twin
usage: {window: 5h, model_calls: 200, auto_model_calls: 40}
connectors:
  - name: store
    functions:
      - {name: list_events, level: R}
`)
	_, err := LoadRegistry(fstest.MapFS{}, m, testFunctions(), LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("LoadRegistry error = %v, want a reserved-name complaint", err)
	}
}

// replaceField swaps one line of well-formed YAML for another and returns
// the fixture set with just the read-intent file replaced, for table cases
// that mutate a single field.
func fsWithIntent(intentYAML string) fstest.MapFS {
	return fstest.MapFS{
		"twins/testtwin/intents/_shared.yaml": {Data: []byte(sharedYAML)},
		"twins/testtwin/intents/one.yaml":     {Data: []byte(intentYAML)},
	}
}

func TestLoadRegistryRejects(t *testing.T) {
	m := mustManifest(t, baseManifestYAML)
	fns := testFunctions()

	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			"bad id shape",
			`
id: BadId
description: x
function: store.next_event
templates: ["hi"]
escalate_if: [slot_unresolved, ambiguous_match]
tests: [{utterance: hi, intent: BadId}, {utterance: bye, intent: "none"}]
`,
			"must be two lowercase",
		},
		{
			"unknown top-level key",
			`
id: a.b
description: x
function: store.next_event
bogus: true
templates: ["hi"]
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"field bogus not found",
		},
		{
			"undeclared slot in template",
			`
id: a.b
description: x
function: store.calendar_events
templates: ["show {ghost}"]
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"undeclared slot",
		},
		{
			"unknown rule reference",
			`
id: a.b
description: x
function: store.next_event
templates: ["<nope> me"]
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"unknown rule",
		},
		{
			"escalate_if outside enum",
			`
id: a.b
description: x
function: store.next_event
templates: ["hi"]
escalate_if: [not_a_real_reason]
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"slot_unresolved or ambiguous_match",
		},
		{
			"requires_pending_approval on the wrong intent",
			`
id: a.b
description: x
function: store.next_event
templates: ["hi"]
requires_pending_approval: true
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"approvals.respond",
		},
		{
			"read function is also a manifest function",
			`
id: a.b
description: x
function: gcal.list_events
templates: ["hi"]
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"must not also be a manifest function",
		},
		{
			"read function not registered",
			`
id: a.b
description: x
function: store.does_not_exist
templates: ["hi"]
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"not a registered reflex handler",
		},
		{
			"reflex_eligible on a non-read-only function",
			`
id: a.b
description: x
function: mail.write_only
templates: ["hi"]
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"not read-only",
		},
		{
			"reflex_eligible missing an escalate_if value",
			`
id: a.b
description: x
function: store.next_event
templates: ["hi"]
reflex_eligible: true
escalate_if: [slot_unresolved]
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"both slot_unresolved and ambiguous_match",
		},
		{
			"read intent uses a text slot",
			`
id: a.b
description: x
function: store.calendar_events
slots:
  when: {type: text}
templates: ["show {when}"]
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"may not use a text slot",
		},
		{
			"slot not an argument of the function",
			`
id: a.b
description: x
function: store.calendar_events
slots:
  ghost: {type: count}
templates: ["show {ghost}"]
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"not an argument of",
		},
		{
			"slot type mismatch",
			`
id: a.b
description: x
function: store.calendar_events
slots:
  when: {type: count}
templates: ["show {when}"]
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"declares it as",
		},
		{
			"write intent sets function",
			`
id: a.b
description: x
kind: write
function: store.next_event
action: gcal.create_event
proposer: calendar.create
templates: ["hi"]
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"must not set function",
		},
		{
			"write intent missing proposer",
			`
id: a.b
description: x
kind: write
action: gcal.create_event
templates: ["hi"]
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"requires both action and proposer",
		},
		{
			"write intent's action is not connector.function shaped",
			`
id: a.b
description: x
kind: write
action: not-an-action
proposer: calendar.create
slots:
  when: {type: date, required: true}
  at: {type: time, required: true}
templates: ["hi {when} {at}"]
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"not a connector.function id",
		},
		{
			"write intent's action connector is not in the manifest",
			`
id: a.b
description: x
kind: write
action: slack.post_message
proposer: calendar.create
slots:
  when: {type: date, required: true}
  at: {type: time, required: true}
templates: ["hi {when} {at}"]
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"is not in the",
		},
		{
			"write intent's action is neither granted nor planned",
			`
id: a.b
description: x
kind: write
action: gcal.list_events
proposer: calendar.create
slots:
  when: {type: date, required: true}
  at: {type: time, required: true}
templates: ["hi {when} {at}"]
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"is level R; write intents must target a level-A function",
		},
		{
			"required arg with no slot",
			`
id: a.b
description: x
kind: write
action: gcal.create_event
proposer: calendar.create
slots:
  when: {type: date, required: true}
templates: ["hi {when}"]
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"required argument \"at\" has no declared slot",
		},
		{
			"text slot not template-final",
			`
id: a.b
description: x
kind: write
action: gcal.create_event
proposer: calendar.create
slots:
  when: {type: date, required: true}
  at: {type: time, required: true}
  title: {type: text}
templates: ["book {title} {when} at {at}"]
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"text slot must be the last element",
		},
		{
			"template literal contains an escalate word",
			`
id: a.b
description: x
function: store.next_event
templates: ["should i do this"]
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"escalate word",
		},
		{
			"template literal contains a clause joiner",
			`
id: a.b
description: x
function: store.next_event
templates: ["do this and then that"]
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"clause joiner",
		},
		{
			"fewer than 2 tests",
			`
id: a.b
description: x
function: store.next_event
templates: ["hi"]
tests: [{utterance: hi, intent: a.b}]
`,
			"at least two tests",
		},
		{
			"no positive test",
			`
id: a.b
description: x
function: store.next_event
templates: ["hi"]
tests: [{utterance: hi, intent: "none"}, {utterance: bye, intent: "none"}]
`,
			"at least one test must be a positive case",
		},
		{
			"origin set in an embedded file",
			`
id: a.b
description: x
function: store.next_event
templates: ["hi"]
origin: learned
tests: [{utterance: hi, intent: a.b}, {utterance: bye, intent: "none"}]
`,
			"origin must be empty",
		},
		{
			"learned-prefixed id in an embedded file",
			`
id: learned.a
description: x
function: store.next_event
templates: ["hi"]
tests: [{utterance: hi, intent: learned.a}, {utterance: bye, intent: "none"}]
`,
			"reserved for the overlay",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadRegistry(fsWithIntent(c.yaml), m, fns, LoadOptions{})
			if err == nil {
				t.Fatalf("LoadRegistry: got no error, want one containing %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("LoadRegistry error = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

func TestLoadRegistryDuplicateID(t *testing.T) {
	m := mustManifest(t, baseManifestYAML)
	fsys := fstest.MapFS{
		"twins/testtwin/intents/_shared.yaml": {Data: []byte(sharedYAML)},
		"twins/testtwin/intents/a.yaml":       {Data: []byte(readIntentYAML)},
		"twins/testtwin/intents/b.yaml":       {Data: []byte(readIntentYAML)},
	}
	_, err := LoadRegistry(fsys, m, testFunctions(), LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "duplicate id") {
		t.Fatalf("LoadRegistry error = %v, want a duplicate id complaint", err)
	}
}

func TestWriteIntentInactiveUntilGranted(t *testing.T) {
	fns := testFunctions()

	t.Run("not granted", func(t *testing.T) {
		m := mustManifest(t, baseManifestYAML) // no gcal.create_event
		r, err := LoadRegistry(wellFormedFS(), m, fns, LoadOptions{})
		if err != nil {
			t.Fatalf("LoadRegistry: %v", err)
		}
		it, ok := r.Lookup("calendar.create_event")
		if !ok || it.Active {
			t.Fatalf("calendar.create_event: got %+v, want inactive", it)
		}
		if !strings.Contains(it.InactiveReason, "gcal.create_event is planned but not granted in the testtwin manifest") {
			t.Fatalf("InactiveReason = %q", it.InactiveReason)
		}
	})

	t.Run("granted at level A with a matching schema", func(t *testing.T) {
		grantedYAML := baseManifestYAML + `
  - name: gcal
    functions:
      - {name: create_event, level: A}
`
		// gcal already exists above; adding another "connectors: - name:
		// gcal" block would duplicate the connector, so build a clean
		// manifest instead.
		m := mustManifest(t, `
id: testtwin
name: Test twin
usage: {window: 5h, model_calls: 200, auto_model_calls: 40}
connectors:
  - name: gcal
    functions:
      - {name: list_events, level: R}
      - {name: create_event, level: A}
  - name: gmail
    functions:
      - {name: list_messages, level: R}
`)
		_ = grantedYAML
		schema := func(action string) (SchemaInfo, bool) {
			if action != "gcal.create_event" {
				return SchemaInfo{}, false
			}
			return SchemaInfo{Required: []string{"title", "start", "end"}, Properties: []string{"title", "start", "end", "attendees"}}, true
		}
		r, err := LoadRegistry(wellFormedFS(), m, fns, LoadOptions{Schema: schema})
		if err != nil {
			t.Fatalf("LoadRegistry: %v", err)
		}
		it, ok := r.Lookup("calendar.create_event")
		if !ok || !it.Active {
			t.Fatalf("calendar.create_event: got %+v, want active", it)
		}
		cands := r.Candidates()
		found := false
		for _, c := range cands {
			if c.ID == "calendar.create_event" {
				found = true
			}
		}
		if !found {
			t.Fatalf("Candidates() = %+v, want calendar.create_event once granted", cands)
		}
	})

	t.Run("granted at a level other than A is an error", func(t *testing.T) {
		m := mustManifest(t, `
id: testtwin
name: Test twin
usage: {window: 5h, model_calls: 200, auto_model_calls: 40}
connectors:
  - name: gcal
    functions:
      - {name: list_events, level: R}
      - {name: create_event, level: D}
  - name: gmail
    functions:
      - {name: list_messages, level: R}
`)
		_, err := LoadRegistry(wellFormedFS(), m, fns, LoadOptions{})
		if err == nil || !strings.Contains(err.Error(), "write intents must target a level-A function") {
			t.Fatalf("LoadRegistry error = %v, want a level complaint", err)
		}
	})

	t.Run("unknown action is an error", func(t *testing.T) {
		m := mustManifest(t, baseManifestYAML)
		fsys := fstest.MapFS{
			"twins/testtwin/intents/_shared.yaml": {Data: []byte(sharedYAML)},
			"twins/testtwin/intents/one.yaml": {Data: []byte(`
id: calendar.create_event
description: Create a calendar event
kind: write
action: gcal.sned_event
proposer: calendar.create
slots:
  when: {type: date, required: true}
  at: {type: time, required: true}
  title: {type: text}
templates:
  - "book a meeting {when} at {at} [called {title}]"
tests:
  - {utterance: "book a meeting tomorrow at 3pm", intent: calendar.create_event, slots: {when: tomorrow, at: "3pm"}}
  - {utterance: "what's on my calendar", intent: "none"}
`)},
		}
		_, err := LoadRegistry(fsys, m, fns, LoadOptions{})
		if err == nil || !strings.Contains(err.Error(), "neither a function") {
			t.Fatalf("LoadRegistry error = %v, want a neither-function-nor-planned complaint", err)
		}
	})

	t.Run("schema mismatch is an error", func(t *testing.T) {
		m := mustManifest(t, `
id: testtwin
name: Test twin
usage: {window: 5h, model_calls: 200, auto_model_calls: 40}
connectors:
  - name: gcal
    functions:
      - {name: list_events, level: R}
      - {name: create_event, level: A}
  - name: gmail
    functions:
      - {name: list_messages, level: R}
`)
		schema := func(action string) (SchemaInfo, bool) {
			return SchemaInfo{Required: []string{"title", "start", "end", "organizer"}, Properties: []string{"title", "start", "end", "organizer"}}, true
		}
		_, err := LoadRegistry(wellFormedFS(), m, fns, LoadOptions{Schema: schema})
		if err == nil || !strings.Contains(err.Error(), "does not emit required schema key") {
			t.Fatalf("LoadRegistry error = %v, want a missing-required-key complaint", err)
		}
	})
}

func TestPlannedActionsMatchDecisions(t *testing.T) {
	keys := parsePlannedActionsFromDecisions(t)
	if len(keys) == 0 {
		t.Fatalf("found no plannedActions entries in internal/decisions/registry.go — is the variable name still plannedActions?")
	}
	for k := range keys {
		if !plannedActions[k] {
			t.Errorf("intents.plannedActions is missing %q, present in decisions.plannedActions", k)
		}
	}
	for k := range plannedActions {
		if !keys[k] {
			t.Errorf("intents.plannedActions has %q, not present in decisions.plannedActions", k)
		}
	}
}

func TestLoadRegistryLearnedSkipAndReport(t *testing.T) {
	m := mustManifest(t, baseManifestYAML)
	learned := fstest.MapFS{
		"good.yaml": {Data: []byte(`
id: learned.mail_count
description: how many messages today
kind: read
function: store.next_event
templates: ["how many messages"]
escalate_if: [slot_unresolved, ambiguous_match]
reflex_eligible: true
origin: learned
tests: [{utterance: "how many messages", intent: learned.mail_count}, {utterance: "bye", intent: "none"}]
`)},
		"bad_no_origin.yaml": {Data: []byte(`
id: learned.no_origin
description: missing origin
function: store.next_event
templates: ["hi there"]
tests: [{utterance: "hi there", intent: learned.no_origin}, {utterance: "bye", intent: "none"}]
`)},
		"bad_prefix.yaml": {Data: []byte(`
id: not_learned.bad
description: wrong prefix
function: store.next_event
origin: learned
templates: ["yo"]
tests: [{utterance: "yo", intent: not_learned.bad}, {utterance: "bye", intent: "none"}]
`)},
	}
	r, err := LoadRegistry(wellFormedFS(), m, testFunctions(), LoadOptions{Learned: learned})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if _, ok := r.Lookup("learned.mail_count"); !ok {
		t.Fatalf("learned.mail_count was not loaded")
	}
	skipped := r.LearnedSkipped()
	if len(skipped) != 2 {
		t.Fatalf("LearnedSkipped() = %+v, want 2 entries", skipped)
	}
}

func TestLoadRegistryLearnedEmbeddedWins(t *testing.T) {
	m := mustManifest(t, baseManifestYAML)
	learned := fstest.MapFS{
		"collide.yaml": {Data: []byte(strings.Replace(readIntentYAML, "id: schedule.on_date", "id: schedule.on_date", 1) + "\norigin: learned\n")},
	}
	// The learned file reuses the embedded intent's id (schedule.on_date),
	// which is not even learned-id-shaped, so it should be rejected for
	// the id-prefix rule before collision is even reached — verifies
	// embedded ids are simply unreachable from the overlay.
	r, err := LoadRegistry(wellFormedFS(), m, testFunctions(), LoadOptions{Learned: learned})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if len(r.LearnedSkipped()) != 1 {
		t.Fatalf("LearnedSkipped() = %+v, want 1 entry", r.LearnedSkipped())
	}
	it, _ := r.Lookup("schedule.on_date")
	if it.Origin != "" {
		t.Fatalf("schedule.on_date: origin = %q, want the embedded (non-learned) version to remain", it.Origin)
	}
}

func TestHashDeterministicAndChanges(t *testing.T) {
	m := mustManifest(t, baseManifestYAML)
	r1, err := LoadRegistry(wellFormedFS(), m, testFunctions(), LoadOptions{})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	r2, err := LoadRegistry(wellFormedFS(), m, testFunctions(), LoadOptions{})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if r1.Hash() != r2.Hash() {
		t.Fatalf("Hash() not deterministic: %q vs %q", r1.Hash(), r2.Hash())
	}
	changed := wellFormedFS()
	changed["twins/testtwin/intents/_shared.yaml"] = &fstest.MapFile{Data: []byte(sharedYAML + "\n")}
	r3, err := LoadRegistry(changed, m, testFunctions(), LoadOptions{})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if r1.Hash() == r3.Hash() {
		t.Fatalf("Hash() did not change after file content changed")
	}
}
