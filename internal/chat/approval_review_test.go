package chat

import (
	"strings"
	"testing"
	"unicode/utf8"
	"water/internal/tools"
)

func TestHarnessApprovalDetailsCanReachCommandTail(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 60, 12
	if err := m.enterRole("ceo", ""); err != nil {
		t.Fatal(err)
	}
	m.relayout()
	m.approvalDetails = true
	req := tools.ApprovalRequest{Role: "ceo", Tool: tools.ToolApplyActions, Actions: []tools.PlannedAction{{Tool: tools.ToolRun, Args: map[string]any{"command": "echo " + strings.Repeat("long_argument ", 100) + "REVIEW_THE_END"}}}}
	var all strings.Builder
	for i := 0; i < 100; i++ {
		m.approvalPage = i
		all.WriteString(strings.Join(m.approvalCard(req), "\n"))
	}
	if !strings.Contains(all.String(), "REVIEW_THE_END") {
		t.Fatal("command tail is inaccessible even in details view")
	}
}

func TestHarnessApprovalTruncationPreservesUTF8(t *testing.T) {
	got := truncateApproval(strings.Repeat("測", 40), 60)
	if !utf8.ValidString(got) {
		t.Fatalf("approval splits a UTF-8 character: %q", got)
	}
}
