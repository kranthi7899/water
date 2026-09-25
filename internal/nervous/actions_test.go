package nervous

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/nervous/intents"
	"water/internal/nervous/propose"
	"water/internal/nervous/reflex"
	"water/internal/nervous/render"
	"water/internal/runtime"
	"water/internal/store"
	"water/internal/twins"
)

const actionsManifestYAML = `
id: testtwin
name: Test twin
usage: {window: 5h, model_calls: 200, auto_model_calls: 40}
models: {fast: haiku, strong: ""}
connectors:
  - name: gmail
    functions:
      - {name: draft_message, level: D}
      - {name: send_message, level: A}
  - name: gcal
    functions:
      - {name: move_event, level: A}
      - {name: create_event, level: A}
`

const actionsSharedYAML = `
rules: {}
skip_words: [please, hey]
deny_words: [reply, send]
escalate_words: [should]
clause_joiners: ["and then"]
corrections: []
`

const mailDraftReplyYAML = `
id: mail.draft_reply
description: Draft a reply
kind: write
action: gmail.draft_message
proposer: mail.reply
slots:
  who: {type: person, required: true}
  body: {type: text, required: true}
templates:
  - "reply to {who} saying {body}"
escalate_if: [slot_unresolved, ambiguous_match]
reflex_eligible: true
tests:
  - {utterance: "reply to priya saying ok see you then", intent: mail.draft_reply, slots: {who: "priya nair <priya@x.com>"}}
  - {utterance: "gibberish nonsense", intent: "none"}
`

const mailSendReplyYAML = `
id: mail.send_reply
description: Send a reply
kind: write
action: gmail.send_message
proposer: mail.reply
slots:
  who: {type: person, required: true}
  body: {type: text, required: true}
templates:
  - "send {who} a reply saying {body}"
escalate_if: [slot_unresolved, ambiguous_match]
reflex_eligible: true
tests:
  - {utterance: "send priya a reply saying ok see you then", intent: mail.send_reply, slots: {who: "priya nair <priya@x.com>"}}
  - {utterance: "gibberish nonsense", intent: "none"}
`

const calendarMoveEventYAML = `
id: calendar.move_event
description: Move an existing calendar event
kind: write
action: gcal.move_event
proposer: calendar.move
slots:
  from: {type: time, required: true}
  on: {type: date, default: today}
  to_date: {type: date}
  to: {type: time}
templates:
  - "move my {from} to {to}"
escalate_if: [slot_unresolved, ambiguous_match]
reflex_eligible: true
tests:
  - {utterance: "move my 3pm to 4pm", intent: calendar.move_event, slots: {from: "3pm", to: "4pm"}}
  - {utterance: "gibberish nonsense", intent: "none"}
`

func actionsFixtureRegistry(t *testing.T, files map[string]string) *intents.Registry {
	t.Helper()
	m, err := twins.Parse([]byte(actionsManifestYAML))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	fsys := fstest.MapFS{"twins/testtwin/intents/_shared.yaml": {Data: []byte(actionsSharedYAML)}}
	for name, content := range files {
		fsys["twins/testtwin/intents/"+name+".yaml"] = &fstest.MapFile{Data: []byte(content)}
	}
	reg, err := intents.LoadRegistry(fsys, m, intents.Functions{Read: reflex.Specs(), Write: propose.Specs()}, intents.LoadOptions{})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	return reg
}

// actionsTestEnv builds a real (temp-dir-backed) runtime.Env and seeds one
// message from priya@x.com, so mail.reply's proposer can find something to
// reply to.
func actionsTestEnv(t *testing.T) (runtime.Env, context.Context, *backend.Fake) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	// Senders() (which builds the person entity list Validate resolves a
	// "who" slot against) derives Name/Email from the message's own From
	// header via net/mail.ParseAddress, so From must carry a display name
	// for a bare first name like "priya" to resolve (store.queries_router.go).
	if err := st.Upsert(context.Background(), &store.Message{
		Meta: store.Meta{Source: "fake", SourceID: "m1", CreatedAt: tier0FixedNow, UpdatedAt: tier0FixedNow},
		From: "Priya Nair <priya@x.com>", Subject: "Q3 budget", SentAt: tier0FixedNow,
	}); err != nil {
		t.Fatal(err)
	}
	log, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	q := approvals.NewQueue(st, log)
	q.Now = func() time.Time { return tier0FixedNow }
	m, err := twins.Parse([]byte(actionsManifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	fk := backend.NewFake("fake")
	env := runtime.Env{
		Store: st, Approvals: q, Manifest: m, Backend: fk,
		Now: func() time.Time { return tier0FixedNow },
	}
	return env, context.Background(), fk
}

// fakeActionSink records every ProposeAction call and returns a canned
// envelope, so a test can assert exactly one proposal happened without a
// real gate or connector registry.
type fakeActionSink struct {
	mu    sync.Mutex
	calls []struct {
		fn      string
		payload map[string]any
		ch      runtime.Channel
	}
	envelope approvals.Envelope
	err      error
}

func (f *fakeActionSink) ProposeAction(ctx context.Context, fn string, payload map[string]any, ch runtime.Channel) (approvals.Envelope, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, struct {
		fn      string
		payload map[string]any
		ch      runtime.Channel
	}{fn, payload, ch})
	if f.err != nil {
		return approvals.Envelope{}, f.err
	}
	return f.envelope, nil
}

func (f *fakeActionSink) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func nervousForActions(t *testing.T, reg *intents.Registry, actions ActionSink) *Nervous {
	t.Helper()
	n, err := New(Config{
		Registry:     func() *intents.Registry { return reg },
		Style:        render.DefaultStyle(),
		Tier0Enabled: true,
		MainEnabled:  true,
		Clock:        realClock{},
		AckAfter:     DefaultAckAfter,
		Actions:      actions,
		SenderWindow: 180 * 24 * time.Hour,
		SenderLimit:  500,
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestHandleWriteIntentLevelDDeliversDraftDirectly proves the level-D
// resolution (docs/slices/R.md Risk item 24): gmail.draft_message is
// granted at level D, so mail.draft_reply's RequiresApproval is false, and
// a successful proposal is delivered directly as the answer -- no
// ActionSink call, no ApprovalID, no approval_required event.
func TestHandleWriteIntentLevelDDeliversDraftDirectly(t *testing.T) {
	reg := actionsFixtureRegistry(t, map[string]string{"mail_draft_reply": mailDraftReplyYAML})
	env, ctx, fk := actionsTestEnv(t)
	sink := &fakeActionSink{}
	n := nervousForActions(t, reg, sink)

	var events []runtime.Event
	var mu sync.Mutex
	n.Handle(ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "reply to priya saying ok see you then", TaskID: "d1"}, collect(&events, &mu))

	if sink.callCount() != 0 {
		t.Fatalf("ActionSink calls = %d, want 0 (level D never queues an envelope)", sink.callCount())
	}
	if fk.Calls() != 0 {
		t.Fatalf("backend calls = %d, want 0 (the sous chef answered)", fk.Calls())
	}
	for _, e := range events {
		if e.Kind == runtime.EventApprovalRequired {
			t.Fatalf("got approval_required, want none for a level-D draft")
		}
	}
	found := false
	for _, e := range events {
		if e.Kind == runtime.EventDone {
			found = true
			if e.Text == "" {
				t.Fatalf("done event has empty text, want the drafted content")
			}
		}
	}
	if !found {
		t.Fatalf("events = %v, want a done event", events)
	}
}

// TestHandleWriteIntentLevelAQueuesEnvelope proves the level-A path:
// gmail.send_message is granted at level A, so mail.send_reply's
// RequiresApproval is true, and a successful proposal is queued through
// ActionSink -- the result carries the ApprovalID and the facade emits
// approval_required.
func TestHandleWriteIntentLevelAQueuesEnvelope(t *testing.T) {
	reg := actionsFixtureRegistry(t, map[string]string{"mail_send_reply": mailSendReplyYAML})
	env, ctx, fk := actionsTestEnv(t)
	sink := &fakeActionSink{envelope: approvals.Envelope{ID: "env_123", Action: "gmail.send_message", Status: approvals.Pending}}
	n := nervousForActions(t, reg, sink)

	var events []runtime.Event
	var mu sync.Mutex
	n.Handle(ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "send priya a reply saying ok see you then", TaskID: "a1"}, collect(&events, &mu))

	if sink.callCount() != 1 {
		t.Fatalf("ActionSink calls = %d, want exactly 1", sink.callCount())
	}
	if sink.calls[0].fn != "gmail.send_message" {
		t.Fatalf("ActionSink fn = %q, want gmail.send_message", sink.calls[0].fn)
	}
	if fk.Calls() != 0 {
		t.Fatalf("backend calls = %d, want 0 (the sous chef proposed, nothing executed)", fk.Calls())
	}
	var gotApproval bool
	for _, e := range events {
		if e.Kind == runtime.EventApprovalRequired {
			gotApproval = true
			if e.ApprovalID != "env_123" {
				t.Fatalf("approval_required.ApprovalID = %q, want env_123", e.ApprovalID)
			}
		}
	}
	if !gotApproval {
		t.Fatalf("events = %v, want an approval_required event", events)
	}
}

// TestHandleWriteIntentUnresolvedEscalatesToMain proves the sous chef never
// guesses: calendar.move's proposer needs exactly one non-cancelled event
// starting at "from" in the store, and none is seeded, so Build reports
// Unresolved and the turn escalates to the main path instead of answering.
func TestHandleWriteIntentUnresolvedEscalatesToMain(t *testing.T) {
	reg := actionsFixtureRegistry(t, map[string]string{"calendar_move_event": calendarMoveEventYAML})
	env, ctx, fk := actionsTestEnv(t)
	fk.Reply = func(backend.Request) string { return "you have no 3pm event today" }
	sink := &fakeActionSink{}
	n := nervousForActions(t, reg, sink)

	var events []runtime.Event
	var mu sync.Mutex
	n.Handle(ctx, env, Turn{Channel: runtime.ChannelCLI, Text: "move my 3pm to 4pm", TaskID: "u1"}, collect(&events, &mu))

	if sink.callCount() != 0 {
		t.Fatalf("ActionSink calls = %d, want 0 (nothing to propose)", sink.callCount())
	}
	if fk.Calls() == 0 {
		t.Fatalf("backend calls = 0, want at least 1 (escalated to the main path)")
	}
}

// TestProposeStoreFallsBackToEnvStore checks the ReadStore-or-env.Store
// fallback proposeStore shares with n.deps/n.entities: with no
// Config.ReadStore configured, it uses the per-turn env.Store, exactly like
// a real write-intent build must (Config has no store of its own set here).
func TestProposeStoreFallsBackToEnvStore(t *testing.T) {
	reg := actionsFixtureRegistry(t, map[string]string{"mail_draft_reply": mailDraftReplyYAML})
	env, _, _ := actionsTestEnv(t)
	n, err := New(Config{
		Registry: func() *intents.Registry { return reg }, Style: render.DefaultStyle(),
		Tier0Enabled: true, MainEnabled: true, Clock: realClock{}, AckAfter: DefaultAckAfter,
	})
	if err != nil {
		t.Fatal(err)
	}
	sv := n.proposeStore(env)
	if sv == nil {
		t.Fatalf("proposeStore(env) = nil, want a non-nil StoreView backed by env.Store")
	}
	msgs, err := sv.MessagesFrom(context.Background(), "priya@x.com", 1)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("MessagesFrom via env.Store fallback: got %v, %v, want the seeded message", msgs, err)
	}
}
