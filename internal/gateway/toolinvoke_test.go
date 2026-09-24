package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/gate"
)

// invokeAsModel calls /v1/tools/invoke with a freshly-minted turn token, the
// way the MCP bridge subprocess would on the model's behalf.
func (h *harness) invokeAsModel(t *testing.T, origin gate.Origin, taint gate.Taint, function string, args map[string]any) map[string]any {
	t.Helper()
	tok := h.d.mintTurnToken(origin, taint, time.Hour)

	body, _ := json.Marshal(map[string]any{"function": function, "args": args})
	req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/v1/tools/invoke", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestALevelToolCallIsQueuedNotExecuted(t *testing.T) {
	h := newHarness(t)
	out := h.invokeAsModel(t, gate.P0, gate.Clean, "fake_mail.send_email",
		map[string]any{"to": []any{"dana@acme.com"}, "subject": "Re: Hi", "body": "Confirmed."})
	if out["status"] != "queued" {
		t.Fatalf("out = %+v, want status=queued", out)
	}
	id, _ := out["approval_id"].(string)
	if id == "" {
		t.Fatal("no approval_id returned")
	}
	if len(h.mail.Sent()) != 0 {
		t.Fatal("send_email executed inline; it must only be queued")
	}
	pending, err := h.q.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != id {
		t.Fatalf("pending = %+v", pending)
	}
}

func TestApprovalDecisionRefusesStaleHashAndExecutesOnceWithTheRightOne(t *testing.T) {
	h := newHarness(t)
	out := h.invokeAsModel(t, gate.P0, gate.Clean, "fake_mail.send_email",
		map[string]any{"to": []any{"dana@acme.com"}, "subject": "Re: Hi", "body": "Confirmed."})
	id := out["approval_id"].(string)

	stale := h.post(t, "/v1/approvals/"+id+"/decision", `{"payload_hash":"not-the-real-hash","reply":"yes"}`, h.token)
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("stale hash: status = %d", stale.StatusCode)
	}
	stale.Body.Close()

	env, err := h.q.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	good := h.post(t, "/v1/approvals/"+id+"/decision", `{"payload_hash":"`+env.PayloadHash+`","reply":"yes"}`, h.token)
	if good.StatusCode != http.StatusOK {
		t.Fatalf("decision: status = %d", good.StatusCode)
	}
	var result DecisionResult
	if err := json.NewDecoder(good.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	good.Body.Close()

	// A "yes" both approves AND executes, in this one call, exactly once.
	if !result.Executed {
		t.Fatalf("expected the decision to execute the action: %+v", result)
	}
	if result.Envelope.Status != approvals.Executed {
		t.Fatalf("envelope status = %s, want executed", result.Envelope.Status)
	}
	if len(h.mail.Sent()) != 1 {
		t.Fatalf("sent = %v, want exactly one message", h.mail.Sent())
	}

	// Deciding the same (already-decided) envelope again must not send twice.
	again := h.post(t, "/v1/approvals/"+id+"/decision", `{"payload_hash":"`+env.PayloadHash+`","reply":"yes"}`, h.token)
	again.Body.Close()
	if len(h.mail.Sent()) != 1 {
		t.Fatalf("deciding twice sent %d messages, want 1", len(h.mail.Sent()))
	}

	n, verr := audit.Verify(h.log.Path())
	if verr != nil || n == 0 {
		t.Fatalf("audit chain: %d entries, %v", n, verr)
	}
}

func TestTaintedSLevelCallIsQueuedNotAutoRun(t *testing.T) {
	h := newHarness(t)
	out := h.invokeAsModel(t, gate.P0, gate.Tainted, "notes.save_note", map[string]any{"text": "from an email"})
	if out["status"] != "queued" {
		t.Fatalf("out = %+v, want status=queued (tainted S escalates)", out)
	}
	if len(h.notes.saved) != 0 {
		t.Fatal("save_note executed despite tainted S-level input")
	}
}

func TestCleanSLevelCallRunsAutonomously(t *testing.T) {
	h := newHarness(t)
	out := h.invokeAsModel(t, gate.P0, gate.Clean, "notes.save_note", map[string]any{"text": "a clean note"})
	if out["status"] != "ok" {
		t.Fatalf("out = %+v, want status=ok", out)
	}
	if len(h.notes.saved) != 1 {
		t.Fatalf("saved = %v", h.notes.saved)
	}
}

// TestExternalToolResultEscalatesSessionTaint is the A3 taint fix: a model
// tool call whose result is Untrusted (fake_mail.list_messages is External)
// must escalate the daemon's stable session token to tainted, so a later
// call in the same warm session is authorized as tainted even though this
// one call itself was requested clean.
func TestExternalToolResultEscalatesSessionTaint(t *testing.T) {
	h := newHarness(t)
	tok := h.d.stableSessionToken()
	if ta, ok := h.d.lookupTurnToken(tok); !ok || ta.Taint != gate.Clean {
		t.Fatalf("expected a fresh session token to start clean, got %+v ok=%v", ta, ok)
	}

	out := h.invokeAsModel(t, gate.P0, gate.Clean, "fake_mail.list_messages", nil)
	if out["status"] != "ok" {
		t.Fatalf("out = %+v, want status=ok", out)
	}

	ta, ok := h.d.lookupTurnToken(tok)
	if !ok || ta.Taint != gate.Tainted {
		t.Fatalf("an External tool result did not escalate the session token's taint: %+v ok=%v", ta, ok)
	}
}

func TestExpiredTurnTokenIsRejected(t *testing.T) {
	h := newHarness(t)
	tok := h.d.mintTurnToken(gate.P0, gate.Clean, -time.Second) // already expired
	body := `{"function":"fake_mail.list_messages","args":{}}`
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/v1/tools/invoke", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for an expired turn token", resp.StatusCode)
	}
}

// TestApprovedTaintedSLevelEnvelopeIsClaimed: an S-level call queued only
// because it was tainted must, once approved and run through the decision
// handler, be claimed like any envelope — moved to Executed and bound in the
// audit decision — so a later ExpireStale never records a denial for an
// action that actually ran.
func TestApprovedTaintedSLevelEnvelopeIsClaimed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	out := h.invokeAsModel(t, gate.P0, gate.Tainted, "notes.save_note", map[string]any{"text": "from an email"})
	id, _ := out["approval_id"].(string)
	if out["status"] != "queued" || id == "" {
		t.Fatalf("out = %+v, want queued with an approval_id", out)
	}
	env, err := h.q.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	resp := h.post(t, "/v1/approvals/"+id+"/decision", `{"payload_hash":"`+env.PayloadHash+`","reply":"yes"}`, h.token)
	var result DecisionResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !result.Executed || len(h.notes.saved) != 1 {
		t.Fatalf("expected the approved note to execute once: %+v saved=%v", result, h.notes.saved)
	}
	got, err := h.q.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != approvals.Executed {
		t.Fatalf("envelope status = %s, want executed (the gate never claimed it)", got.Status)
	}

	h.q.Now = func() time.Time { return got.ExpiresAt.Add(time.Hour) }
	if err := h.q.ExpireStale(ctx); err != nil {
		t.Fatal(err)
	}
	entries := readAuditEntries(t, h.log.Path())
	for _, e := range entries {
		if e.EnvelopeID == id && e.Kind == audit.KindDenial {
			t.Fatalf("audit records a denial for an envelope that executed: %+v", e)
		}
	}
	bound := false
	for _, e := range entries {
		if e.EnvelopeID == id && e.Kind == audit.KindDecision && e.Allowed && strings.Contains(e.Reason, "with approved envelope") {
			bound = true
		}
	}
	if !bound {
		t.Fatal("the gate's decision for the approved envelope does not record the envelope binding")
	}
}

func readAuditEntries(t *testing.T, path string) []audit.Entry {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []audit.Entry
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e audit.Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}
