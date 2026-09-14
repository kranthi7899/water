package guards_test

import (
	"context"
	"strings"
	"testing"

	"water/internal/agent"
	"water/internal/backend"
	"water/internal/orchestrator"
	"water/internal/roles"
	"water/internal/tools"
	"water/internal/trace"
)

func cooGrant() *roles.ToolGrant {
	g := &roles.ToolGrant{}
	g.Trace = "current-run"
	return g
}

// TestCOOTraceScope — COO in run A cannot resolve run B's trace, and the
// trace grant gives no filesystem or shell tools whatsoever.
func TestCOOTraceScope(t *testing.T) {
	pol := tools.FromGrant("coo", "", cooGrant(), []string{t.TempDir()})
	if !pol.HasTrace() {
		t.Fatal("trace grant not applied")
	}
	if !pol.Empty() || len(pol.ToolNames()) != 0 || len(pol.Filesystem.Roots) != 0 {
		t.Fatalf("trace grant must expose no subprocess tools: %+v", pol)
	}
	svc := tools.NewService(pol, nil)
	if _, err := svc.Call(context.Background(), tools.ToolReadFile, map[string]any{"path": "/etc/hosts"}); err == nil {
		t.Fatal("COO read the filesystem")
	}
	// A recorder for run A holds a call; a reference into run B never resolves,
	// and neither does a reference to a denied call.
	recA, _ := trace.New("", "run-A")
	recA.ToolCall(tools.Event{CallID: "call-cto-aaa", RunID: "run-A", Role: "cto", Tool: "read_file", Allowed: true, Basis: "ok"})
	recA.ToolCall(tools.Event{CallID: "call-cto-den", RunID: "run-A", Role: "cto", Tool: "read_file", Allowed: false, Basis: "outside roots"})
	if _, ok := recA.Resolve(orchestrator.TraceRef{RunID: "run-A", CallID: "call-cto-aaa"}); !ok {
		t.Fatal("own-run reference should resolve")
	}
	if _, ok := recA.Resolve(orchestrator.TraceRef{RunID: "run-B", CallID: "call-cto-aaa"}); ok {
		t.Fatal("reference into another run resolved")
	}
	if _, ok := recA.Resolve(orchestrator.TraceRef{RunID: "run-A", CallID: "call-cto-den"}); ok {
		t.Fatal("a denied call counted as evidence")
	}
}

func evidenceRun(t *testing.T, ctoReply string) (*orchestrator.State, *trace.Recorder) {
	t.Helper()
	fake := backend.NewFake("fake-sub")
	fake.Reply = func(req backend.Request) string {
		switch req.Role {
		case "ceo":
			if strings.Contains(req.Prompt, "[status]") {
				return agent.RouteFinal + "\nDecided."
			}
			return agent.RouteDelegate + "\nCheck the config."
		case "coo":
			if strings.Contains(req.Prompt, "[deliverable]") {
				return "Status: see verification. UNCONFIRMED: design's estimate."
			}
			return "## cto\nRead the config.\n## design\nEstimate the rework."
		case "cto":
			return ctoReply
		case "design":
			return "The rework is roughly two weeks; this is my judgment, nothing was measured."
		}
		return "?"
	}
	rec, _ := trace.New("", "run-A")
	env := agent.Env{Backend: fake, Trace: rec, RoleTools: map[string]*tools.Policy{"coo": tools.FromGrant("coo", "", cooGrant(), nil)}}
	g, st, _, _ := buildHierarchy(t, fake, env)
	// The CTO "read" a file earlier in this run: the recorder holds the call.
	rec.ToolCall(tools.Event{CallID: "call-cto-real", RunID: "run-A", Role: "cto", Tool: "read_file", Args: map[string]any{"path": "/roots/config.yaml"}, Allowed: true, Basis: "filesystem.mode=read-only"})
	st2 := orchestrator.NewState("run-A", "the brief", []string{"ceo"})
	st2.SetEdges(st.Edges())
	if err := (&orchestrator.Executor{MaxParallel: 4}).Run(context.Background(), g, st2); err != nil {
		t.Fatal(err)
	}
	return st2, rec
}

func ceoStatus(st *orchestrator.State) string {
	for _, m := range st.Inbox("ceo") {
		if m.Topic == orchestrator.TopicStatus {
			return m.Payload
		}
	}
	return ""
}

// TestDanglingEvidenceFails — a TraceRef that does not resolve is reported as
// failed verification in the status the CEO receives, not silently ignored.
func TestDanglingEvidenceFails(t *testing.T) {
	st, _ := evidenceRun(t, "The config sets max_conn to 400 (evidence: call-cto-real).\n\nThe timeout is 30s (evidence: call-cto-nope).")
	status := ceoStatus(st)
	if !strings.Contains(status, "FAILED VERIFICATION: The timeout is 30s") || !strings.Contains(status, "call-cto-nope: does not resolve") {
		t.Fatalf("dangling reference not reported:\n%s", status)
	}
	if !strings.Contains(status, "VERIFIED: The config sets max_conn to 400") || !strings.Contains(status, "call-cto-real: read_file") {
		t.Fatalf("resolving reference not verified:\n%s", status)
	}
	if !strings.Contains(status, "1 verified-with-evidence, 1 failed-verification") {
		t.Fatalf("counts wrong:\n%s", status)
	}
}

// TestUnconfirmedPropagates — a claim with no evidence reaches the CEO marked
// unconfirmed-on-word, never as verified; the deliverable's Evidence field
// carries only real references.
func TestUnconfirmedPropagates(t *testing.T) {
	st, _ := evidenceRun(t, "The config sets max_conn to 400 (evidence: call-cto-real).\n\nThis estimate is optimistic given fat-tailed overrun patterns.")
	status := ceoStatus(st)
	if !strings.Contains(status, "Verification of cto's deliverable (mechanical, by water): 1 verified-with-evidence, 0 failed-verification, 1 unconfirmed-on-word") {
		t.Fatalf("cto verdicts wrong:\n%s", status)
	}
	if !strings.Contains(status, "Verification of design's deliverable (mechanical, by water): 0 verified-with-evidence, 0 failed-verification, 1 unconfirmed-on-word") {
		t.Fatalf("design (reasoning only) must be unconfirmed:\n%s", status)
	}
	for _, m := range st.Messages() {
		if m.From == "cto" && m.Topic == orchestrator.TopicDeliverable {
			if len(m.Evidence) != 1 || m.Evidence[0].CallID != "call-cto-real" || m.Evidence[0].RunID != "run-A" {
				t.Fatalf("deliverable evidence: %+v", m.Evidence)
			}
		}
		if m.From == "coo" && m.Topic == orchestrator.TopicStatus && len(m.Evidence) != 1 {
			t.Fatalf("status should carry the verified refs only: %+v", m.Evidence)
		}
	}
	// Without the trace grant, COO cannot verify anything: every referenced
	// claim fails rather than silently passing.
	fake := backend.NewFake("fake-sub")
	fake.Reply = func(req backend.Request) string { return "x" }
	vs := agent.Verify(agent.ExtractClaims("Claim (evidence: call-cto-real).", "run-A"), nil)
	if len(vs) != 1 || vs[0].Status != agent.VerdictFailed {
		t.Fatalf("no resolver must fail, got %+v", vs)
	}
}
