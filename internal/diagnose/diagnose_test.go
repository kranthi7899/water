package diagnose

import (
	"strings"
	"testing"

	"water/internal/orchestrator"
	"water/internal/trace"
)

func TestDiagnoseDetectsSmoothing(t *testing.T) {
	edges := orchestrator.Hierarchy("ceo", "coo", []string{"cto", "design"})
	final := "We will ship the migration in two phases because the throughput ceiling measured at 400 rps cannot be exceeded before March."
	snap := &orchestrator.Snapshot{RunID: "r", FinalOutput: &final, Visits: map[string]int{"ceo": 2, "coo": 2, "cto": 1, "design": 1},
		Artifacts: map[string]string{"ceo/frame": "delegate to coo please"},
		Outbox: []orchestrator.AgentMessage{
			{ID: "1", From: "user", To: "ceo", Topic: "brief", Payload: "should we migrate"},
			{ID: "2", From: "ceo", To: "coo", Topic: "direction", Payload: "find out"},
			{ID: "3", From: "coo", To: "cto", Topic: "assignment", Payload: "check throughput"},
			{ID: "4", From: "cto", To: "coo", Topic: "deliverable", Payload: "throughput ceiling measured at 400 rps cannot be exceeded before March migration phases"},
			{ID: "5", From: "cto", To: "coo", Topic: "dissent", Payload: "I disagree with the deadline", Verbatim: true},
			{ID: "6", From: "coo", To: "ceo", Topic: "status", Payload: "UNCONFIRMED: cto says fine. dissent noted: the deadline is debatable"},
		}}
	events := []trace.Event{{Type: "node_finished", Role: "cto", DurationMS: 1200}, {Type: "run_finished"}}
	rep := Analyze("r", events, snap, edges)
	got := map[string]string{}
	for _, f := range rep.Findings {
		got[f.Check] = f.Severity + ": " + f.Detail
	}
	if !strings.HasPrefix(got["dissent-survival"], "fail") {
		t.Fatalf("paraphrased dissent should fail: %v", got)
	}
	if rep.Influence["cto"] < 0.3 {
		t.Fatalf("cto influence %v", rep.Influence)
	}
	if !strings.HasPrefix(got["role-violation"], "ok") || !strings.HasPrefix(got["unverified-done"], "ok") {
		t.Fatalf("%v", got)
	}
	// Now forward verbatim and add an edge violation.
	snap.Outbox = append(snap.Outbox,
		orchestrator.AgentMessage{ID: "7", From: "coo", To: "ceo", Topic: "dissent", Payload: "I disagree with the deadline", Verbatim: true, ForwardedFrom: "cto"},
		orchestrator.AgentMessage{ID: "8", From: "cto", To: "design", Topic: "deliverable", Payload: "psst"},
	)
	rep = Analyze("r", events, snap, edges)
	got = map[string]string{}
	for _, f := range rep.Findings {
		got[f.Check] = f.Severity + ": " + f.Detail
	}
	if !strings.HasPrefix(got["dissent-survival"], "ok") || rep.DissentRate != 1 {
		t.Fatalf("verbatim forward should pass: %v", got["dissent-survival"])
	}
	if !strings.HasPrefix(got["role-violation"], "fail") {
		t.Fatalf("cto→design should be flagged: %v", got["role-violation"])
	}
}
