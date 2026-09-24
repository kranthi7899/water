package sync_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/connectors"
	"water/internal/connectors/fake"
	"water/internal/gate"
	"water/internal/gate/permit"
	"water/internal/store"
	watersync "water/internal/sync"
	"water/internal/twins"
	"water/internal/vault"
)

const testManifest = `
id: sync-test
name: Sync test twin
usage: {window: 1h, model_calls: 10}
connectors:
  - name: fake_calendar
    functions:
      - {name: list_events, level: R}
  - name: fake_mail
    functions:
      - {name: list_messages, level: R}
  - name: fake_incr_events
    functions:
      - {name: list_events, level: R}
  - name: fake_incr_mail
    functions:
      - {name: list_messages, level: R}
auto_allowlist:
  - fake_calendar.list_events
  - fake_mail.list_messages
  - fake_incr_events.list_events
  - fake_incr_mail.list_messages
`

// errFakeExpired is the fake incremental connectors' stand-in for
// gcal.ErrSyncTokenExpired / gmail.ErrHistoryTooOld.
var errFakeExpired = errors.New("fake: cursor expired")

// incrConnector is a minimal connectors.Connector for cursor tests: Invoke
// records the args it was called with and returns a caller-set JSON object,
// or a caller-set error exactly once.
type incrConnector struct {
	name, fn string

	mu       sync.Mutex
	calls    int
	lastArgs map[string]any
	failWith error // consumed (cleared) by the next Invoke
	output   map[string]any
}

func (c *incrConnector) Name() string                 { return c.name }
func (c *incrConnector) Credential() (string, string) { return "", "" }
func (c *incrConnector) Functions() []connectors.Function {
	return []connectors.Function{{
		Name: c.fn, Level: twins.R, Risk: connectors.RiskLow,
		Schema: connectors.Schema{Properties: map[string]connectors.Property{"cursor": {Type: "string"}}},
	}}
}

func (c *incrConnector) Invoke(_ context.Context, p permit.Permit) (json.RawMessage, error) {
	call, err := p.Open()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.lastArgs = call.Args
	if c.failWith != nil {
		err := c.failWith
		c.failWith = nil
		return nil, err
	}
	return json.Marshal(c.output)
}

func (c *incrConnector) Normalize(string, json.RawMessage) ([]store.Record, error) { return nil, nil }

func (c *incrConnector) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *incrConnector) args() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastArgs
}

func (c *incrConnector) setFailWith(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failWith = err
}

func (c *incrConnector) setOutput(v map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.output = v
}

// cursorArgs builds args the way the real connectors' defaults do: a "cursor"
// key when resuming, nothing when doing a full fetch.
func cursorArgs(_ time.Time, cursor string) map[string]any {
	if cursor == "" {
		return map[string]any{}
	}
	return map[string]any{"cursor": cursor}
}

type rig struct {
	g          *gate.Gate
	v          *vault.MemoryVault
	log        *audit.Log
	st         *store.Store
	incrEvents *incrConnector
	incrMail   *incrConnector
	lines      []string
	mu         sync.Mutex
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	r := &rig{
		incrEvents: &incrConnector{name: "fake_incr_events", fn: "list_events"},
		incrMail:   &incrConnector{name: "fake_incr_mail", fn: "list_messages"},
	}
	var err error
	if r.st, err = store.Open(filepath.Join(dir, "water.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.st.Close() })
	if r.log, err = audit.Open(filepath.Join(dir, "audit.jsonl")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.log.Close() })
	q := approvals.NewQueue(r.st, r.log)
	reg, err := connectors.NewRegistry(
		fake.NewCalendar(), fake.NewMail(fake.Message{ID: "m1", From: "dana@acme.com", Subject: "Hi", Body: "hello"}),
		r.incrEvents, r.incrMail,
	)
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(testManifest))
	if err != nil {
		t.Fatal(err)
	}
	r.v = vault.NewMemory()
	if r.g, err = gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: r.log, Vault: r.v, Store: r.st}); err != nil {
		t.Fatal(err)
	}
	return r
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

// noArgs matches fake_calendar.list_events and fake_mail.list_messages,
// whose (deliberately minimal) schemas accept no properties at all — unlike
// gcal/gmail's real functions, which need a time window and a search query.
func noArgs(time.Time, string) map[string]any { return map[string]any{} }

// connect seeds the credential Refresher's connected-check looks for.
func (r *rig) connect(t *testing.T) {
	t.Helper()
	if err := r.v.Set(fake.MailService, fake.MailAccount, vault.NewSecret("tok-123")); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) auditIsEmpty(t *testing.T) bool {
	t.Helper()
	b, err := os.ReadFile(r.log.Path())
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b)) == ""
}

// TestRunOnceSkipsQuietlyWhenNotConnected is the "no credential present"
// case: RunOnce must neither call the gate (so the audit log stays empty)
// nor panic, and it logs one skip line per function (events and mail refresh
// independently, so each reports its own skip).
func TestRunOnceSkipsQuietlyWhenNotConnected(t *testing.T) {
	r := newRig(t)
	ref := watersync.New(watersync.Config{
		Gate: r.g, Vault: r.v, Service: fake.MailService, Account: fake.MailAccount,
		EventsFunction: "fake_calendar.list_events", MailFunction: "fake_mail.list_messages",
		EventsArgs: noArgs, MailArgs: noArgs,
		Logf: r.logf,
	})
	ref.RunOnce(context.Background())
	if !r.auditIsEmpty(t) {
		t.Fatal("RunOnce called the gate despite no stored credential")
	}
	lines := r.logLines()
	if len(lines) != 2 {
		t.Fatalf("log lines = %v, want exactly two skip lines (events, mail)", lines)
	}
	for _, l := range lines {
		if !strings.Contains(l, "skipping") {
			t.Errorf("line %q does not mention skipping", l)
		}
	}
}

// TestRunOnceSyncsBothFunctionsWhenConnected seeds the credential the
// refresher checks for, then expects both the events and mail functions to
// have gone through the gate (audit entries recorded) with no error logged.
func TestRunOnceSyncsBothFunctionsWhenConnected(t *testing.T) {
	r := newRig(t)
	r.connect(t)
	ref := watersync.New(watersync.Config{
		Gate: r.g, Vault: r.v, Service: fake.MailService, Account: fake.MailAccount,
		EventsFunction: "fake_calendar.list_events", MailFunction: "fake_mail.list_messages",
		EventsArgs: noArgs, MailArgs: noArgs,
		Logf: r.logf,
	})
	ref.RunOnce(context.Background())
	if lines := r.logLines(); len(lines) != 0 {
		t.Fatalf("unexpected log lines: %v", lines)
	}
	if r.auditIsEmpty(t) {
		t.Fatal("RunOnce did not call the gate despite a stored credential")
	}
	if _, err := audit.Verify(r.log.Path()); err != nil {
		t.Fatal(err)
	}
}

// TestRunStopsOnContextCancellation is the "stop on shutdown" requirement:
// Run must return promptly once its context is cancelled, even mid-interval.
func TestRunStopsOnContextCancellation(t *testing.T) {
	r := newRig(t)
	r.connect(t)
	ref := watersync.New(watersync.Config{
		Gate: r.g, Vault: r.v, Service: fake.MailService, Account: fake.MailAccount,
		EventsFunction: "fake_calendar.list_events", MailFunction: "fake_mail.list_messages",
		EventsArgs: noArgs, MailArgs: noArgs,
		EventsInterval: time.Millisecond, MailInterval: time.Millisecond, Logf: r.logf,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ref.Run(ctx)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond) // let it tick a few times
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after its context was cancelled")
	}
}

// TestTwoIntervalsFireIndependently is the split-loop requirement: a fast
// mail interval and a slow events interval must not couple to each other's
// pace.
func TestTwoIntervalsFireIndependently(t *testing.T) {
	r := newRig(t)
	r.connect(t)
	ref := watersync.New(watersync.Config{
		Gate: r.g, Vault: r.v, Store: r.st, Service: fake.MailService, Account: fake.MailAccount,
		EventsFunction: "fake_incr_events.list_events", MailFunction: "fake_incr_mail.list_messages",
		EventsArgs: cursorArgs, MailArgs: cursorArgs,
		EventsExpiredErr: errFakeExpired, MailExpiredErr: errFakeExpired,
		MailInterval: 5 * time.Millisecond, EventsInterval: 60 * time.Millisecond,
		Logf: r.logf,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ref.Run(ctx)
		close(done)
	}()
	time.Sleep(130 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after its context was cancelled")
	}
	mailCalls, eventCalls := r.incrMail.callCount(), r.incrEvents.callCount()
	if mailCalls <= eventCalls {
		t.Fatalf("mail (interval 5ms) ticked %d times, events (interval 60ms) ticked %d times; want mail to fire far more often", mailCalls, eventCalls)
	}
	if eventCalls < 1 {
		t.Fatalf("events never ticked")
	}
}

// TestEventsCursorPersistedAfterSuccessAndReused checks the incremental
// contract end to end: a first call with no stored cursor persists the one
// the connector reports, and the next call passes that cursor back.
func TestEventsCursorPersistedAfterSuccessAndReused(t *testing.T) {
	r := newRig(t)
	r.connect(t)
	r.incrEvents.setOutput(map[string]any{"next_sync_token": "tok-1"})
	ref := watersync.New(watersync.Config{
		Gate: r.g, Vault: r.v, Store: r.st, Service: fake.MailService, Account: fake.MailAccount,
		EventsFunction: "fake_incr_events.list_events", MailFunction: "fake_mail.list_messages",
		EventsArgs: cursorArgs, MailArgs: noArgs,
		EventsExpiredErr: errFakeExpired, MailExpiredErr: errFakeExpired,
		Logf: r.logf,
	})
	ctx := context.Background()
	ref.RunOnceEvents(ctx)

	if args := r.incrEvents.args(); args["cursor"] != nil {
		t.Fatalf("first call should be a full fetch (no cursor), got args=%v", args)
	}
	v, ok, err := r.st.GetCursor(ctx, "gcal:primary:sync_token")
	if err != nil || !ok || v != "tok-1" {
		t.Fatalf("cursor after first success = %q ok=%v err=%v, want tok-1", v, ok, err)
	}

	r.incrEvents.setOutput(map[string]any{"next_sync_token": "tok-2"})
	ref.RunOnceEvents(ctx)
	if args := r.incrEvents.args(); args["cursor"] != "tok-1" {
		t.Fatalf("second call args = %v, want cursor=tok-1 (resumed from the persisted cursor)", args)
	}
	v, ok, err = r.st.GetCursor(ctx, "gcal:primary:sync_token")
	if err != nil || !ok || v != "tok-2" {
		t.Fatalf("cursor after second success = %q ok=%v err=%v, want tok-2", v, ok, err)
	}
}

// TestMailCursorPersistedAfterSuccess mirrors the events case for the mail
// side (gmail:history_id), through RunOnceMail.
func TestMailCursorPersistedAfterSuccess(t *testing.T) {
	r := newRig(t)
	r.connect(t)
	r.incrMail.setOutput(map[string]any{"history_id": "555"})
	ref := watersync.New(watersync.Config{
		Gate: r.g, Vault: r.v, Store: r.st, Service: fake.MailService, Account: fake.MailAccount,
		EventsFunction: "fake_calendar.list_events", MailFunction: "fake_incr_mail.list_messages",
		EventsArgs: noArgs, MailArgs: cursorArgs,
		EventsExpiredErr: errFakeExpired, MailExpiredErr: errFakeExpired,
		Logf: r.logf,
	})
	ctx := context.Background()
	ref.RunOnceMail(ctx)
	v, ok, err := r.st.GetCursor(ctx, "gmail:history_id")
	if err != nil || !ok || v != "555" {
		t.Fatalf("cursor after success = %q ok=%v err=%v, want 555", v, ok, err)
	}
}

// TestExpiredCursorIsClearedAndNextTickReseeds is the fallback contract: a
// sentinel "cursor expired" error deletes the stored cursor (surfacing
// through the gate's returned error via errors.Is, per gate.go's
// scrubbedError), and the very next tick — now cursor-less — does a full
// fetch and re-establishes a fresh cursor from that response.
func TestExpiredCursorIsClearedAndNextTickReseeds(t *testing.T) {
	r := newRig(t)
	r.connect(t)
	ctx := context.Background()
	if err := r.st.SetCursor(ctx, "gcal:primary:sync_token", "stale-token"); err != nil {
		t.Fatal(err)
	}
	r.incrEvents.setFailWith(errFakeExpired)
	ref := watersync.New(watersync.Config{
		Gate: r.g, Vault: r.v, Store: r.st, Service: fake.MailService, Account: fake.MailAccount,
		EventsFunction: "fake_incr_events.list_events", MailFunction: "fake_mail.list_messages",
		EventsArgs: cursorArgs, MailArgs: noArgs,
		EventsExpiredErr: errFakeExpired, MailExpiredErr: errFakeExpired,
		Logf: r.logf,
	})

	ref.RunOnceEvents(ctx)
	if args := r.incrEvents.args(); args["cursor"] != "stale-token" {
		t.Fatalf("expected the stale cursor to have been sent once, got args=%v", args)
	}
	if _, ok, _ := r.st.GetCursor(ctx, "gcal:primary:sync_token"); ok {
		t.Fatal("expired cursor should have been deleted")
	}
	found := false
	for _, l := range r.logLines() {
		if strings.Contains(l, "expired") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a log line about the expired cursor, got %v", r.logLines())
	}

	// Next tick: cursor-less (full fetch), and it re-seeds a fresh one.
	r.incrEvents.setOutput(map[string]any{"next_sync_token": "fresh-token"})
	ref.RunOnceEvents(ctx)
	if args := r.incrEvents.args(); args["cursor"] != nil {
		t.Fatalf("fallback tick should be a full fetch (no cursor), got args=%v", args)
	}
	v, ok, err := r.st.GetCursor(ctx, "gcal:primary:sync_token")
	if err != nil || !ok || v != "fresh-token" {
		t.Fatalf("cursor after reseed = %q ok=%v err=%v, want fresh-token", v, ok, err)
	}
}

// TestBackgroundBriefPrecomputeFiresOncePastReadyAfter covers the events
// tick's brief hook: it does nothing before BriefReadyAfter or once a brief
// is already cached, and calls Config.Brief exactly once otherwise.
func TestBackgroundBriefPrecomputeFiresOncePastReadyAfter(t *testing.T) {
	r := newRig(t)
	r.connect(t)
	ctx := context.Background()
	var briefCalls int
	before := time.Date(2026, 9, 24, 6, 0, 0, 0, time.UTC)
	after := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	now := before
	ref := watersync.New(watersync.Config{
		Gate: r.g, Vault: r.v, Store: r.st, Service: fake.MailService, Account: fake.MailAccount,
		EventsFunction: "fake_calendar.list_events", MailFunction: "fake_mail.list_messages",
		EventsArgs: noArgs, MailArgs: noArgs,
		Now:             func() time.Time { return now },
		BriefReadyAfter: "07:00",
		Brief: func(ctx context.Context) error {
			briefCalls++
			return r.st.SetBrief(ctx, now.Format("2006-01-02"), "computed")
		},
		Logf: r.logf,
	})

	ref.RunOnceEvents(ctx)
	if briefCalls != 0 {
		t.Fatalf("brief computed before ready_after: calls=%d", briefCalls)
	}

	now = after
	ref.RunOnceEvents(ctx)
	if briefCalls != 1 {
		t.Fatalf("brief calls after first past-ready tick = %d, want 1", briefCalls)
	}

	// Already cached: a second past-ready tick must not recompute.
	ref.RunOnceEvents(ctx)
	if briefCalls != 1 {
		t.Fatalf("brief calls after second past-ready tick = %d, want still 1 (already cached)", briefCalls)
	}
}

// TestTruncatedOutputIsLoggedAndKeepsCursor: a connector output flagged
// truncated (gcal's windowed seed cut short by max) carries no cursor, so
// the stored cursor stays put, and the tick says so in the log instead of
// failing silently.
func TestTruncatedOutputIsLoggedAndKeepsCursor(t *testing.T) {
	r := newRig(t)
	r.connect(t)
	r.incrEvents.setOutput(map[string]any{"events": []any{}, "truncated": true})
	ref := watersync.New(watersync.Config{
		Gate: r.g, Vault: r.v, Store: r.st, Service: fake.MailService, Account: fake.MailAccount,
		EventsFunction: "fake_incr_events.list_events", MailFunction: "fake_mail.list_messages",
		EventsArgs: cursorArgs, MailArgs: noArgs,
		EventsExpiredErr: errFakeExpired, MailExpiredErr: errFakeExpired,
		Logf: r.logf,
	})
	ctx := context.Background()
	ref.RunOnceEvents(ctx)
	if _, ok, err := r.st.GetCursor(ctx, "gcal:primary:sync_token"); err != nil || ok {
		t.Fatalf("a truncated output must not set a cursor: ok=%v err=%v", ok, err)
	}
	found := false
	for _, l := range r.logLines() {
		if strings.Contains(l, "truncated") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no log line reports the truncation: %v", r.logLines())
	}
}
