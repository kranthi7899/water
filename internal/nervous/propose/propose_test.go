package propose_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"water/internal/nervous/propose"
	"water/internal/nervous/slots"
	"water/internal/store"
)

var fixedNow = time.Date(2026, 9, 24, 9, 0, 0, 0, time.FixedZone("PT", -7*3600)) // Thursday

func testEnts() slots.Entities {
	return slots.Entities{People: []slots.Person{
		{Name: "Priya Nair", Email: "priya@example.com"},
		{Name: "Alex Chen", Email: "alex.chen@example.com"},
		{Name: "Alex Rivera", Email: "alex.rivera@example.com"},
	}}
}

func mustResolve(t *testing.T, typ slots.Type, s string, ents slots.Entities) slots.Value {
	t.Helper()
	v, outcome, _ := slots.ResolveString(typ, s, slots.Spec{}, fixedNow, ents)
	if outcome != slots.Resolved {
		t.Fatalf("resolve %q as %s: outcome=%v, want Resolved", s, typ, outcome)
	}
	return v
}

// fakeStore is a minimal propose.StoreView for tests.
type fakeStore struct {
	events      []store.Event
	eventsErr   error
	messages    map[string][]store.Message
	messagesErr error
}

func (f fakeStore) EventsInRange(ctx context.Context, from, to time.Time) ([]store.Event, error) {
	return f.events, f.eventsErr
}

func (f fakeStore) MessagesFrom(ctx context.Context, email string, limit int) ([]store.Message, error) {
	if f.messagesErr != nil {
		return nil, f.messagesErr
	}
	return f.messages[email], nil
}

func deps(st fakeStore) propose.Deps {
	return propose.Deps{Store: st, Now: func() time.Time { return fixedNow }}
}

// ---- calendar.create ----

func TestBuildCalendarCreateHappyPath(t *testing.T) {
	ents := testEnts()
	args := map[string]slots.Value{
		"when":  mustResolve(t, slots.TypeDate, "tomorrow", ents),
		"at":    mustResolve(t, slots.TypeTime, "3pm", ents),
		"dur":   mustResolve(t, slots.TypeDuration, "30 minutes", ents),
		"who":   mustResolve(t, slots.TypePerson, "priya", ents),
		"title": mustResolve(t, slots.TypeText, "Budget review", ents),
	}
	p, outcome, err := propose.Table()["calendar.create"].Build(context.Background(), deps(fakeStore{}), args)
	if err != nil || outcome != propose.Ok {
		t.Fatalf("Build: outcome=%v err=%v, want Ok", outcome, err)
	}
	if p.Payload["title"] != "Budget review" {
		t.Fatalf("title = %v, want 'Budget review'", p.Payload["title"])
	}
	if p.Payload["start"] != "2026-09-25T22:00:00Z" {
		t.Fatalf("start = %v, want 2026-09-25T22:00:00Z", p.Payload["start"])
	}
	if p.Payload["end"] != "2026-09-25T22:30:00Z" {
		t.Fatalf("end = %v, want 2026-09-25T22:30:00Z", p.Payload["end"])
	}
	attendees, ok := p.Payload["attendees"].([]string)
	if !ok || len(attendees) != 1 || attendees[0] != "priya@example.com" {
		t.Fatalf("attendees = %v, want [priya@example.com]", p.Payload["attendees"])
	}
	if p.Summary == "" {
		t.Fatalf("Summary is empty, want a one-line interpretation echo")
	}
	if p.Tainted {
		t.Fatalf("Tainted = true, want false (nothing here came from an External record)")
	}
}

func TestBuildCalendarCreateNoWhoDefaultsTitleAndEmptyAttendees(t *testing.T) {
	ents := testEnts()
	args := map[string]slots.Value{
		"when": mustResolve(t, slots.TypeDate, "friday", ents),
		"at":   mustResolve(t, slots.TypeTime, "10am", ents),
	}
	p, outcome, err := propose.Table()["calendar.create"].Build(context.Background(), deps(fakeStore{}), args)
	if err != nil || outcome != propose.Ok {
		t.Fatalf("Build: outcome=%v err=%v, want Ok", outcome, err)
	}
	if p.Payload["title"] != "Meeting" {
		t.Fatalf("title = %v, want the default 'Meeting'", p.Payload["title"])
	}
	attendees, ok := p.Payload["attendees"].([]string)
	if !ok || len(attendees) != 0 {
		t.Fatalf("attendees = %v, want an empty (but present) list", p.Payload["attendees"])
	}
	// 10am PT on 2026-09-25 (the next Friday) is 17:00 UTC; the default
	// 30-minute duration (no "dur" slot in args at all) makes the end 17:30 UTC.
	if p.Payload["start"] != "2026-09-25T17:00:00Z" || p.Payload["end"] != "2026-09-25T17:30:00Z" {
		t.Fatalf("start/end = %v/%v, want 17:00:00Z/17:30:00Z (default 30-minute duration applied)", p.Payload["start"], p.Payload["end"])
	}
}

// ---- calendar.move ----

func TestBuildCalendarMoveHappyPath(t *testing.T) {
	ents := testEnts()
	from := mustResolve(t, slots.TypeTime, "3pm", ents)
	on := mustResolve(t, slots.TypeDate, "today", ents)
	to := mustResolve(t, slots.TypeTime, "4pm", ents)
	fromAt := time.Date(2026, 9, 24, 15, 0, 0, 0, fixedNow.Location())
	ev := store.Event{
		Meta:    store.Meta{SourceID: "evt1"},
		Title:   "Board sync",
		StartAt: fromAt,
		EndAt:   fromAt.Add(45 * time.Minute),
		Status:  "confirmed",
	}
	args := map[string]slots.Value{"from": from, "on": on, "to": to}
	p, outcome, err := propose.Table()["calendar.move"].Build(context.Background(), deps(fakeStore{events: []store.Event{ev}}), args)
	if err != nil || outcome != propose.Ok {
		t.Fatalf("Build: outcome=%v err=%v, want Ok", outcome, err)
	}
	if p.Payload["event_id"] != "evt1" {
		t.Fatalf("event_id = %v, want evt1", p.Payload["event_id"])
	}
	// Original duration (45m) preserved at the new start time (4pm PT = 23:00 UTC).
	if p.Payload["new_start"] != "2026-09-24T23:00:00Z" || p.Payload["new_end"] != "2026-09-24T23:45:00Z" {
		t.Fatalf("new_start/new_end = %v/%v, want 23:00:00Z/23:45:00Z", p.Payload["new_start"], p.Payload["new_end"])
	}
}

func TestBuildCalendarMoveZeroMatchesUnresolved(t *testing.T) {
	ents := testEnts()
	args := map[string]slots.Value{
		"from": mustResolve(t, slots.TypeTime, "3pm", ents),
		"on":   mustResolve(t, slots.TypeDate, "today", ents),
		"to":   mustResolve(t, slots.TypeTime, "4pm", ents),
	}
	_, outcome, err := propose.Table()["calendar.move"].Build(context.Background(), deps(fakeStore{events: nil}), args)
	if err != nil || outcome != propose.Unresolved {
		t.Fatalf("Build: outcome=%v err=%v, want Unresolved (no matching event)", outcome, err)
	}
}

func TestBuildCalendarMoveTwoMatchesAmbiguous(t *testing.T) {
	ents := testEnts()
	fromAt := time.Date(2026, 9, 24, 15, 0, 0, 0, fixedNow.Location())
	dup := store.Event{Meta: store.Meta{SourceID: "evt1"}, StartAt: fromAt, EndAt: fromAt.Add(time.Hour), Status: "confirmed"}
	dup2 := store.Event{Meta: store.Meta{SourceID: "evt2"}, StartAt: fromAt, EndAt: fromAt.Add(time.Hour), Status: "confirmed"}
	args := map[string]slots.Value{
		"from": mustResolve(t, slots.TypeTime, "3pm", ents),
		"on":   mustResolve(t, slots.TypeDate, "today", ents),
		"to":   mustResolve(t, slots.TypeTime, "4pm", ents),
	}
	_, outcome, err := propose.Table()["calendar.move"].Build(context.Background(), deps(fakeStore{events: []store.Event{dup, dup2}}), args)
	if err != nil || outcome != propose.Ambiguous {
		t.Fatalf("Build: outcome=%v err=%v, want Ambiguous (two matching events)", outcome, err)
	}
}

func TestBuildCalendarMoveIgnoresCancelledEvent(t *testing.T) {
	ents := testEnts()
	fromAt := time.Date(2026, 9, 24, 15, 0, 0, 0, fixedNow.Location())
	cancelled := store.Event{Meta: store.Meta{SourceID: "evt1"}, StartAt: fromAt, EndAt: fromAt.Add(time.Hour), Status: "cancelled"}
	args := map[string]slots.Value{
		"from": mustResolve(t, slots.TypeTime, "3pm", ents),
		"on":   mustResolve(t, slots.TypeDate, "today", ents),
		"to":   mustResolve(t, slots.TypeTime, "4pm", ents),
	}
	_, outcome, err := propose.Table()["calendar.move"].Build(context.Background(), deps(fakeStore{events: []store.Event{cancelled}}), args)
	if err != nil || outcome != propose.Unresolved {
		t.Fatalf("Build: outcome=%v err=%v, want Unresolved (only match is cancelled)", outcome, err)
	}
}

func TestBuildCalendarMoveNeitherToNorToDateUnresolved(t *testing.T) {
	ents := testEnts()
	args := map[string]slots.Value{
		"from": mustResolve(t, slots.TypeTime, "3pm", ents),
		"on":   mustResolve(t, slots.TypeDate, "today", ents),
	}
	_, outcome, err := propose.Table()["calendar.move"].Build(context.Background(), deps(fakeStore{}), args)
	if err != nil || outcome != propose.Unresolved {
		t.Fatalf("Build: outcome=%v err=%v, want Unresolved (neither to nor to_date given)", outcome, err)
	}
}

func TestBuildCalendarMoveStoreErrorPropagates(t *testing.T) {
	ents := testEnts()
	wantErr := errors.New("store unavailable")
	args := map[string]slots.Value{
		"from": mustResolve(t, slots.TypeTime, "3pm", ents),
		"to":   mustResolve(t, slots.TypeTime, "4pm", ents),
	}
	_, _, err := propose.Table()["calendar.move"].Build(context.Background(), deps(fakeStore{eventsErr: wantErr}), args)
	if err == nil {
		t.Fatalf("Build: err = nil, want the store's own error")
	}
}

// ---- mail.reply ----

func TestBuildMailReplyHappyPathAddsReprefix(t *testing.T) {
	ents := testEnts()
	st := fakeStore{messages: map[string][]store.Message{
		"priya@example.com": {{Meta: store.Meta{SourceID: "m1"}, From: "priya@example.com", Subject: "Q3 budget", Thread: "t1"}},
	}}
	args := map[string]slots.Value{
		"who":  mustResolve(t, slots.TypePerson, "priya", ents),
		"body": mustResolve(t, slots.TypeText, "sounds good, see you then", ents),
	}
	p, outcome, err := propose.Table()["mail.reply"].Build(context.Background(), deps(st), args)
	if err != nil || outcome != propose.Ok {
		t.Fatalf("Build: outcome=%v err=%v, want Ok", outcome, err)
	}
	if p.Payload["subject"] != "Re: Q3 budget" {
		t.Fatalf("subject = %v, want 'Re: Q3 budget'", p.Payload["subject"])
	}
	if p.Payload["body"] != "sounds good, see you then" {
		t.Fatalf("body = %v, want the dictated text verbatim", p.Payload["body"])
	}
	to, ok := p.Payload["to"].([]string)
	if !ok || len(to) != 1 || to[0] != "priya@example.com" {
		t.Fatalf("to = %v, want [priya@example.com]", p.Payload["to"])
	}
}

func TestBuildMailReplyAlreadyPrefixedSubjectUnchanged(t *testing.T) {
	ents := testEnts()
	st := fakeStore{messages: map[string][]store.Message{
		"priya@example.com": {{Meta: store.Meta{SourceID: "m1"}, From: "priya@example.com", Subject: "Re: Q3 budget"}},
	}}
	args := map[string]slots.Value{
		"who":  mustResolve(t, slots.TypePerson, "priya", ents),
		"body": mustResolve(t, slots.TypeText, "sounds good", ents),
	}
	p, outcome, err := propose.Table()["mail.reply"].Build(context.Background(), deps(st), args)
	if err != nil || outcome != propose.Ok {
		t.Fatalf("Build: outcome=%v err=%v, want Ok", outcome, err)
	}
	if p.Payload["subject"] != "Re: Q3 budget" {
		t.Fatalf("subject = %v, want unchanged 'Re: Q3 budget'", p.Payload["subject"])
	}
}

func TestBuildMailReplyNoMessageUnresolved(t *testing.T) {
	ents := testEnts()
	args := map[string]slots.Value{
		"who":  mustResolve(t, slots.TypePerson, "priya", ents),
		"body": mustResolve(t, slots.TypeText, "sounds good", ents),
	}
	_, outcome, err := propose.Table()["mail.reply"].Build(context.Background(), deps(fakeStore{}), args)
	if err != nil || outcome != propose.Unresolved {
		t.Fatalf("Build: outcome=%v err=%v, want Unresolved (no message from who)", outcome, err)
	}
}

func TestBuildMailReplyTaintedFromExternalMessage(t *testing.T) {
	ents := testEnts()
	st := fakeStore{messages: map[string][]store.Message{
		"priya@example.com": {{Meta: store.Meta{SourceID: "m1", External: true}, From: "priya@example.com", Subject: "Q3 budget"}},
	}}
	args := map[string]slots.Value{
		"who":  mustResolve(t, slots.TypePerson, "priya", ents),
		"body": mustResolve(t, slots.TypeText, "sounds good", ents),
	}
	p, outcome, err := propose.Table()["mail.reply"].Build(context.Background(), deps(st), args)
	if err != nil || outcome != propose.Ok {
		t.Fatalf("Build: outcome=%v err=%v, want Ok", outcome, err)
	}
	if !p.Tainted {
		t.Fatalf("Tainted = false, want true (the message replied to is an External record)")
	}
}

func TestBuildMailReplyStoreErrorPropagates(t *testing.T) {
	ents := testEnts()
	wantErr := errors.New("store unavailable")
	args := map[string]slots.Value{
		"who":  mustResolve(t, slots.TypePerson, "priya", ents),
		"body": mustResolve(t, slots.TypeText, "sounds good", ents),
	}
	_, _, err := propose.Table()["mail.reply"].Build(context.Background(), deps(fakeStore{messagesErr: wantErr}), args)
	if err == nil {
		t.Fatalf("Build: err = nil, want the store's own error")
	}
}

// ---- Specs/Table shape ----

func TestSpecsMatchTable(t *testing.T) {
	specs := propose.Specs()
	table := propose.Table()
	if len(specs) != len(table) {
		t.Fatalf("Specs() has %d entries, Table() has %d, want equal", len(specs), len(table))
	}
	for k, p := range table {
		if specs[k].ID != p.Spec.ID {
			t.Fatalf("Specs()[%q] = %+v, want it to match Table()[%q].Spec", k, specs[k], k)
		}
	}
}
