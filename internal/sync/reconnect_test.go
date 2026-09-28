package sync_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"water/internal/connectors/fake"
	"water/internal/connectors/google/gapi"
	watersync "water/internal/sync"
	"water/internal/vault"
)

// TestReconnectNeededBacksOffUntilReconnected is the review finding: once
// Google refused the grant, every mail tick (60s) and events tick called the
// token endpoint again and logged the same line, forever. Now the state is
// sticky: logged once, no calls until a backoff probe or a new credential,
// cleared on the first success, and visible through ReconnectNeeded.
func TestReconnectNeededBacksOffUntilReconnected(t *testing.T) {
	r := newRig(t)
	r.connect(t)
	now := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	ref := watersync.New(watersync.Config{
		Gate: r.g, Vault: r.v, Store: r.st, Service: fake.MailService, Account: fake.MailAccount,
		EventsFunction: "fake_incr_events.list_events", MailFunction: "fake_incr_mail.list_messages",
		EventsArgs: cursorArgs, MailArgs: cursorArgs,
		Now:  func() time.Time { return now },
		Logf: r.logf,
	})
	ctx := context.Background()
	refused := fmt.Errorf("gmail: %w", gapi.ErrReconnect)

	r.incrMail.setFailWith(refused)
	ref.RunOnceMail(ctx)
	if need, since := ref.ReconnectNeeded(); !need || !since.Equal(now) {
		t.Fatalf("ReconnectNeeded = %v, %v; want true since %v", need, since, now)
	}
	if n := len(r.logLines()); n != 1 || !strings.Contains(r.logLines()[0], "water connect google") {
		t.Fatalf("log = %v, want one reconnect line", r.logLines())
	}

	// Further ticks inside the backoff make no call at all, and log nothing.
	for i := 0; i < 4; i++ { // the first probe is due 5 minutes in
		now = now.Add(time.Minute)
		ref.RunOnceMail(ctx)
		ref.RunOnceEvents(ctx)
	}
	if m, e := r.incrMail.callCount(), r.incrEvents.callCount(); m != 1 || e != 0 {
		t.Fatalf("calls during backoff: mail=%d events=%d, want 1 and 0", m, e)
	}
	if n := len(r.logLines()); n != 1 {
		t.Fatalf("log during backoff = %v, want still one line", r.logLines())
	}

	// A due probe goes out; still refused, still quiet.
	now = now.Add(10 * time.Minute)
	r.incrMail.setFailWith(refused)
	ref.RunOnceMail(ctx)
	if m := r.incrMail.callCount(); m != 2 {
		t.Fatalf("mail calls after the backoff = %d, want one probe (2 total)", m)
	}
	if n := len(r.logLines()); n != 1 {
		t.Fatalf("log after a refused probe = %v, want still one line", r.logLines())
	}

	// `water connect google` stores a new credential: the next tick tries
	// at once, succeeds, and clears the state.
	if err := r.v.Set(fake.MailService, fake.MailAccount, vault.NewSecret("tok-new")); err != nil {
		t.Fatal(err)
	}
	ref.RunOnceMail(ctx)
	if m := r.incrMail.callCount(); m != 3 {
		t.Fatalf("mail calls after reconnect = %d, want 3", m)
	}
	if need, _ := ref.ReconnectNeeded(); need {
		t.Fatal("ReconnectNeeded still true after a successful call")
	}
	ref.RunOnceEvents(ctx)
	if e := r.incrEvents.callCount(); e != 1 {
		t.Fatalf("events calls after reconnect = %d, want 1", e)
	}
}
