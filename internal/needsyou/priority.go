package needsyou

import (
	"strings"
	"time"
)

// Priority is a plain-language urgency bucket, computed once here (server
// side) so native notifications and the web UI's chips both style/order
// items from a single source of truth. docs/slices/UI.md's U14: "Priority
// is computed server-side as needsyou.Item.Priority so native notifications
// can share it."
type Priority string

const (
	PriorityUrgent Priority = "urgent"
	PriorityHigh   Priority = "high"
	PriorityNormal Priority = "normal"
)

// urgentDeadlineDays and highDeadlineDays are U14's thresholds: a deadline
// this close (or closer — including already overdue) bumps priority up a
// level regardless of severity.
const (
	urgentDeadlineDays = 3
	highDeadlineDays   = 7
)

// DecisionPriority implements U14's rule for a decision card:
//   - Urgent: severity >= 3, or a deadline <= 3 days away.
//   - High: severity == 2, or a deadline <= 7 days away.
//   - Normal: everything else.
//
// An overdue deadline (already past relative to now) counts as "within"
// every threshold: overdue is at least as urgent as due soon, never less.
func DecisionPriority(severity int, deadline *time.Time, now time.Time) Priority {
	if severity >= 3 || deadlineWithin(deadline, now, urgentDeadlineDays) {
		return PriorityUrgent
	}
	if severity == 2 || deadlineWithin(deadline, now, highDeadlineDays) {
		return PriorityHigh
	}
	return PriorityNormal
}

// ApprovalPriority implements U14's approval sub-rule:
//   - Urgent: a deadline <= 3 days away (checked first, so a near deadline
//     always wins over the risk/money floor below).
//   - High: risk is high, or the action looks like it moves money.
//   - Normal: everything else.
//
// Person-request approvals ("Person requests take the priority seeded or
// requested with them") don't exist yet in this codebase — this phase only
// keeps the rule extensible for them (a future caller can just pass that
// seeded/requested Priority straight through instead of calling this
// function), per the task's own explicit scope note; it does not build
// person-request handling.
func ApprovalPriority(riskHigh, money bool, deadline *time.Time, now time.Time) Priority {
	if deadlineWithin(deadline, now, urgentDeadlineDays) {
		return PriorityUrgent
	}
	if riskHigh || money {
		return PriorityHigh
	}
	return PriorityNormal
}

func deadlineWithin(deadline *time.Time, now time.Time, days int) bool {
	if deadline == nil {
		return false
	}
	return !deadline.After(now.Add(time.Duration(days) * 24 * time.Hour))
}

// moneyPayloadMarkers are payload keys (matched as a case-insensitive
// substring) that mark an approval as a money-moving action for U14's "risk
// high or money -> high" rule. This is a deliberately general,
// connector-agnostic signal — no domain, company or specific connector/
// action name — because no connector wired into this phase actually moves
// money yet; it is a forward-looking hook for ApprovalPriority's money
// argument, exercised today only by this package's own tests.
var moneyPayloadMarkers = []string{"amount", "price", "cost", "budget"}

// PayloadLooksLikeMoney reports whether payload has a key naming a
// money-shaped value (see moneyPayloadMarkers). It is exported so a caller
// building an Item from an approvals.Envelope can compute
// ApprovalPriority's money argument from the envelope's own Payload.
func PayloadLooksLikeMoney(payload map[string]any) bool {
	for k := range payload {
		lk := strings.ToLower(k)
		for _, marker := range moneyPayloadMarkers {
			if strings.Contains(lk, marker) {
				return true
			}
		}
	}
	return false
}
