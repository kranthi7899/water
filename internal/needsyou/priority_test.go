package needsyou

import (
	"testing"
	"time"
)

var priorityNow = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func days(n int) *time.Time {
	t := priorityNow.Add(time.Duration(n) * 24 * time.Hour)
	return &t
}

// TestDecisionPriority is U14's table test for the decision-card rule:
// Urgent (severity>=3 or deadline<=3d), High (severity==2 or deadline<=7d),
// Normal otherwise.
func TestDecisionPriority(t *testing.T) {
	cases := []struct {
		name     string
		severity int
		deadline *time.Time
		want     Priority
	}{
		{"severity 3, no deadline", 3, nil, PriorityUrgent},
		{"severity 5, no deadline", 5, nil, PriorityUrgent},
		{"severity 1, deadline in 2 days", 1, days(2), PriorityUrgent},
		{"severity 1, deadline exactly 3 days", 1, days(3), PriorityUrgent},
		{"severity 1, deadline overdue", 1, days(-1), PriorityUrgent},
		{"severity 3 wins even with far deadline", 3, days(30), PriorityUrgent},
		{"severity 2, no deadline", 2, nil, PriorityHigh},
		{"severity 1, deadline in 7 days", 1, days(7), PriorityHigh},
		{"severity 1, deadline in 4 days", 1, days(4), PriorityHigh},
		{"severity 0, no deadline", 0, nil, PriorityNormal},
		{"severity 1, deadline in 10 days", 1, days(10), PriorityNormal},
		{"severity 1, no deadline", 1, nil, PriorityNormal},
		{"severity 2 wins over a far deadline", 2, days(30), PriorityHigh},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DecisionPriority(c.severity, c.deadline, priorityNow)
			if got != c.want {
				t.Errorf("DecisionPriority(severity=%d, deadline=%v) = %q, want %q", c.severity, c.deadline, got, c.want)
			}
		})
	}
}

// TestApprovalPriority is U14's table test for the approval sub-rule: a
// near deadline is Urgent regardless of risk/money; otherwise risk high or
// money is High; otherwise Normal.
func TestApprovalPriority(t *testing.T) {
	cases := []struct {
		name            string
		riskHigh, money bool
		deadline        *time.Time
		want            Priority
	}{
		{"low risk, no money, no deadline", false, false, nil, PriorityNormal},
		{"high risk, no deadline", true, false, nil, PriorityHigh},
		{"money, no deadline", false, true, nil, PriorityHigh},
		{"high risk and money, no deadline", true, true, nil, PriorityHigh},
		{"low risk, deadline in 2 days", false, false, days(2), PriorityUrgent},
		{"high risk, deadline in 2 days: deadline still wins", true, false, days(2), PriorityUrgent},
		{"low risk, deadline in 3 days (boundary)", false, false, days(3), PriorityUrgent},
		{"low risk, deadline in 4 days", false, false, days(4), PriorityNormal},
		{"high risk, deadline in 4 days", true, false, days(4), PriorityHigh},
		{"overdue deadline", false, false, days(-2), PriorityUrgent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ApprovalPriority(c.riskHigh, c.money, c.deadline, priorityNow)
			if got != c.want {
				t.Errorf("ApprovalPriority(risk=%v, money=%v, deadline=%v) = %q, want %q", c.riskHigh, c.money, c.deadline, got, c.want)
			}
		})
	}
}

func TestPayloadLooksLikeMoney(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		want    bool
	}{
		{"no payload", nil, false},
		{"unrelated payload", map[string]any{"to": []string{"a@b.com"}, "subject": "hi"}, false},
		{"amount key", map[string]any{"amount_usd": 500}, true},
		{"price key", map[string]any{"price": 10}, true},
		{"cost key case-insensitive", map[string]any{"Cost": 10}, true},
		{"budget key", map[string]any{"budget_line": "ops"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PayloadLooksLikeMoney(c.payload); got != c.want {
				t.Errorf("PayloadLooksLikeMoney(%v) = %v, want %v", c.payload, got, c.want)
			}
		})
	}
}
