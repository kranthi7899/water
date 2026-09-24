package agentmail_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"water/internal/agentmail"
	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/connectors"
	"water/internal/connectors/google/gapi"
	"water/internal/connectors/google/gmail"
	"water/internal/decisions"
	"water/internal/gate"
	"water/internal/gate/permit"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

const testManifest = `
id: t
name: Test twin
usage: {window: 1h, model_calls: 50, auto_model_calls: 10}
connectors:
  - name: agentmail
    functions:
      - {name: list_messages, level: R}
auto_allowlist:
  - agentmail.list_messages
`

// fakeMailbox stands in for the real Gmail-backed agentmail.New in these
// tests, so no network fixture is needed: Watcher only cares about the raw
// JSON shape a "agentmail.list_messages" call hands back (see gmail's own
// list_messages output shape, which this reproduces directly), never about
// how that JSON was produced.
type fakeMailbox struct {
	mu       sync.Mutex
	calls    []map[string]any
	output   map[string]any
	failWith error // consumed (cleared) by the next Invoke
}

func (*fakeMailbox) Name() string                 { return agentmail.ConnectorName }
func (*fakeMailbox) Credential() (string, string) { return gapi.Service, agentmail.Account }
func (*fakeMailbox) Functions() []connectors.Function {
	return []connectors.Function{{
		Name: "list_messages", Level: twins.R, Risk: connectors.RiskLow,
		Schema: connectors.Schema{Properties: map[string]connectors.Property{
			"query": {Type: "string"}, "since_history_id": {Type: "string"},
		}},
	}}
}
func (f *fakeMailbox) Invoke(_ context.Context, p permit.Permit) (json.RawMessage, error) {
	call, err := p.Open()
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call.Args)
	if f.failWith != nil {
		err := f.failWith
		f.failWith = nil
		return nil, err
	}
	return json.Marshal(f.output)
}
func (*fakeMailbox) Normalize(string, json.RawMessage) ([]store.Record, error) { return nil, nil }

func (f *fakeMailbox) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeMailbox) lastArgs() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return nil
	}
	return f.calls[len(f.calls)-1]
}

// fakeClassifier hands back one fixed verdict for every message, and counts
// how many messages it saw.
type fakeClassifier struct {
	verdict decisions.Classification
	seen    []string // subjects
}

func (f *fakeClassifier) Classify(_ context.Context, item store.Record) (decisions.Classification, error) {
	m := item.(*store.Message)
	f.seen = append(f.seen, m.Subject)
	return f.verdict, nil
}

type rig struct {
	g    *gate.Gate
	v    *vault.MemoryVault
	st   *store.Store
	q    *approvals.Queue
	mbox *fakeMailbox

	mu    sync.Mutex
	lines []string
}

func (r *rig) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *rig) logLines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	q := approvals.NewQueue(st, log)
	mbox := &fakeMailbox{}
	reg, err := connectors.NewRegistry(mbox)
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(testManifest))
	if err != nil {
		t.Fatal(err)
	}
	v := vault.NewMemory()
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: v, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	return &rig{g: g, v: v, st: st, q: q, mbox: mbox}
}

func (r *rig) connect(t *testing.T) {
	t.Helper()
	cred := gapi.Credential{ClientID: "cid", ClientSecret: "secret", RefreshToken: "refresh"}
	sec, err := cred.Secret()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.v.Set(gapi.Service, agentmail.Account, sec); err != nil {
		t.Fatal(err)
	}
}

func newWatcher(r *rig, classifier decisions.Classifier, forwardTo string) *agentmail.Watcher {
	return agentmail.NewWatcher(agentmail.Config{
		Gate: r.g, Store: r.st, Vault: r.v, Approvals: r.q, Classifier: classifier,
		Function: agentmail.ConnectorName + ".list_messages", ForwardTo: forwardTo, Logf: r.logf,
	})
}

func rawOutput(t *testing.T, historyID string, msgs ...map[string]any) map[string]any {
	t.Helper()
	return map[string]any{"messages": msgs, "history_id": historyID}
}

// TestWatcherTickSkipsQuietlyWhenTheAgentMailboxIsNotConnected: no stored
// "agent" credential means Tick must never call the gate at all (so nothing
// is audited and no cursor is written), and must not panic.
func TestWatcherTickSkipsQuietlyWhenTheAgentMailboxIsNotConnected(t *testing.T) {
	r := newRig(t)
	w := newWatcher(r, &fakeClassifier{}, "")
	w.Tick(context.Background())
	if r.mbox.callCount() != 0 {
		t.Fatal("Tick called the connector despite no stored agent credential")
	}
	if _, ok, _ := r.st.GetCursor(context.Background(), agentmail.DefaultCursorKey); ok {
		t.Fatal("a cursor was written despite the mailbox never being polled")
	}
	found := false
	for _, l := range r.logLines() {
		if strings.Contains(l, "not connected") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a 'not connected' log line, got %v", r.logLines())
	}
}

// TestWatcherTickRecordsAnAgentDirectedMessageAsABriefSignal: a message the
// classifier says is NOT for the CEO becomes a Signal RecentSignals can
// read back, never an approval, and the connector's own next cursor is
// persisted so the next tick resumes from it.
func TestWatcherTickRecordsAnAgentDirectedMessageAsABriefSignal(t *testing.T) {
	r := newRig(t)
	r.connect(t)
	r.mbox.output = rawOutput(t, "7", map[string]any{"id": "m1", "from": "notify@service.com", "subject": "Your weekly digest", "body": "..."})
	classifier := &fakeClassifier{verdict: decisions.Classification{NeedsDecision: false, TypeID: "agent_directed"}}
	w := newWatcher(r, classifier, "ceo@real.example.com")

	w.Tick(context.Background())

	sigs, err := agentmail.RecentSignals(context.Background(), r.st, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 1 || sigs[0].From != "notify@service.com" || sigs[0].Subject != "Your weekly digest" {
		t.Fatalf("signals = %+v, want exactly the one agent-directed message", sigs)
	}
	if v, ok, err := r.st.GetCursor(context.Background(), agentmail.DefaultCursorKey); err != nil || !ok || v != "7" {
		t.Fatalf("cursor = %q, ok=%v, err=%v, want \"7\"", v, ok, err)
	}
	pending, err := r.q.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("an agent-directed message must never be staged for approval, got %+v", pending)
	}
}

// TestWatcherTickStagesAForwardForACEODirectedMessage: a message the
// classifier says IS for the CEO becomes a level-A gmail.send_message
// approval, quoting the original, addressed to agent.forward_to -- and
// nothing executes on its own; it only sits pending like any other A-level
// call until the CEO decides it.
func TestWatcherTickStagesAForwardForACEODirectedMessage(t *testing.T) {
	r := newRig(t)
	r.connect(t)
	r.mbox.output = rawOutput(t, "9", map[string]any{"id": "m2", "from": "dana@acme.com", "subject": "Need the CEO's help", "body": "Please call me back."})
	classifier := &fakeClassifier{verdict: decisions.Classification{NeedsDecision: true, TypeID: "ceo_directed"}}
	w := newWatcher(r, classifier, "ceo@real.example.com")

	w.Tick(context.Background())

	pending, err := r.q.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending approvals = %d, want exactly 1: %+v", len(pending), pending)
	}
	env := pending[0]
	if env.Action != "gmail.send_message" {
		t.Fatalf("Action = %q, want gmail.send_message", env.Action)
	}
	if env.Origin != string(gate.P1) {
		t.Fatalf("Origin = %q, want %q (never p2: gate.go's auto-mode rule must not be able to apply)", env.Origin, gate.P1)
	}
	to, _ := env.Payload["to"].([]any)
	if len(to) != 1 || to[0] != "ceo@real.example.com" {
		t.Fatalf("to = %+v, want [ceo@real.example.com]", env.Payload["to"])
	}
	subject, _ := env.Payload["subject"].(string)
	if !strings.HasPrefix(subject, "Fwd: ") || !strings.Contains(subject, "Need the CEO's help") {
		t.Fatalf("subject = %q, want a Fwd: of the original", subject)
	}
	body, _ := env.Payload["body"].(string)
	if !strings.Contains(body, "dana@acme.com") || !strings.Contains(body, "Please call me back.") {
		t.Fatalf("body does not quote the original message: %q", body)
	}
	sigs, err := agentmail.RecentSignals(context.Background(), r.st, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 0 {
		t.Fatalf("a CEO-directed message must never also become a brief signal, got %+v", sigs)
	}
}

// TestWatcherTickWithNoForwardToConfiguredNeverStages confirms the "your
// call" design choice's safety net: without agent.forward_to set, a
// CEO-directed message is logged, never silently forwarded to nowhere and
// never staged with an empty recipient (which the gate would reject anyway,
// but this should never even try).
func TestWatcherTickWithNoForwardToConfiguredNeverStages(t *testing.T) {
	r := newRig(t)
	r.connect(t)
	r.mbox.output = rawOutput(t, "1", map[string]any{"id": "m3", "from": "dana@acme.com", "subject": "Hi", "body": "b"})
	classifier := &fakeClassifier{verdict: decisions.Classification{NeedsDecision: true}}
	w := newWatcher(r, classifier, "") // no forward_to configured

	w.Tick(context.Background())

	pending, err := r.q.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %+v, want none without agent.forward_to configured", pending)
	}
	found := false
	for _, l := range r.logLines() {
		if strings.Contains(l, "not staged") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a 'not staged' log line, got %v", r.logLines())
	}
}

// TestWatcherTickClearsAnExpiredCursorForAFullResync mirrors internal/sync's
// own gmail cursor handling exactly: on gmail.ErrHistoryTooOld the stored
// cursor is dropped so the next tick falls back to a full recent fetch
// instead of failing forever against a history id Gmail no longer has.
func TestWatcherTickClearsAnExpiredCursorForAFullResync(t *testing.T) {
	r := newRig(t)
	r.connect(t)
	ctx := context.Background()
	if err := r.st.SetCursor(ctx, agentmail.DefaultCursorKey, "stale-123"); err != nil {
		t.Fatal(err)
	}
	r.mbox.failWith = fmt.Errorf("gmail: %w", gmail.ErrHistoryTooOld)
	w := newWatcher(r, &fakeClassifier{}, "")

	w.Tick(ctx)

	if _, ok, err := r.st.GetCursor(ctx, agentmail.DefaultCursorKey); err != nil || ok {
		t.Fatalf("cursor still present after ErrHistoryTooOld: ok=%v err=%v", ok, err)
	}
	if args := r.mbox.lastArgs(); args["since_history_id"] != "stale-123" {
		t.Fatalf("first attempt should have used the stale cursor, got args=%+v", args)
	}
}

// TestWatcherTickUsesAFullFetchTheFirstTimeThenResumesFromTheCursor checks
// the two argument shapes end to end: no cursor yet means a plain recent
// query, and a persisted cursor is passed back as since_history_id on the
// next tick.
func TestWatcherTickUsesAFullFetchTheFirstTimeThenResumesFromTheCursor(t *testing.T) {
	r := newRig(t)
	r.connect(t)
	r.mbox.output = rawOutput(t, "42")
	w := newWatcher(r, &fakeClassifier{}, "")

	w.Tick(context.Background())
	if args := r.mbox.lastArgs(); args["since_history_id"] != nil || args["query"] == nil {
		t.Fatalf("first tick args = %+v, want a plain query, no since_history_id", args)
	}

	r.mbox.output = rawOutput(t, "43")
	w.Tick(context.Background())
	if args := r.mbox.lastArgs(); args["since_history_id"] != "42" {
		t.Fatalf("second tick args = %+v, want since_history_id=42", args)
	}
}
