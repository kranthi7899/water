// Package surface is extension point 7: output/UI consumers of run events.
package surface

import (
	"sort"

	"water/internal/backend"
	"water/internal/orchestrator"
	"water/internal/trace"
)

// RunStats is the end-of-run summary shown by every surface.
type RunStats = trace.Stats

// Surface observes a run. Implementations must be safe for concurrent calls
// (parallel nodes finish concurrently).
type Surface interface {
	Name() string
	RunStarted(runID, brief string)
	NodeStarted(role string)
	NodeFinished(role string, r backend.Response)
	NodeFailed(role string, err error)
	MessageSent(m orchestrator.AgentMessage)
	RunFinished(final string, stats RunStats)
}

// Multi fans events out to several surfaces.
type Multi []Surface

func (m Multi) Name() string { return "multi" }
func (m Multi) RunStarted(runID, brief string) {
	for _, s := range m {
		s.RunStarted(runID, brief)
	}
}
func (m Multi) NodeStarted(role string) {
	for _, s := range m {
		s.NodeStarted(role)
	}
}
func (m Multi) NodeFinished(role string, r backend.Response) {
	for _, s := range m {
		s.NodeFinished(role, r)
	}
}
func (m Multi) NodeFailed(role string, err error) {
	for _, s := range m {
		s.NodeFailed(role, err)
	}
}
func (m Multi) MessageSent(msg orchestrator.AgentMessage) {
	for _, s := range m {
		s.MessageSent(msg)
	}
}
func (m Multi) RunFinished(final string, stats RunStats) {
	for _, s := range m {
		s.RunFinished(final, stats)
	}
}

// Options configure a surface.
type Options struct {
	Quiet   bool
	Verbose bool
}

var factories = map[string]func(Options) Surface{}

// Register adds a surface by name ("terminal", "json", later "dashboard").
func Register(name string, f func(Options) Surface) { factories[name] = f }

// Open constructs the named surface.
func Open(name string, o Options) (Surface, bool) {
	f, ok := factories[name]
	if !ok {
		return nil, false
	}
	return f(o), true
}

// Names lists registered surfaces.
func Names() []string {
	out := make([]string, 0, len(factories))
	for n := range factories {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
