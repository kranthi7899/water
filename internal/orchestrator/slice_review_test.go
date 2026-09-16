package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// Review-only regression: cancellation must join started siblings before saving.
func TestSliceReviewCancellationPersistsStartedSibling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	s := NewState("review-cancel", "", []string{"ceo"})
	s.markVisited("ceo")
	cp := &FileCheckpointer{Dir: t.TempDir(), Orchestrators: []string{"ceo"}}
	g := &Graph{Router: &CEOFanoutRouter{CEO: "ceo", Delegates: []string{"a", "b"}}, Nodes: map[string]Node{
		"ceo": func(context.Context, *State) error { return nil },
		"a": func(_ context.Context, s *State) error {
			close(started)
			<-release // controlled completion/cleanup already in flight when cancellation arrives
			s.MustAppend(AgentMessage{From: "a", To: "ceo", Topic: TopicReport, Payload: "completed work"})
			return nil
		},
		"b": func(context.Context, *State) error { return nil },
	}}
	e := Executor{MaxParallel: 1, Checkpointer: cp, Hooks: Hooks{NodeFinished: func(role string, _ time.Duration, _ error) {
		if role == "a" {
			close(finished)
		}
	}}}
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, g, s) }()
	<-started
	begin := time.Now()
	cancel()
	early := false
	var runErr error
	select {
	case runErr = <-done:
		early = true
		t.Logf("Run returned after %s while sibling a was still blocked", time.Since(begin))
	case <-time.After(100 * time.Millisecond):
	}
	unblock()
	<-finished
	if !early {
		runErr = <-done
	}
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("cancel error = %v", runErr)
	}
	saved, err := cp.Load(context.Background(), s.RunID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("live a visits=%d, checkpoint a visits=%d, live reports=%d, checkpoint reports=%d", s.Visits("a"), saved.Visits("a"), len(s.Inbox("ceo")), len(saved.Inbox("ceo")))
	t.Logf("resume schedules %v", g.Router.Next(saved))
	if early || saved.Visits("a") != 1 || len(saved.Inbox("ceo")) != 1 {
		t.Fatal("cancellation returned and checkpointed before the started sibling completed; resume repeats its work")
	}
}
