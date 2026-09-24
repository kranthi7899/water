package sync_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/connectors"
	"water/internal/connectors/fake"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/sync"
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
auto_allowlist:
  - fake_calendar.list_events
  - fake_mail.list_messages
`

type rig struct {
	g     *gate.Gate
	v     *vault.MemoryVault
	log   *audit.Log
	st    *store.Store
	lines []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	r := &rig{}
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
	reg, err := connectors.NewRegistry(fake.NewCalendar(), fake.NewMail(fake.Message{ID: "m1", From: "dana@acme.com", Subject: "Hi", Body: "hello"}))
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
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

// noArgs matches fake_calendar.list_events and fake_mail.list_messages,
// whose (deliberately minimal) schemas accept no properties at all — unlike
// gcal/gmail's real functions, which need a time window and a search query.
func noArgs(time.Time) map[string]any { return map[string]any{} }

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
// nor panic, and it logs exactly one line explaining why.
func TestRunOnceSkipsQuietlyWhenNotConnected(t *testing.T) {
	r := newRig(t)
	ref := sync.New(sync.Config{
		Gate: r.g, Vault: r.v, Service: fake.MailService, Account: fake.MailAccount,
		EventsFunction: "fake_calendar.list_events", MailFunction: "fake_mail.list_messages",
		EventsArgs: noArgs, MailArgs: noArgs,
		Logf: r.logf,
	})
	ref.RunOnce(context.Background())
	if !r.auditIsEmpty(t) {
		t.Fatal("RunOnce called the gate despite no stored credential")
	}
	if len(r.lines) != 1 || !strings.Contains(r.lines[0], "skipping") {
		t.Fatalf("log lines = %v, want exactly one skip line", r.lines)
	}
}

// TestRunOnceSyncsBothFunctionsWhenConnected seeds the credential the
// refresher checks for, then expects both the events and mail functions to
// have gone through the gate (audit entries recorded) with no error logged.
func TestRunOnceSyncsBothFunctionsWhenConnected(t *testing.T) {
	r := newRig(t)
	if err := r.v.Set(fake.MailService, fake.MailAccount, vault.NewSecret("tok-123")); err != nil {
		t.Fatal(err)
	}
	ref := sync.New(sync.Config{
		Gate: r.g, Vault: r.v, Service: fake.MailService, Account: fake.MailAccount,
		EventsFunction: "fake_calendar.list_events", MailFunction: "fake_mail.list_messages",
		EventsArgs: noArgs, MailArgs: noArgs,
		Logf: r.logf,
	})
	ref.RunOnce(context.Background())
	if len(r.lines) != 0 {
		t.Fatalf("unexpected log lines: %v", r.lines)
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
	if err := r.v.Set(fake.MailService, fake.MailAccount, vault.NewSecret("tok-123")); err != nil {
		t.Fatal(err)
	}
	ref := sync.New(sync.Config{
		Gate: r.g, Vault: r.v, Service: fake.MailService, Account: fake.MailAccount,
		EventsFunction: "fake_calendar.list_events", MailFunction: "fake_mail.list_messages",
		EventsArgs: noArgs, MailArgs: noArgs,
		Interval: time.Millisecond, Logf: r.logf,
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
