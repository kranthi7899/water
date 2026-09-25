package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/nervous"
	"water/internal/nervous/intents"
	"water/internal/nervous/propose"
	"water/internal/nervous/render"
	"water/internal/runtime"
	"water/internal/store"
	"water/internal/twins"
)

// ---- (*Daemon).ProposeAction / proposeEnvelope, direct calls ----

func TestProposeActionLevelAQueuesEnvelopeAndAudits(t *testing.T) {
	h := newHarness(t)
	envelope, err := h.d.ProposeAction(context.Background(), "fake_mail.send_email",
		map[string]any{"to": []any{"dana@acme.com"}, "subject": "Re: Hi", "body": "Confirmed."}, runtime.ChannelCLI)
	if err != nil {
		t.Fatalf("ProposeAction: %v", err)
	}
	if envelope.Status != approvals.Pending {
		t.Fatalf("status = %s, want pending", envelope.Status)
	}
	pending, err := h.q.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != envelope.ID {
		t.Fatalf("pending = %+v, want exactly one envelope (%s)", pending, envelope.ID)
	}
	if len(h.mail.Sent()) != 0 {
		t.Fatalf("sent = %v, want 0 (nothing executes at proposal time)", h.mail.Sent())
	}
	entries := readAuditEntries(t, h.log.Path())
	proposeCount := 0
	for _, e := range entries {
		if e.EnvelopeID == envelope.ID && e.Kind == audit.KindPropose {
			proposeCount++
		}
	}
	if proposeCount != 1 {
		t.Fatalf("propose audit records for %s = %d, want 1", envelope.ID, proposeCount)
	}
}

// TestProposeActionDeniesNonLevelA is the R-20 regression test: checkAction
// should never let a write intent's action resolve to anything but level A
// or D, so this should never happen in practice -- but ProposeAction denies
// it defensively anyway, rather than trusting the caller.
func TestProposeActionDeniesNonLevelA(t *testing.T) {
	h := newHarness(t)
	_, err := h.d.ProposeAction(context.Background(), "fake_mail.list_messages", map[string]any{}, runtime.ChannelCLI)
	if err == nil || !strings.Contains(err.Error(), "level A") {
		t.Fatalf("err = %v, want a level-A complaint", err)
	}
	pending, err := h.q.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %+v, want none (denied before ever proposing)", pending)
	}
}

func TestProposeActionDeniesUnknownFunction(t *testing.T) {
	h := newHarness(t)
	_, err := h.d.ProposeAction(context.Background(), "nope.nope", map[string]any{}, runtime.ChannelCLI)
	if err == nil {
		t.Fatalf("err = nil, want an error for a function not in the manifest")
	}
}

// ---- end to end: a write intent's proposal through the real HTTP turn path ----

const actionsIntentSharedYAML = `
rules: {}
skip_words: [please, hey]
deny_words: [reply, send]
escalate_words: [should]
clause_joiners: ["and then"]
corrections: []
`

// mailSendReplyToFakeMailYAML targets the harness's own fake_mail.send_email
// (level A, schema {to, subject, body} -- an exact match for mail.reply's
// Emits), so this exercises the real connector registry/manifest the
// harness already uses, not a synthetic one.
const mailSendReplyToFakeMailYAML = `
id: mail.send_reply
description: Send a dictated reply
kind: write
action: fake_mail.send_email
proposer: mail.reply
slots:
  who: {type: person, required: true}
  body: {type: text, required: true}
templates:
  - "send {who} a reply saying {body}"
escalate_if: [slot_unresolved, ambiguous_match]
reflex_eligible: true
tests:
  - {utterance: "send dana a reply saying got it thanks", intent: mail.send_reply, slots: {who: "dana <dana@acme.com>"}}
  - {utterance: "gibberish nonsense", intent: "none"}
`

// writeIntentNervous builds a *nervous.Nervous with one write intent
// (mail.send_reply, targeting the harness's real fake_mail.send_email) and
// Actions wired to d, so a real turn over HTTP exercises the exact same
// proposeEnvelope path a model-initiated tool call uses.
func writeIntentNervous(t *testing.T, m *twins.Manifest, st *store.Store, d *Daemon) *nervous.Nervous {
	t.Helper()
	fsys := fstest.MapFS{
		"twins/" + m.ID + "/intents/_shared.yaml":         {Data: []byte(actionsIntentSharedYAML)},
		"twins/" + m.ID + "/intents/mail_send_reply.yaml": {Data: []byte(mailSendReplyToFakeMailYAML)},
	}
	reg, err := intents.LoadRegistry(fsys, m, intents.Functions{Write: propose.Specs()}, intents.LoadOptions{})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	cfg := nervous.DefaultConfig()
	cfg.Registry = func() *intents.Registry { return reg }
	cfg.Style = render.DefaultStyle()
	cfg.Store = st
	cfg.Actions = d
	n, err := nervous.New(cfg)
	if err != nil {
		t.Fatalf("nervous.New: %v", err)
	}
	return n
}

// TestWriteIntentTurnQueuesEnvelopeThroughSharedPath is the R-20 headline
// integration test: a write intent matched by the sous chef proposes
// through the exact same proposeEnvelope path a model-queued tool call
// uses. It proves, over real HTTP, that the read-back and approval_required
// are emitted, exactly one envelope is queued (one propose audit record),
// and zero connector executions happen until a real decision.
func TestWriteIntentTurnQueuesEnvelopeThroughSharedPath(t *testing.T) {
	h := newHarness(t)
	if err := h.st.Upsert(context.Background(), &store.Message{
		Meta:    store.Meta{Source: "fake", SourceID: "m1", CreatedAt: time.Now(), UpdatedAt: time.Now()},
		From:    "Dana <dana@acme.com>",
		Subject: "Hi",
		SentAt:  time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	h.d.cfg.Nervous = writeIntentNervous(t, h.d.cfg.Manifest, h.st, h.d)

	resp := h.post(t, "/v1/turns", `{"channel":"cli","prompt":"send dana a reply saying got it thanks"}`, h.token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	events := readEvents(t, resp)

	var approvalID string
	var gotReadback bool
	for _, e := range events {
		if e.Kind == runtime.EventApprovalRequired {
			approvalID = e.ApprovalID
		}
		if e.Kind == runtime.EventDone && strings.Contains(e.Text, "Send email") {
			gotReadback = true
		}
	}
	if approvalID == "" {
		t.Fatalf("events = %+v, want an approval_required event", events)
	}
	if !gotReadback {
		t.Fatalf("events = %+v, want the done text to carry the read-back", events)
	}
	if h.fake.Calls() != 0 {
		t.Fatalf("model backend calls = %d, want 0 (the sous chef answered)", h.fake.Calls())
	}
	if len(h.mail.Sent()) != 0 {
		t.Fatalf("sent = %v, want 0 (nothing executes until a real decision)", h.mail.Sent())
	}
	pending, err := h.q.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != approvalID {
		t.Fatalf("pending = %+v, want exactly one envelope (%s)", pending, approvalID)
	}
	entries := readAuditEntries(t, h.log.Path())
	proposeCount := 0
	for _, e := range entries {
		if e.EnvelopeID == approvalID && e.Kind == audit.KindPropose {
			proposeCount++
		}
	}
	if proposeCount != 1 {
		t.Fatalf("propose audit records for %s = %d, want 1", approvalID, proposeCount)
	}

	// Deciding it now runs the exact same execution path a model-queued
	// call's approval would.
	env, err := h.q.Get(context.Background(), approvalID)
	if err != nil {
		t.Fatal(err)
	}
	decideResp := h.post(t, "/v1/approvals/"+approvalID+"/decision", `{"payload_hash":"`+env.PayloadHash+`","reply":"yes"}`, h.token)
	decideResp.Body.Close()
	if decideResp.StatusCode != http.StatusOK {
		t.Fatalf("decision status = %d", decideResp.StatusCode)
	}
	if len(h.mail.Sent()) != 1 {
		t.Fatalf("sent = %v, want exactly one message after approval", h.mail.Sent())
	}
}
