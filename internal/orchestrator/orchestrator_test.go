package orchestrator

import (
	"context"
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

func TestExecutorStepGuard(t *testing.T) {
	loop := routerFunc{entry: "x", next: func(*State) []string { return []string{"x"} }}
	g := &Graph{Nodes: map[string]Node{"x": func(context.Context, *State) error { return nil }}, Router: loop}
	err := (&Executor{MaxSteps: 3}).Run(context.Background(), g, NewState("", "", nil))
	if err == nil {
		t.Fatal("expected step guard error")
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
	s.AppendMessage(AgentMessage{From: "ceo", To: "cfo", Topic: "x", Payload: "y"})
	s.SetArtifact("k", "v")
	s.markVisited("ceo")
	_ = s.SetFinalOutput("ceo", "done")
	r := Restore(s.Snapshot(), []string{"ceo"})
	if r.RunID != "run1" || len(r.Messages()) != 1 || r.Visits("ceo") != 1 {
		t.Fatal("restore lost state")
	}
	if v, _ := r.Artifact("k"); v != "v" {
		t.Fatal("artifact lost")
	}
	if f, ok := r.FinalOutput(); !ok || f != "done" {
		t.Fatal("final lost")
	}
}
