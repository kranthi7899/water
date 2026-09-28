package approvals_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/connectors"
	"water/internal/connectors/fake"
	"water/internal/connectors/requests"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

const requestManifest = `
id: test
name: Test twin
usage: {window: 1h, model_calls: 50, auto_model_calls: 10}
connectors:
  - name: requests
    functions:
      - {name: respond, level: A, rate: {max: 30, per: 1h}}
  - name: fake_mail
    functions:
      - {name: send_email, level: A, rate: {max: 20, per: 1h}}
`

// TestProposeRequestApprovesAndExecutesThroughTheGateOnly is U15's core
// proof (docs/slices/UI.md, "Owner decisions needed before building", U15):
// approving a person-request envelope executes requests.respond through
// Gate.Invoke, and nothing else in the registry ever runs -- in particular,
// no mail is sent, even though a send function sits right there in the same
// manifest and registry.
func TestProposeRequestApprovesAndExecutesThroughTheGateOnly(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	q := approvals.NewQueue(st, log)
	mail := fake.NewMail()
	reg, err := connectors.NewRegistry(requests.New(), mail)
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(requestManifest))
	if err != nil {
		t.Fatal(err)
	}
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: vault.NewMemory(), Store: st})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Lee asks for a $3k/mo budget reallocation; the CEO's drafted answer is
	// already the envelope's payload -- approving it just executes that
	// exact text, like any other envelope.
	e, err := q.ProposeRequest(ctx, approvals.Envelope{
		RequestedBy: "lee", Origin: "p1", Kind: approvals.KindMoney,
		Payload: map[string]any{
			"request_id": "req_lee_reallocation",
			"answer":     "Approved: move $3k/mo from Marketing to Infra.",
			"note":       "see budget note",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if e.Action != "requests.respond" {
		t.Fatalf("action = %q, want requests.respond", e.Action)
	}
	if e.OriginKind != approvals.OriginKindPersonRequest {
		t.Fatalf("origin_kind = %q, want %q", e.OriginKind, approvals.OriginKindPersonRequest)
	}
	if e.RequestedBy != "lee" {
		t.Fatalf("requested_by = %q, want lee", e.RequestedBy)
	}
	if e.Kind != approvals.KindMoney {
		t.Fatalf("kind = %q, want the caller's explicit %q (not the requests.respond fallback)", e.Kind, approvals.KindMoney)
	}

	decided, err := q.Decide(ctx, e.ID, approvals.Yes)
	if err != nil || decided.Status != approvals.Approved {
		t.Fatalf("decide: %+v %v", decided, err)
	}

	res, err := g.Invoke(ctx, gate.Call{
		Function: e.Action, Args: e.Payload, Origin: gate.Origin(e.Origin), Taint: gate.Tainted, EnvelopeID: e.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("output: %v", err)
	}
	if out["recorded"] != true || out["answer"] != "Approved: move $3k/mo from Marketing to Infra." {
		t.Fatalf("output = %s", res.Output)
	}

	// The core proof: nothing outward ran.
	if n := len(mail.Sent()); n != 0 {
		t.Fatalf("requests.respond's approval also sent %d mail(s)", n)
	}
	if got, _ := q.Get(ctx, e.ID); got.Status != approvals.Executed {
		t.Fatalf("status = %s, want executed", got.Status)
	}
}

// TestProposeRequestRequiresRequestedBy: a person-request envelope with no
// requester is refused before it is ever proposed.
func TestProposeRequestRequiresRequestedBy(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	q := approvals.NewQueue(st, log)
	if _, err := q.ProposeRequest(context.Background(), approvals.Envelope{Origin: "p1", Payload: map[string]any{"request_id": "r1", "answer": "ok"}}); err == nil {
		t.Fatal("proposed with no RequestedBy")
	}
}
