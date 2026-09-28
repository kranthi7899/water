package cli

import (
	"strings"
	"testing"
)

func TestDecisionOutcomeNeverSaysNotExecutedForAnUnknownOrRanAction(t *testing.T) {
	for _, tc := range []struct {
		r    DecisionResult
		want string
	}{
		{DecisionResult{OutcomeUnknown: true, Error: "send outcome unknown"}, "outcome unknown"},
		{DecisionResult{Executed: true, Error: "store: disk full", Output: []byte(`{"id":"m1"}`)}, "executed, but"},
		{DecisionResult{Executed: true, Output: []byte(`{}`)}, "executed: "},
		{DecisionResult{Error: "HTTP 400"}, "not executed"},
	} {
		got := decisionOutcome(tc.r)
		if !strings.HasPrefix(got, tc.want) {
			t.Errorf("decisionOutcome(%+v) = %q, want prefix %q", tc.r, got, tc.want)
		}
		if tc.want != "not executed" && strings.Contains(got, "not executed") {
			t.Errorf("decisionOutcome(%+v) = %q says not executed", tc.r, got)
		}
	}
}
