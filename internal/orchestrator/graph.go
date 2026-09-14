package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Node is one unit of work. It reads its inbox from s, calls a backend, and
// appends messages. Identical contract for every role.
type Node func(ctx context.Context, s *State) error

// Router is the pluggable graph strategy (extension point 5). Next returns the
// set of nodes to run in parallel for the next step; empty = run complete.
type Router interface {
	Name() string
	Entry() string
	Next(s *State) []string
}

// Graph binds nodes to a router.
type Graph struct {
	Nodes  map[string]Node
	Router Router
}

// Hooks let surfaces and tracers observe execution without the executor
// knowing about them.
type Hooks struct {
	StepStarted  func(step int, nodes []string)
	NodeStarted  func(role string)
	NodeFinished func(role string, dur time.Duration, err error)
}

// Executor runs a Graph to completion.
type Executor struct {
	MaxParallel  int
	Timeout      time.Duration // whole-run ceiling; 0 = none
	MaxSteps     int           // guard against router cycles; 0 = 64
	Checkpointer Checkpointer
	Hooks        Hooks
}

// ErrNoCheckpoint is returned by Load when nothing is stored.
var ErrNoCheckpoint = errors.New("no checkpoint for run")

// NodeError wraps a failure in a specific node.
type NodeError struct {
	Role string
	Err  error
}

func (e *NodeError) Error() string { return fmt.Sprintf("node %s: %v", e.Role, e.Err) }
func (e *NodeError) Unwrap() error { return e.Err }

// Run executes the graph starting from the router's Entry. The brief is
// injected as an AgentMessage from "user" to the entry node if the outbox is
// empty, so even the entry role receives context only through its inbox.
func (e *Executor) Run(ctx context.Context, g *Graph, s *State) error {
	if g.Router == nil {
		return errors.New("graph has no router")
	}
	if _, ok := g.Nodes[g.Router.Entry()]; !ok {
		return fmt.Errorf("router %s entry %q is not a node", g.Router.Name(), g.Router.Entry())
	}
	if e.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.Timeout)
		defer cancel()
	}
	maxSteps := e.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 64
	}
	par := e.MaxParallel
	if par <= 0 {
		par = 4
	}
	cp := e.Checkpointer
	if cp == nil {
		cp = NoopCheckpointer{}
	}
	if len(s.Messages()) == 0 && s.Brief != "" {
		s.AppendMessage(AgentMessage{From: UserSender, To: g.Router.Entry(), Topic: TopicBrief, Payload: s.Brief})
	}

	for step := 1; ; step++ {
		if step > maxSteps {
			return fmt.Errorf("router %s exceeded %d steps without completing", g.Router.Name(), maxSteps)
		}
		next := g.Router.Next(s)
		if len(next) == 0 {
			return nil
		}
		for _, n := range next {
			if _, ok := g.Nodes[n]; !ok {
				return fmt.Errorf("router %s scheduled unknown node %q", g.Router.Name(), n)
			}
		}
		if e.Hooks.StepStarted != nil {
			e.Hooks.StepStarted(step, next)
		}
		if err := e.runStep(ctx, g, s, next, par); err != nil {
			return err
		}
		if err := cp.Save(ctx, s); err != nil {
			return fmt.Errorf("checkpoint: %w", err)
		}
	}
}

func (e *Executor) runStep(ctx context.Context, g *Graph, s *State, nodes []string, par int) error {
	sem := make(chan struct{}, par)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for _, name := range nodes {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			defer func() { <-sem }()
			if e.Hooks.NodeStarted != nil {
				e.Hooks.NodeStarted(name)
			}
			start := time.Now()
			err := e.safeRun(ctx, g.Nodes[name], s)
			s.markVisited(name)
			if e.Hooks.NodeFinished != nil {
				e.Hooks.NodeFinished(name, time.Since(start), err)
			}
			if err != nil {
				mu.Lock()
				errs = append(errs, &NodeError{Role: name, Err: err})
				mu.Unlock()
			}
		}(name)
	}
	wg.Wait()
	if len(errs) == 1 {
		return errs[0]
	}
	if len(errs) > 1 {
		var parts []string
		for _, err := range errs {
			parts = append(parts, err.Error())
		}
		return errors.New(strings.Join(parts, "; "))
	}
	return nil
}

func (e *Executor) safeRun(ctx context.Context, n Node, s *State) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return n(ctx, s)
}
