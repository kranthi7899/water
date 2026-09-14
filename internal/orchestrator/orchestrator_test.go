package orchestrator

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestCEOFanoutPhases(t *testing.T) {
	r := &CEOFanoutRouter{CEO: "ceo", Delegates: []string{"cfo", "cto"}}
	s := NewState("", "b", []string{"ceo"})
	if got := r.Next(s); !reflect.DeepEqual(got, []string{"ceo"}) {
		t.Fatalf("phase1 %v", got)
	}
	s.markVisited("ceo")
	if got := r.Next(s); !reflect.DeepEqual(got, []string{"cfo", "cto"}) {
		t.Fatalf("phase2 %v", got)
	}
	s.markVisited("cfo")
	s.markVisited("cto")
	if got := r.Next(s); !reflect.DeepEqual(got, []string{"ceo"}) {
		t.Fatalf("phase3 %v", got)
	}
	s.markVisited("ceo")
	if got := r.Next(s); len(got) != 0 {
		t.Fatalf("done %v", got)
	}
}

func TestExecutorInjectsBriefAndBoundsParallelism(t *testing.T) {
	var inflight, peak atomic.Int32
	node := func(ctx context.Context, s *State) error {
		n := inflight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		defer inflight.Add(-1)
		s.MustAppend(AgentMessage{From: "x", To: "y", Topic: "t", Payload: "p"})
		return nil
	}
	g := &Graph{Nodes: map[string]Node{"ceo": node, "a": node, "b": node, "c": node},
		Router: &CEOFanoutRouter{CEO: "ceo", Delegates: []string{"a", "b", "c"}}}
	s := NewState("", "hello", []string{"ceo"})
	if err := (&Executor{MaxParallel: 2}).Run(context.Background(), g, s); err != nil {
		t.Fatal(err)
	}
	if in := s.Inbox("ceo"); len(in) != 1 || in[0].Topic != TopicBrief || in[0].Payload != "hello" {
		t.Fatalf("brief not injected: %v", in)
	}
	if peak.Load() > 2 {
		t.Fatalf("parallelism %d exceeded max 2", peak.Load())
	}
	if s.Visits("ceo") != 2 {
		t.Fatalf("ceo visits %d", s.Visits("ceo"))
	}
}

func TestExecutorStepGuardAndStall(t *testing.T) {
	loop := routerFunc{entry: "x", next: func(*State) []string { return []string{"x"} }}
	// A node that changes nothing each step is a stall, caught before the step guard.
	g := &Graph{Nodes: map[string]Node{"x": func(context.Context, *State) error { return nil }}, Router: loop}
	err := (&Executor{MaxSteps: 3}).Run(context.Background(), g, NewState("", "", nil))
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("expected stall error, got %v", err)
	}
	// A node that keeps producing messages hits the step guard instead.
	g2 := &Graph{Nodes: map[string]Node{"x": func(_ context.Context, s *State) error {
		s.MustAppend(AgentMessage{From: "x", To: "x", Topic: "loop"})
		return nil
	}}, Router: loop}
	err = (&Executor{MaxSteps: 3}).Run(context.Background(), g2, NewState("", "", nil))
	if err == nil || errors.Is(err, ErrStalled) {
		t.Fatalf("expected step guard error, got %v", err)
	}
}

type routerFunc struct {
	entry string
	next  func(*State) []string
}

func (r routerFunc) Name() string           { return "loop" }
func (r routerFunc) Entry() string          { return r.entry }
func (r routerFunc) Next(s *State) []string { return r.next(s) }

func TestSnapshotRestore(t *testing.T) {
	s := NewState("run1", "b", []string{"ceo"})
	s.MustAppend(AgentMessage{From: "ceo", To: "cfo", Topic: "x", Payload: "y"})
	s.SetArtifact("k", "v")
	s.markVisited("ceo")
	s.MarkConsumed("ceo", 1)
	s.MarkUntrusted("cfo")
	_ = s.SetFinalOutput("ceo", "done")
	r := Restore(s.Snapshot(), []string{"ceo"})
	if r.RunID != "run1" || len(r.Messages()) != 1 || r.Visits("ceo") != 1 || r.Consumed("ceo") != 1 || !r.IsUntrusted("cfo") {
		t.Fatal("restore lost state")
	}
	if v, _ := r.Artifact("k"); v != "v" {
		t.Fatal("artifact lost")
	}
	if f, ok := r.FinalOutput(); !ok || f != "done" {
		t.Fatal("final lost")
	}
}

// --- Part 6 gates -----------------------------------------------------------

func hier() *HierarchyRouter {
	return &HierarchyRouter{CEO: "ceo", COO: "coo", Specialists: []string{"cto", "design"}}
}

func TestHierarchyPhases(t *testing.T) {
	h := hier()
	s := NewState("", "brief", []string{"ceo"})
	s.SetEdges(h.Graph())
	s.MustAppend(AgentMessage{From: UserSender, To: "ceo", Topic: TopicBrief, Payload: "brief"})
	if got := h.Next(s); !reflect.DeepEqual(got, []string{"ceo"}) {
		t.Fatalf("frame: %v", got)
	}
	// CEO delegates.
	s.MarkConsumed("ceo", 1)
	s.MustAppend(AgentMessage{From: "ceo", To: "coo", Topic: TopicDirection, Payload: "go"})
	s.markVisited("ceo")
	if got := h.Next(s); !reflect.DeepEqual(got, []string{"coo"}) {
		t.Fatalf("assign: %v", got)
	}
	// COO assigns cto only.
	s.MarkConsumed("coo", 1)
	s.MustAppend(AgentMessage{From: "coo", To: "cto", Topic: TopicAssignment, Payload: "check", CorrelationID: "asg-1-cto"})
	s.markVisited("coo")
	if got := h.Next(s); !reflect.DeepEqual(got, []string{"cto"}) {
		t.Fatalf("specialists: %v", got)
	}
	// CTO delivers.
	s.MarkConsumed("cto", 1)
	s.MustAppend(AgentMessage{From: "cto", To: "coo", Topic: TopicDeliverable, Payload: "done", CorrelationID: "asg-1-cto"})
	s.markVisited("cto")
	if got := h.Next(s); !reflect.DeepEqual(got, []string{"coo"}) {
		t.Fatalf("rollup: %v", got)
	}
	// COO reports.
	s.MarkConsumed("coo", 2)
	s.MustAppend(AgentMessage{From: "coo", To: "ceo", Topic: TopicStatus, Payload: "status"})
	s.markVisited("coo")
	if got := h.Next(s); !reflect.DeepEqual(got, []string{"ceo"}) {
		t.Fatalf("adjudicate: %v", got)
	}
	s.MarkConsumed("ceo", 2)
	_ = s.SetFinalOutput("ceo", "final")
	s.markVisited("ceo")
	if got := h.Next(s); len(got) != 0 {
		t.Fatalf("done: %v", got)
	}
}

func TestHierarchyCEOAnswersAlone(t *testing.T) {
	h := hier()
	s := NewState("", "brief", []string{"ceo"})
	s.SetEdges(h.Graph())
	s.MustAppend(AgentMessage{From: UserSender, To: "ceo", Topic: TopicBrief, Payload: "brief"})
	_ = s.SetFinalOutput("ceo", "answered alone")
	s.markVisited("ceo")
	if got := h.Next(s); len(got) != 0 {
		t.Fatalf("run should end when CEO answers alone, got %v", got)
	}
}

// TestSpecialistsCannotMessageEachOther — Part 6.3 gate.
func TestSpecialistsCannotMessageEachOther(t *testing.T) {
	s := NewState("", "b", []string{"ceo"})
	s.SetEdges(hier().Graph())
	for _, topic := range []string{TopicDeliverable, TopicStatus, TopicDissent, TopicEscalation, TopicAssignment, "anything"} {
		if _, err := s.AppendMessage(AgentMessage{From: "cto", To: "design", Topic: topic, Payload: "x"}); !errors.Is(err, ErrEdgeForbidden) {
			t.Fatalf("cto→design %s: got %v, want ErrEdgeForbidden", topic, err)
		}
		if _, err := s.AppendMessage(AgentMessage{From: "design", To: "cto", Topic: topic, Payload: "x"}); !errors.Is(err, ErrEdgeForbidden) {
			t.Fatalf("design→cto %s: got %v, want ErrEdgeForbidden", topic, err)
		}
	}
	if len(s.Messages()) != 0 {
		t.Fatal("a forbidden message was appended")
	}
	// The legitimate path works.
	if _, err := s.AppendMessage(AgentMessage{From: "design", To: "coo", Topic: TopicDeliverable, Payload: "x"}); err != nil {
		t.Fatal(err)
	}
}

// TestEscalationEdge — CTO/Design→CEO works only for escalation topics.
func TestEscalationEdge(t *testing.T) {
	s := NewState("", "b", []string{"ceo"})
	s.SetEdges(hier().Graph())
	for _, sp := range []string{"cto", "design"} {
		if _, err := s.AppendMessage(AgentMessage{From: sp, To: "ceo", Topic: TopicEscalation, Payload: "danger", Verbatim: true}); err != nil {
			t.Fatalf("%s escalation should be allowed: %v", sp, err)
		}
		for _, topic := range []string{TopicDeliverable, TopicStatus, TopicDissent, TopicDirection, "chat"} {
			if _, err := s.AppendMessage(AgentMessage{From: sp, To: "ceo", Topic: topic, Payload: "x"}); !errors.Is(err, ErrEdgeForbidden) {
				t.Fatalf("%s→ceo %s: got %v, want ErrEdgeForbidden", sp, topic, err)
			}
		}
	}
	// COO cannot assign to the CEO, nor can the CEO assign to a specialist directly.
	if _, err := s.AppendMessage(AgentMessage{From: "ceo", To: "cto", Topic: TopicAssignment}); !errors.Is(err, ErrEdgeForbidden) {
		t.Fatalf("ceo→cto should be forbidden, got %v", err)
	}
}

// TestUntrustedMustBeMarked — Part 5.5 gate: a node that consumed untrusted
// content cannot write an unmarked outbox message.
func TestUntrustedMustBeMarked(t *testing.T) {
	s := NewState("", "b", []string{"ceo"})
	s.SetEdges(hier().Graph())
	s.MarkUntrusted("cto")
	if _, err := s.AppendMessage(AgentMessage{From: "cto", To: "coo", Topic: TopicDeliverable, Payload: "x"}); !errors.Is(err, ErrUnmarkedUntrusted) {
		t.Fatalf("got %v, want ErrUnmarkedUntrusted", err)
	}
	if _, err := s.AppendMessage(AgentMessage{From: "cto", To: "coo", Topic: TopicDeliverable, Payload: "x", Untrusted: true}); err != nil {
		t.Fatal(err)
	}
}

// TestResumeDoesNotRerun — Part 3C gate: resume continues from the router's
// derived phase without re-executing completed nodes, and a node that failed
// mid-superstep does not lose its completed siblings' writes.
func TestResumeDoesNotRerun(t *testing.T) {
	dir := t.TempDir()
	h := hier()
	runs := map[string]int{}
	var failCTO atomic.Bool
	failCTO.Store(true)
	mk := func(name string) Node {
		return func(_ context.Context, s *State) error {
			runs[name]++
			in := s.Inbox(name)
			s.MarkConsumed(name, len(in))
			switch name {
			case "ceo":
				if s.Visits("ceo") == 0 {
					s.MustAppend(AgentMessage{From: "ceo", To: "coo", Topic: TopicDirection, Payload: "go"})
					return nil
				}
				return s.SetFinalOutput("ceo", "final")
			case "coo":
				if s.Visits("coo") == 0 {
					s.MustAppend(AgentMessage{From: "coo", To: "cto", Topic: TopicAssignment, CorrelationID: "a-cto", Payload: "x"})
					s.MustAppend(AgentMessage{From: "coo", To: "design", Topic: TopicAssignment, CorrelationID: "a-design", Payload: "y"})
					return nil
				}
				s.MustAppend(AgentMessage{From: "coo", To: "ceo", Topic: TopicStatus, Payload: "status"})
				return nil
			case "cto":
				if failCTO.Load() {
					return errors.New("cto crashed")
				}
				s.MustAppend(AgentMessage{From: "cto", To: "coo", Topic: TopicDeliverable, CorrelationID: "a-cto", Payload: "d"})
				return nil
			default:
				s.MustAppend(AgentMessage{From: "design", To: "coo", Topic: TopicDeliverable, CorrelationID: "a-design", Payload: "d"})
				return nil
			}
		}
	}
	nodes := map[string]Node{"ceo": mk("ceo"), "coo": mk("coo"), "cto": mk("cto"), "design": mk("design")}
	g := &Graph{Nodes: nodes, Router: h}
	cp := &FileCheckpointer{Dir: dir, Orchestrators: []string{"ceo"}}

	st := NewState("run-x", "brief", []string{"ceo"})
	st.SetEdges(h.Graph())
	err := (&Executor{Checkpointer: cp, MaxParallel: 1}).Run(context.Background(), g, st)
	if err == nil {
		t.Fatal("expected first run to fail at cto")
	}
	if runs["design"] != 1 {
		t.Fatalf("design should have completed once before the failure, ran %d", runs["design"])
	}

	// Simulate process restart: reload from disk by the caller-persisted id.
	failCTO.Store(false)
	st2, err := cp.Load(context.Background(), "run-x")
	if err != nil {
		t.Fatal(err)
	}
	st2.SetEdges(h.Graph())
	deliverables := 0
	for _, m := range st2.Inbox("coo") {
		if m.Topic == TopicDeliverable && m.From == "design" {
			deliverables++
		}
	}
	if deliverables != 1 {
		t.Fatalf("design's completed deliverable was not persisted: %v", st2.Messages())
	}
	if err := (&Executor{Checkpointer: cp, MaxParallel: 1}).Run(context.Background(), g, st2); err != nil {
		t.Fatal(err)
	}
	if f, ok := st2.FinalOutput(); !ok || f != "final" {
		t.Fatal("resumed run did not finish")
	}
	if runs["ceo"] != 2 || runs["coo"] != 2 || runs["design"] != 1 || runs["cto"] != 2 {
		t.Fatalf("re-execution counts wrong: %v (design must be 1, cto 2 = one failure + one success)", runs)
	}
	if _, err := cp.Load(context.Background(), "nope"); !errors.Is(err, ErrNoCheckpoint) {
		t.Fatalf("missing checkpoint: %v", err)
	}
}

func TestPendingSpecialistsUsesCorrelation(t *testing.T) {
	h := hier()
	s := NewState("", "b", []string{"ceo"})
	s.SetEdges(h.Graph())
	s.MustAppend(AgentMessage{From: "coo", To: "cto", Topic: TopicAssignment, CorrelationID: "r1"})
	s.MustAppend(AgentMessage{From: "cto", To: "coo", Topic: TopicDeliverable, CorrelationID: "r1"})
	if p := h.PendingSpecialists(s); len(p) != 0 {
		t.Fatalf("answered assignment still pending: %v", p)
	}
	s.MustAppend(AgentMessage{From: "coo", To: "cto", Topic: TopicAssignment, CorrelationID: "r2"})
	if p := h.PendingSpecialists(s); !reflect.DeepEqual(p, []string{"cto"}) {
		t.Fatalf("second round not pending: %v", p)
	}
}

// TestResumeAfterHubFailure — a hub (COO) that fails mid-call must not look
// "consumed" on resume, or the router would declare the run finished with no
// final output (seen in the first real kill/resume test).
func TestResumeAfterHubFailure(t *testing.T) {
	h := hier()
	st := NewState("run-hub", "brief", []string{"ceo"})
	st.SetEdges(h.Graph())
	st.MustAppend(AgentMessage{From: UserSender, To: "ceo", Topic: TopicBrief, Payload: "brief"})
	st.MarkConsumed("ceo", 1)
	st.MustAppend(AgentMessage{From: "ceo", To: "coo", Topic: TopicDirection, Payload: "go"})
	st.markVisited("ceo")
	// Old behaviour: COO marked consumed, then its backend call failed.
	st.MarkConsumed("coo", 1)
	snap := st.Snapshot()
	r := Restore(snap, []string{"ceo"})
	r.SetEdges(h.Graph())
	if got := h.Next(r); !reflect.DeepEqual(got, []string{"coo"}) {
		t.Fatalf("resume should re-run the failed hub, got %v", got)
	}
}
