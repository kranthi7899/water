package sync_test

import (
	"context"
	"encoding/json"
	"path/filepath"
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

const prefetchManifest = `
id: prefetch-test
name: Prefetch test twin
usage: {window: 1h, model_calls: 10}
connectors:
  - name: fake_calendar
    functions:
      - {name: list_events, level: R}
  - name: fake_mail
    functions:
      - {name: list_messages, level: R}
  - name: fake_pf_mail
    functions:
      - {name: search, level: R}
  - name: fake_pf_docs
    functions:
      - {name: search, level: R}
`

// queryConnector is a minimal R-level connector taking one "query" string
// argument and recording every call it receives, so a test can assert
// exactly what prefetch asked it for.
type queryConnector struct {
	name string

	mu    sync.Mutex
	calls []map[string]any
}

func (c *queryConnector) Name() string                 { return c.name }
func (c *queryConnector) Credential() (string, string) { return "", "" }
func (c *queryConnector) Functions() []connectors.Function {
	return []connectors.Function{{
		Name: "search", Level: twins.R, Risk: connectors.RiskLow,
		Schema: connectors.Schema{Properties: map[string]connectors.Property{"query": {Type: "string"}}},
	}}
}
func (c *queryConnector) Invoke(_ context.Context, p permit.Permit) (json.RawMessage, error) {
	call, err := p.Open()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.calls = append(c.calls, call.Args)
	c.mu.Unlock()
	return json.RawMessage(`{}`), nil
}
func (c *queryConnector) Normalize(string, json.RawMessage) ([]store.Record, error) { return nil, nil }

func (c *queryConnector) callArgs() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]any(nil), c.calls...)
}

type pfRig struct {
	g          *gate.Gate
	v          *vault.MemoryVault
	st         *store.Store
	mail, docs *queryConnector
}

func newPFRig(t *testing.T) *pfRig {
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
	mail := &queryConnector{name: "fake_pf_mail"}
	docs := &queryConnector{name: "fake_pf_docs"}
	reg, err := connectors.NewRegistry(fake.NewCalendar(), fake.NewMail(), mail, docs)
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(prefetchManifest))
	if err != nil {
		t.Fatal(err)
	}
	v := vault.NewMemory()
	if err := v.Set(fake.MailService, fake.MailAccount, vault.NewSecret("tok-123")); err != nil {
		t.Fatal(err)
	}
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: v, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	return &pfRig{g: g, v: v, st: st, mail: mail, docs: docs}
}

func (r *pfRig) seedEvent(t *testing.T, id string, start time.Time, attendees []string, title string) {
	t.Helper()
	ev := &store.Event{Meta: store.Meta{Source: "gcal", SourceID: id}, Title: title, StartAt: start, EndAt: start.Add(time.Hour), Attendees: attendees}
	if err := r.st.Upsert(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
}

func pfConfig(r *pfRig, now time.Time) watersync.Config {
	return watersync.Config{
		Gate: r.g, Vault: r.v, Store: r.st, Service: fake.MailService, Account: fake.MailAccount,
		EventsFunction: "fake_calendar.list_events", MailFunction: "fake_mail.list_messages",
		EventsArgs: noArgs, MailArgs: noArgs,
		PrefetchMailFunction: "fake_pf_mail.search", PrefetchDocsFunction: "fake_pf_docs.search",
		PrefetchLeadTime: 10 * time.Minute,
		Now:              func() time.Time { return now },
		Logf:             func(string, ...any) {},
	}
}

// TestPrefetchPullsAttendeeMailAndLinkedDocsForSoonEvent is Slice M section
// 2's core case: an event starting inside the lead window gets its
// attendees' mail and a title-keyed Drive search pulled in, through the
// gate's ordinary R-level read path.
func TestPrefetchPullsAttendeeMailAndLinkedDocsForSoonEvent(t *testing.T) {
	r := newPFRig(t)
	now := time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)
	r.seedEvent(t, "evt-1", now.Add(5*time.Minute), []string{"priya@acme.com", "sam@acme.com"}, "Kafka budget review")
	ref := watersync.New(pfConfig(r, now))
	ref.RunOnceEvents(context.Background())

	mailCalls := r.mail.callArgs()
	if len(mailCalls) != 1 {
		t.Fatalf("mail prefetch calls = %d, want 1: %v", len(mailCalls), mailCalls)
	}
	if q, _ := mailCalls[0]["query"].(string); q != "from:priya@acme.com OR from:sam@acme.com" {
		t.Fatalf("mail query = %q", q)
	}
	docCalls := r.docs.callArgs()
	if len(docCalls) != 1 {
		t.Fatalf("docs prefetch calls = %d, want 1: %v", len(docCalls), docCalls)
	}
	if q, _ := docCalls[0]["query"].(string); q != "Kafka budget review" {
		t.Fatalf("docs query = %q", q)
	}
}

// TestPrefetchSkipsAnEventOutsideTheLeadWindow: an event further out than
// PrefetchLeadTime is left alone (the next tick, once it's closer, prefetches
// it).
func TestPrefetchSkipsAnEventOutsideTheLeadWindow(t *testing.T) {
	r := newPFRig(t)
	now := time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)
	r.seedEvent(t, "evt-far", now.Add(2*time.Hour), []string{"priya@acme.com"}, "Later meeting")
	ref := watersync.New(pfConfig(r, now))
	ref.RunOnceEvents(context.Background())
	if len(r.mail.callArgs()) != 0 || len(r.docs.callArgs()) != 0 {
		t.Fatalf("an event 2h out must not be prefetched yet: mail=%v docs=%v", r.mail.callArgs(), r.docs.callArgs())
	}
}

// TestPrefetchRunsAnEventOnceOnly: a second tick with the same event still
// inside the window must not prefetch it again.
func TestPrefetchRunsAnEventOnceOnly(t *testing.T) {
	r := newPFRig(t)
	now := time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)
	r.seedEvent(t, "evt-1", now.Add(5*time.Minute), []string{"priya@acme.com"}, "Budget review")
	ref := watersync.New(pfConfig(r, now))
	ref.RunOnceEvents(context.Background())
	ref.RunOnceEvents(context.Background())
	if len(r.mail.callArgs()) != 1 || len(r.docs.callArgs()) != 1 {
		t.Fatalf("event prefetched more than once: mail=%v docs=%v", r.mail.callArgs(), r.docs.callArgs())
	}
}

// TestPrefetchSkipsAttendeelessEventsMailButStillSearchesDocsByTitle: an
// event with no attendees has nothing to build a mail query from, but its
// title still drives a Drive search.
func TestPrefetchSkipsAttendeelessEventsMailButStillSearchesDocsByTitle(t *testing.T) {
	r := newPFRig(t)
	now := time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)
	r.seedEvent(t, "evt-solo", now.Add(5*time.Minute), nil, "Solo prep block")
	ref := watersync.New(pfConfig(r, now))
	ref.RunOnceEvents(context.Background())
	if len(r.mail.callArgs()) != 0 {
		t.Fatalf("no-attendee event should not query mail: %v", r.mail.callArgs())
	}
	if len(r.docs.callArgs()) != 1 {
		t.Fatalf("docs prefetch calls = %d, want 1", len(r.docs.callArgs()))
	}
}

// TestPrefetchNoopWithoutStore: Config.Store == nil disables prefetch
// entirely rather than panicking (mirrors the brief precompute's own guard).
func TestPrefetchNoopWithoutStore(t *testing.T) {
	r := newPFRig(t)
	now := time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)
	r.seedEvent(t, "evt-1", now.Add(5*time.Minute), []string{"priya@acme.com"}, "Budget review")
	cfg := pfConfig(r, now)
	cfg.Store = nil
	ref := watersync.New(cfg)
	ref.RunOnceEvents(context.Background())
	if len(r.mail.callArgs()) != 0 || len(r.docs.callArgs()) != 0 {
		t.Fatalf("prefetch ran with no Store configured: mail=%v docs=%v", r.mail.callArgs(), r.docs.callArgs())
	}
}
