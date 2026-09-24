package approvals

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"water/internal/audit"
	"water/internal/store"
)

func TestMatch(t *testing.T) {
	cases := map[string]Answer{
		"yes":                  Yes,
		"Yes.":                 Yes,
		"yeah send it":         Yes,
		"ok, go ahead":         Yes,
		"YES please":           Yes,
		"no":                   No,
		"No.":                  No,
		"don't send":           No,
		"don’t send it":        No,
		"do not send":          No,
		"cancel":               No,
		"yes, no wait":         Ambiguous,
		"no yes":               Ambiguous,
		"maybe":                Ambiguous,
		"":                     Ambiguous,
		"   ":                  Ambiguous,
		"yes but change it":    Ambiguous,
		"send it to bob":       Ambiguous,
		"yes send it to bob":   Ambiguous,
		"not yet":              Ambiguous,
		"isn't that too early": Ambiguous,
		"yesterday":            Ambiguous,
		"send it":              Ambiguous,
		"nothing":              Ambiguous,
	}
	for in, want := range cases {
		if got := Match(in); got != want {
			t.Errorf("Match(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestReadBackIsFromThePayload(t *testing.T) {
	e := Envelope{Action: "fake_mail.send_email", Recipient: "Dana Lee", Payload: map[string]any{
		"to": []any{"dana@acme.com"}, "subject": "Q3 budget",
		"body": "Confirmed. The Q3 numbers are final and\n\nI will circulate the board deck on Friday once finance signs off.",
	}}
	got := ReadBack(e)
	want := "Send email to Dana Lee <dana@acme.com>, subject 'Q3 budget'. Body begins: 'Confirmed. The Q3 numbers are final and I will circulate the board deck on…'. Say yes to send or no to cancel."
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	// A label cannot hide where mail actually goes.
	e.Payload["to"] = []any{"eve@evil.com", "dana@acme.com"}
	if got := ReadBack(e); !strings.Contains(got, "eve@evil.com, dana@acme.com") || strings.Contains(got, "Dana Lee") {
		t.Fatalf("multi-recipient read-back: %q", got)
	}
	ev := ReadBack(Envelope{Action: "fake_calendar.create_event", Payload: map[string]any{"title": "Board prep", "start": "2026-09-24T10:00:00Z", "attendees": []any{"a@x.com"}}})
	if ev != "Create event 'Board prep' starting 2026-09-24T10:00:00Z with a@x.com. Say yes to create it or no to cancel." {
		t.Fatalf("event: %q", ev)
	}
	other := ReadBack(Envelope{Action: "notes.save_note", Payload: map[string]any{"text": "hi", "pin": true}})
	if other != "Run notes.save_note with pin true; text hi. Say yes to proceed or no to cancel." {
		t.Fatalf("generic: %q", other)
	}
}

func TestQueueLifecycleAndMenu(t *testing.T) {
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
	q := NewQueue(st, log)
	now := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
	q.Now = func() time.Time { return now }
	ctx := context.Background()

	if got := Menu(nil); got != "No approvals waiting." {
		t.Fatal(got)
	}
	a, _ := q.Propose(ctx, Envelope{Action: "fake_mail.send_email", Payload: map[string]any{"to": []any{"a@x.com"}, "subject": "Hi", "body": "b"}, Origin: "p0", Risk: "high"})
	b, _ := q.Propose(ctx, Envelope{Action: "fake_calendar.create_event", Payload: map[string]any{"title": "Sync", "start": "10:00"}, Origin: "p1", ExpiresAt: now.Add(time.Minute)})
	pending, err := q.Pending(ctx)
	if err != nil || len(pending) != 2 {
		t.Fatalf("%d %v", len(pending), err)
	}
	menu := Menu(pending)
	if !strings.Contains(menu, "1. Send email to a@x.com") || !strings.Contains(menu, "2. Create event 'Sync'") || !strings.Contains(menu, "[risk high") {
		t.Fatalf("menu:\n%s", menu)
	}

	now = now.Add(2 * time.Minute)
	pending, _ = q.Pending(ctx)
	if len(pending) != 1 || pending[0].ID != a.ID {
		t.Fatalf("stale envelope still pending: %+v", pending)
	}
	if got, _ := q.Get(ctx, b.ID); got.Status != Expired {
		t.Fatalf("b is %s", got.Status)
	}
	if got, err := q.Respond(ctx, a.ID, "yes, no wait"); err != nil || got.Status != Denied {
		t.Fatalf("mixed reply: %+v %v", got, err)
	}
	if _, err := q.Respond(ctx, a.ID, "yes"); err == nil {
		t.Fatal("a denied envelope was re-decided")
	}
	if _, err := audit.Verify(log.Path()); err != nil {
		t.Fatal(err)
	}
}
