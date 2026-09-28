// Package propose holds the sous chef's write-intent proposers: pure
// functions that turn a matched write intent's resolved slots into a
// connector payload, without ever calling a connector, the gate or the
// approval queue themselves (docs/slices/R.md Design §1(d)/(b): "a write
// intent never calls a connector"; internal/nervous/reflex/imports_test.go's
// import denylist covers this package too and forbids exactly those
// selectors). internal/nervous decides, from the matched intent's
// RequiresApproval (set at registry load time from the manifest's granted
// level -- see internal/nervous/intents.checkAction), whether a successful
// Proposal here is queued for approval (a level-A action) or delivered
// directly as a draft (a level-D action, "nothing leaves").
package propose

import (
	"context"
	"time"

	"water/internal/nervous/intents"
	"water/internal/nervous/slots"
	"water/internal/store"
)

// StoreView is the read-only slice of the twin's store a proposer may read.
// It is defined again here, rather than importing reflex.StoreView, because
// Design §2's dependency-direction table lists this package's allowed
// imports as {store, slots, intents}, not reflex: a proposer's concrete
// Deps.Store is normally the very same reflex.NewStoreView(...) value a
// read handler uses, which already satisfies this interface structurally
// (Go interface-to-interface assignment needs no adapter here).
type StoreView interface {
	EventsInRange(ctx context.Context, from, to time.Time) ([]store.Event, error)
	MessagesFrom(ctx context.Context, email string, limit int) ([]store.Message, error)
}

// Deps is a proposer's readable world: a read-only store view, the current
// time, and the twin's own identity. Owner is unused by every proposer this
// task builds -- none of gcal.create_event/move_event or gmail.
// draft_message/send_message takes an explicit "from"/"organizer" argument,
// since the connector always fills that from its own configured identity --
// but the field is kept so a future proposer can use it without another
// signature change.
type Deps struct {
	Store StoreView
	Now   func() time.Time
	Owner slots.Person
}

// Outcome is a proposer's Build result: Ok means Payload/Summary are ready
// to propose; Unresolved/Ambiguous mean an entity Build needed (an event to
// move, a message to reply to) could not be pinned down safely, and the
// caller must escalate rather than guess -- the sous chef never asks its
// own clarifying question here.
type Outcome int

const (
	Ok Outcome = iota
	Unresolved
	Ambiguous
)

// Proposal is what a successful Build produces. Action is left empty by
// every Build function in this package: the matched intent's own Action
// field (internal/nervous/intents.Intent.Action) is the authoritative
// connector-function target, since one proposer (mail.reply) backs two
// intents that target two different functions depending on the manifest's
// granted level (mail.draft_reply -> gmail.draft_message, level D;
// mail.send_reply -> gmail.send_message, level A) -- internal/nervous fills
// Action in from the intent right before it decides whether to queue an
// envelope or deliver the draft directly.
type Proposal struct {
	Action  string
	Payload map[string]any
	Summary string // one-line interpretation echo, e.g. "Create 'Meeting with Alex Chen' Thu 25 Sep 3pm-3:30pm"
	Tainted bool   // any store record it used is External (e.g. a sender address or thread)
}

// Proposer pairs a write function's declared contract with its pure build
// function.
type Proposer struct {
	Spec  intents.FunctionSpec
	Build func(ctx context.Context, d Deps, a map[string]slots.Value) (Proposal, Outcome, error)
}

var table = map[string]Proposer{
	"calendar.create": {Spec: calendarCreateSpec, Build: buildCalendarCreate},
	"calendar.move":   {Spec: calendarMoveSpec, Build: buildCalendarMove},
	"mail.reply":      {Spec: mailReplySpec, Build: buildMailReply},
}

// Table returns every registered proposer, keyed by the id an intent file's
// proposer: field names (e.g. "calendar.create"). The map is copied so a
// caller can't mutate the package-level table.
func Table() map[string]Proposer {
	out := make(map[string]Proposer, len(table))
	for k, v := range table {
		out[k] = v
	}
	return out
}

// Specs returns each proposer's FunctionSpec, for intents.LoadRegistry's
// Functions.Write.
func Specs() map[string]intents.FunctionSpec {
	out := make(map[string]intents.FunctionSpec, len(table))
	for k, v := range table {
		out[k] = v.Spec
	}
	return out
}

// combineDateTime places at's hour/minute onto day's year/month/day, in
// day's location: date slots resolve to a whole-day Start and time slots
// resolve to an hour/minute on a zero-value date (slots.Value.At), so every
// proposer that needs one instant from both combines them this way.
func combineDateTime(day, at time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), at.Hour(), at.Minute(), 0, 0, day.Location())
}

// dayBounds mirrors reflex.dayBounds (unexported there): the half-open
// [start, end) range covering day's own local day.
func dayBounds(day time.Time) (start, end time.Time) {
	start = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	return start, start.AddDate(0, 0, 1)
}

// spokenRange renders "Thu 25 Sep 3pm-3:30pm" for a one-line summary.
func spokenRange(start, end time.Time) string {
	return start.Local().Format("Mon 2 Jan") + " " + start.Local().Format("3:04pm") + "-" + end.Local().Format("3:04pm")
}
