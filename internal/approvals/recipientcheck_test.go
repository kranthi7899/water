package approvals

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"water/internal/audit"
	"water/internal/store"
)

// fakeResolver answers MX/host lookups from maps; a domain in neither is
// not found. panicOnUse proves a path never touches DNS.
type fakeResolver struct {
	mu         sync.Mutex
	mx         map[string][]*net.MX
	hosts      map[string][]string
	fail       map[string]bool
	calls      int
	panicOnUse bool
}

func (f *fakeResolver) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.panicOnUse {
		panic("DNS lookup on a path that must not resolve: " + name)
	}
	f.calls++
	if f.fail[name] {
		return nil, &net.DNSError{Err: "timeout", Name: name, IsTimeout: true}
	}
	if mx, ok := f.mx[name]; ok {
		return mx, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f *fakeResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.panicOnUse {
		panic("DNS lookup on a path that must not resolve: " + host)
	}
	f.calls++
	if h, ok := f.hosts[host]; ok {
		return h, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func (f *fakeResolver) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newTestQueue(t *testing.T) (*Queue, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	auditPath := filepath.Join(dir, "audit.jsonl")
	log, err := audit.Open(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	return NewQueue(st, log), auditPath
}

// incidentPayload is the exact send the 2026-09-26 incident approved and
// that bounced: "at the rate" misheard as "at the right" and glued on.
func incidentPayload() map[string]any {
	return map[string]any{"to": []any{"kranthetjob@therightgmail.com"}, "subject": "Job search", "body": "Hi"}
}

func sendEnv(p map[string]any) Envelope {
	return Envelope{Action: "gmail.send_message", Payload: p, Origin: "p0", Risk: "high"}
}

// The fail-before proof for D4c: before recipient checks, this proposed an
// envelope (and it was tapped, sent and bounced). Now Propose refuses it,
// with a message the model can act on, and nothing is queued or audited.
func TestProposeRefusesIncidentNearMiss(t *testing.T) {
	q, auditPath := newTestQueue(t)
	q.SetRecipientChecker(NewRecipientChecker(&fakeResolver{panicOnUse: true}, nil))
	before, _ := os.ReadFile(auditPath)

	_, err := q.Propose(context.Background(), sendEnv(incidentPayload()))
	if !errors.Is(err, ErrRecipient) {
		t.Fatalf("Propose(incident) err = %v, want ErrRecipient", err)
	}
	for _, want := range []string{"therightgmail.com", `"gmail.com"`, "spelled out", "confirm_unusual_recipient"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err, want)
		}
	}
	pend, err := q.Pending(context.Background())
	if err != nil || len(pend) != 0 {
		t.Fatalf("Pending = %v, %v; want an empty queue", pend, err)
	}
	after, _ := os.ReadFile(auditPath)
	if string(after) != string(before) {
		t.Fatalf("a refused proposal was audited: %s", after[len(before):])
	}
}

// Without any checker attached (every test default), the pure checks still
// refuse the incident address, and nothing resolves.
func TestProposeNilCheckerStillRunsPureChecks(t *testing.T) {
	q, _ := newTestQueue(t)
	if _, err := q.Propose(context.Background(), sendEnv(incidentPayload())); !errors.Is(err, ErrRecipient) {
		t.Fatalf("err = %v, want ErrRecipient", err)
	}
	for _, bad := range []any{"kranthi at gmail", []any{"a@b"}, []any{"ok@fenwick.io", "gmial.com"}} {
		if _, err := q.Propose(context.Background(), sendEnv(map[string]any{"to": bad, "subject": "s", "body": "b"})); !errors.Is(err, ErrRecipient) {
			t.Errorf("to=%v: err = %v, want ErrRecipient", bad, err)
		}
	}
	// cc and bcc are checked too, and the draft functions as well.
	for _, action := range []string{"gmail.draft_message", "gmail.draft_for_review"} {
		e := Envelope{Action: action, Origin: "p0", Payload: map[string]any{"to": []any{"ok@gmail.com"}, "cc": []any{"x@hotmial.com"}}}
		if _, err := q.Propose(context.Background(), e); !errors.Is(err, ErrRecipient) {
			t.Errorf("%s with near-miss cc: err = %v, want ErrRecipient", action, err)
		}
	}
	// Other actions are untouched.
	if _, err := q.Propose(context.Background(), Envelope{Action: "gcal.create_event", Origin: "p0",
		Payload: map[string]any{"title": "x", "attendees": []any{"a@gmial.com"}}}); err != nil {
		t.Fatalf("gcal envelope: %v", err)
	}
	e, err := q.Propose(context.Background(), sendEnv(map[string]any{"to": []any{"Dana <dana@fenwick.io>"}, "subject": "s", "body": "b"}))
	if err != nil || len(e.Warnings) != 0 {
		t.Fatalf("valid send: %+v, %v; want proposed with no warnings", e, err)
	}
}

func TestProposeOverrideTurnsNearMissIntoWarning(t *testing.T) {
	q, _ := newTestQueue(t)
	r := &fakeResolver{mx: map[string][]*net.MX{"therightgmail.com": {{Host: "mx.therightgmail.com."}}}}
	q.SetRecipientChecker(NewRecipientChecker(r, nil))
	p := incidentPayload()
	p["confirm_unusual_recipient"] = true
	e, err := q.Propose(context.Background(), sendEnv(p))
	if err != nil {
		t.Fatalf("Propose with override: %v", err)
	}
	if len(e.Warnings) != 1 || !strings.Contains(e.Warnings[0], "Unusual domain therightgmail.com (close to gmail.com)") {
		t.Fatalf("Warnings = %q", e.Warnings)
	}
	// The override does not excuse bad syntax.
	bad := map[string]any{"to": []any{"kranthi@gmail"}, "subject": "s", "body": "b", "confirm_unusual_recipient": true}
	if _, err := q.Propose(context.Background(), sendEnv(bad)); !errors.Is(err, ErrRecipient) {
		t.Fatalf("override with bad syntax: err = %v, want ErrRecipient", err)
	}
}

func TestNoMXIsAWarningOnEveryRead(t *testing.T) {
	q, _ := newTestQueue(t)
	r := &fakeResolver{}
	q.SetRecipientChecker(NewRecipientChecker(r, nil))
	ctx := context.Background()
	e, err := q.Propose(ctx, sendEnv(map[string]any{"to": []any{"dana@no-mail-here.io"}, "subject": "Hi", "body": "b"}))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	const want = "no-mail-here.io has no mail server; the message would bounce"
	if len(e.Warnings) != 1 || e.Warnings[0] != want {
		t.Fatalf("Propose Warnings = %q, want [%q]", e.Warnings, want)
	}
	got, err := q.Get(ctx, e.ID)
	if err != nil || len(got.Warnings) != 1 || got.Warnings[0] != want {
		t.Fatalf("Get Warnings = %q, %v", got.Warnings, err)
	}
	pend, err := q.Pending(ctx)
	if err != nil || len(pend) != 1 || len(pend[0].Warnings) != 1 {
		t.Fatalf("Pending = %+v, %v", pend, err)
	}
	rb := ReadBack(got)
	if !strings.Contains(rb, "Warning: "+want+".") || strings.Index(rb, "Warning:") > strings.Index(rb, "Say yes") {
		t.Fatalf("ReadBack %q: want the warning before the question", rb)
	}
	// Warnings are not in the hash: the stored envelope still verifies.
	if got.PayloadHash != e.PayloadHash {
		t.Fatalf("hash changed across reads")
	}
	// Reads come from the cache: one MX + one host lookup at Propose only.
	if n := r.count(); n != 2 {
		t.Fatalf("resolver calls = %d, want 2 (MX then host, at Propose only)", n)
	}
	// A decided envelope no longer carries warnings.
	d, err := q.Decide(ctx, e.ID, No)
	if err != nil || len(d.Warnings) != 0 {
		t.Fatalf("decided envelope: %+v, %v", d.Warnings, err)
	}
}

func TestImplicitMXAndNullMXAndLookupFailure(t *testing.T) {
	r := &fakeResolver{
		hosts: map[string][]string{"a-record.io": {"192.0.2.1"}},
		mx:    map[string][]*net.MX{"null-mx.io": {{Host: "."}}, "ok.io": {{Host: "mx.ok.io."}}},
		fail:  map[string]bool{"slow.io": true},
	}
	c := NewRecipientChecker(r, nil)
	cases := map[string]string{
		"ok.io":       "",
		"a-record.io": "",
		"null-mx.io":  "null-mx.io has no mail server; the message would bounce",
		"slow.io":     "Couldn't verify the mail domain slow.io",
	}
	for d, want := range cases {
		w, err := c.Check(context.Background(), "gmail.send_message", map[string]any{"to": []any{"x@" + d}})
		if err != nil {
			t.Fatalf("%s: %v", d, err)
		}
		if (want == "" && len(w) != 0) || (want != "" && (len(w) != 1 || w[0] != want)) {
			t.Errorf("%s: warnings %q, want %q", d, w, want)
		}
	}
	// Public providers are never looked up.
	n := r.count()
	if _, err := c.Check(context.Background(), "gmail.send_message", map[string]any{"to": []any{"x@gmail.com", "y@outlook.com"}}); err != nil {
		t.Fatal(err)
	}
	if r.count() != n {
		t.Fatalf("a public provider was resolved")
	}
}

// After a daemon restart the MX cache is empty: a pending envelope reads
// "couldn't verify yet" (fail closed) and a background lookup heals it.
func TestCacheMissAfterRestartWarmsUp(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	r := &fakeResolver{mx: map[string][]*net.MX{"fenwick.io": {{Host: "mx.fenwick.io."}}}}
	q.SetRecipientChecker(NewRecipientChecker(r, nil))
	e, err := q.Propose(ctx, sendEnv(map[string]any{"to": []any{"dana@fenwick.io"}, "subject": "s", "body": "b"}))
	if err != nil || len(e.Warnings) != 0 {
		t.Fatalf("Propose: %+v, %v", e.Warnings, err)
	}
	restarted := NewRecipientChecker(r, nil)
	q.SetRecipientChecker(restarted)
	got, err := q.Get(ctx, e.ID)
	if err != nil || len(got.Warnings) != 1 || got.Warnings[0] != "Couldn't verify the mail domain fenwick.io yet" {
		t.Fatalf("after restart: %q, %v", got.Warnings, err)
	}
	restarted.wait()
	got, err = q.Get(ctx, e.ID)
	if err != nil || len(got.Warnings) != 0 {
		t.Fatalf("after warm-up: %q, %v; want none", got.Warnings, err)
	}
}

func TestEditRefusedLeavesOldEnvelopeStanding(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	e, err := q.Propose(ctx, sendEnv(map[string]any{"to": []any{"dana@gmail.com"}, "subject": "s", "body": "b"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Edit(ctx, e.ID, incidentPayload()); !errors.Is(err, ErrRecipient) {
		t.Fatalf("Edit to incident address: err = %v, want ErrRecipient", err)
	}
	got, err := q.Get(ctx, e.ID)
	if err != nil || got.Status != Pending {
		t.Fatalf("old envelope after refused edit: %s, %v; want still pending", got.Status, err)
	}
}

func TestInternalDomainsAreKnown(t *testing.T) {
	c := NewRecipientChecker(nil, []string{" Renaissance.AI "})
	if _, err := c.Check(context.Background(), "gmail.send_message", map[string]any{"to": []any{"dev@renaisance.ai"}}); !errors.Is(err, ErrRecipient) {
		t.Fatalf("near-miss of the company domain: err = %v", err)
	}
	if w, err := c.Check(context.Background(), "gmail.send_message", map[string]any{"to": []any{"dev@renaissance.ai"}}); err != nil || len(w) != 0 {
		t.Fatalf("company domain: %q, %v", w, err)
	}
}

func TestRecipientsOf(t *testing.T) {
	p := map[string]any{"to": []any{"A <a@x.io>", " "}, "cc": "c@x.io", "bcc": []string{"b@x.io"}}
	got := RecipientsOf("gmail.send_message", p)
	if strings.Join(got, ",") != "a@x.io,c@x.io,b@x.io" {
		t.Fatalf("RecipientsOf = %v", got)
	}
	if RecipientsOf("twinlink.send_message", p) != nil || RecipientsOf("gcal.create_event", p) != nil {
		t.Fatal("non-gmail actions must have no recipients to check")
	}
}

func TestReadBackWithoutWarningsUnchanged(t *testing.T) {
	e := Envelope{Action: "gmail.send_message", Payload: map[string]any{"to": []any{"a@x.io"}, "subject": "s", "body": "b"}}
	if strings.Contains(ReadBack(e), "Warning") {
		t.Fatal("warning line without warnings")
	}
	e.Warnings = []string{"line\none.", "two"}
	if rb := ReadBack(e); !strings.Contains(rb, "Warning: line one. two. Say yes") {
		t.Fatalf("ReadBack = %q", rb)
	}
}
