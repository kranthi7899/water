package propose

import (
	"context"
	"fmt"
	"strings"
	"time"

	"water/internal/nervous/intents"
	"water/internal/nervous/slots"
)

const defaultEventDuration = 30 * time.Minute

var calendarCreateSpec = intents.FunctionSpec{
	ID: "calendar.create",
	Args: map[string]slots.Type{
		"who": slots.TypePerson, "when": slots.TypeDate, "at": slots.TypeTime,
		"dur": slots.TypeDuration, "title": slots.TypeText,
	},
	Required: []string{"when", "at"},
	Class:    intents.ClassAction,
	Emits:    []string{"title", "start", "end", "attendees"},
}

// buildCalendarCreate targets gcal.create_event: payload {title, start, end,
// attendees}. when/at are required (Validate guarantees they resolve before
// a proposer ever runs); dur defaults to 30 minutes when the intent's own
// slot default didn't already fill it in. who is optional: an event with no
// named attendee still creates fine, just with an empty attendee list (the
// connector's schema requires the key present, not non-empty).
func buildCalendarCreate(ctx context.Context, d Deps, a map[string]slots.Value) (Proposal, Outcome, error) {
	when, hasWhen := a["when"]
	at, hasAt := a["at"]
	if !hasWhen || !hasAt {
		return Proposal{}, Unresolved, nil
	}

	dur := defaultEventDuration
	if v, ok := a["dur"]; ok {
		dur = v.Dur
	}
	start := combineDateTime(when.Start, at.At)
	end := start.Add(dur)

	attendees := []string{}
	title := "Meeting"
	if w, ok := a["who"]; ok {
		attendees = []string{w.Person.Email}
		title = "Meeting with " + w.Person.Name
	}
	if t, ok := a["title"]; ok && strings.TrimSpace(t.Text) != "" {
		title = t.Text
	}

	payload := map[string]any{
		"title":     title,
		"start":     start.UTC().Format(time.RFC3339),
		"end":       end.UTC().Format(time.RFC3339),
		"attendees": attendees,
	}
	summary := fmt.Sprintf("Create '%s' %s", title, spokenRange(start, end))
	return Proposal{Payload: payload, Summary: summary}, Ok, nil
}

var calendarMoveSpec = intents.FunctionSpec{
	ID: "calendar.move",
	Args: map[string]slots.Type{
		"from": slots.TypeTime, "on": slots.TypeDate, "to_date": slots.TypeDate, "to": slots.TypeTime,
	},
	Required: []string{"from"},
	Class:    intents.ClassAction,
	Emits:    []string{"event_id", "new_start", "new_end"},
}

// buildCalendarMove targets gcal.move_event: payload {event_id, new_start,
// new_end}. It needs exactly one non-cancelled event starting at "from" on
// "on" (default today): zero matches is Unresolved (nothing to move), two
// or more is Ambiguous (which one?) -- the sous chef never guesses. At
// least one of to_date/to must be captured (the registry only requires
// "from"; this proposer enforces the rest, per docs/slices/R.md §7); the
// event's original duration is preserved at its new start time.
func buildCalendarMove(ctx context.Context, d Deps, a map[string]slots.Value) (Proposal, Outcome, error) {
	from, hasFrom := a["from"]
	if !hasFrom {
		return Proposal{}, Unresolved, nil
	}
	day := d.Now()
	if on, ok := a["on"]; ok {
		day = on.Start
	}
	toDate, hasToDate := a["to_date"]
	to, hasTo := a["to"]
	if !hasToDate && !hasTo {
		return Proposal{}, Unresolved, nil
	}

	fromAt := combineDateTime(day, from.At)
	start, end := dayBounds(day)
	events, err := d.Store.EventsInRange(ctx, start, end)
	if err != nil {
		return Proposal{}, Ok, fmt.Errorf("propose: calendar.move: %w", err)
	}

	var matches []int
	for i, e := range events {
		if strings.EqualFold(e.Status, "cancelled") {
			continue
		}
		if e.StartAt.Equal(fromAt) {
			matches = append(matches, i)
		}
	}
	switch len(matches) {
	case 0:
		return Proposal{}, Unresolved, nil
	case 1:
		// exactly one match: proceed below
	default:
		return Proposal{}, Ambiguous, nil
	}
	match := events[matches[0]]

	newDay := day
	if hasToDate {
		newDay = toDate.Start
	}
	newClock := from.At
	if hasTo {
		newClock = to.At
	}
	newStart := combineDateTime(newDay, newClock)
	newEnd := newStart.Add(match.EndAt.Sub(match.StartAt))

	payload := map[string]any{
		"event_id":  match.SourceID,
		"new_start": newStart.UTC().Format(time.RFC3339),
		"new_end":   newEnd.UTC().Format(time.RFC3339),
	}
	summary := fmt.Sprintf("Move '%s' to %s", match.Title, spokenRange(newStart, newEnd))
	return Proposal{Payload: payload, Summary: summary, Tainted: match.External}, Ok, nil
}
