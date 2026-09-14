// Package orchestrator is a native state-graph executor (LangGraph's
// node/state/edge shape, in Go) used for orchestration and inter-agent
// communication ONLY — never for memory, never for model calls.
package orchestrator

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// AgentMessage is the ONLY inter-agent channel. Nodes exchange information by
// appending typed messages; a node's prompt may include only messages
// addressed to it.
type AgentMessage struct {
	ID      string    `json:"id"`
	From    string    `json:"from"`
	To      string    `json:"to"` // "" = broadcast (Phase 1: reserved, unused)
	Topic   string    `json:"topic"`
	Payload string    `json:"payload"`
	At      time.Time `json:"at"`
}

// UserSender is the From value for the brief injected at graph entry.
const UserSender = "user"

// Well-known topics used by the CEO fan-out router and role nodes.
const (
	TopicBrief      = "brief"
	TopicDelegation = "delegation"
	TopicReport     = "report"
)

// ErrNotOrchestrator is returned when a non-orchestrator role tries to write
// FinalOutput.
var ErrNotOrchestrator = errors.New("only an orchestrator role may write FinalOutput")

// State is the shared graph state. All mutation goes through mutex-guarded
// methods; raw slice/map access is never exposed.
type State struct {
	mu sync.Mutex

	RunID string
	Brief string

	outbox        []AgentMessage
	artifacts     map[string]string
	finalOutput   *string
	visits        map[string]int
	orchestrators map[string]bool
	onMessage     func(AgentMessage)
	StartedAt     time.Time
}

// NewState builds a State for one run. orchestratorRoles are the only roles
// permitted to call SetFinalOutput.
func NewState(runID, brief string, orchestratorRoles []string) *State {
	if runID == "" {
		runID = NewRunID()
	}
	s := &State{
		RunID:         runID,
		Brief:         brief,
		artifacts:     map[string]string{},
		visits:        map[string]int{},
		orchestrators: map[string]bool{},
		StartedAt:     time.Now(),
	}
	for _, r := range orchestratorRoles {
		s.orchestrators[r] = true
	}
	return s
}

// NewRunID returns a sortable, unique run identifier.
func NewRunID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// AppendMessage adds a message to the outbox, assigning ID/At if missing.
func (s *State) AppendMessage(m AgentMessage) AgentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.ID == "" {
		m.ID = fmt.Sprintf("msg-%03d", len(s.outbox)+1)
	}
	if m.At.IsZero() {
		m.At = time.Now()
	}
	s.outbox = append(s.outbox, m)
	obs := s.onMessage
	s.mu.Unlock()
	if obs != nil {
		obs(m)
	}
	s.mu.Lock()
	return m
}

// Observe registers a callback invoked for every appended message (tracing,
// surfaces). Observers never receive a handle to State.
func (s *State) Observe(fn func(AgentMessage)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onMessage = fn
}

// Inbox returns a copy of the messages addressed to role. This filtered slice
// is the only part of shared state permitted into a node's prompt.
func (s *State) Inbox(role string) []AgentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []AgentMessage
	for _, m := range s.outbox {
		if m.To == role {
			out = append(out, m)
		}
	}
	return out
}

// Messages returns a copy of the whole outbox — for tracing, surfaces, and
// routers. Never for prompt assembly.
func (s *State) Messages() []AgentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]AgentMessage(nil), s.outbox...)
}

// SetArtifact stores a named intermediate output.
func (s *State) SetArtifact(name, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.artifacts[name] = value
}

// Artifact reads a named intermediate output.
func (s *State) Artifact(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.artifacts[name]
	return v, ok
}

// Artifacts returns a copy of all artifacts.
func (s *State) Artifacts() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.artifacts))
	for k, v := range s.artifacts {
		out[k] = v
	}
	return out
}

// SetFinalOutput records the run's final synthesis. Only a role registered as
// an orchestrator may call it; anything else returns ErrNotOrchestrator.
func (s *State) SetFinalOutput(role, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.orchestrators[role] {
		return fmt.Errorf("%w: %q attempted to write FinalOutput", ErrNotOrchestrator, role)
	}
	t := text
	s.finalOutput = &t
	return nil
}

// FinalOutput returns the final synthesis, if written.
func (s *State) FinalOutput() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finalOutput == nil {
		return "", false
	}
	return *s.finalOutput, true
}

// IsOrchestrator reports whether role may write FinalOutput.
func (s *State) IsOrchestrator(role string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.orchestrators[role]
}

// markVisited is called by the executor after a node completes.
func (s *State) markVisited(role string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.visits[role]++
}

// Visits reports how many times a node has completed in this run. Routers use
// it to derive the current phase without hidden internal counters, which keeps
// routing resumable from a checkpoint.
func (s *State) Visits(role string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.visits[role]
}

// Snapshot is the serialisable form of State used by checkpointers/traces.
type Snapshot struct {
	RunID       string            `json:"run_id"`
	Brief       string            `json:"brief"`
	Outbox      []AgentMessage    `json:"outbox"`
	Artifacts   map[string]string `json:"artifacts"`
	FinalOutput *string           `json:"final_output"`
	Visits      map[string]int    `json:"visits"`
	StartedAt   time.Time         `json:"started_at"`
}

// Snapshot returns a deep copy suitable for serialisation.
func (s *State) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{
		RunID:     s.RunID,
		Brief:     s.Brief,
		Outbox:    append([]AgentMessage(nil), s.outbox...),
		Artifacts: map[string]string{},
		Visits:    map[string]int{},
		StartedAt: s.StartedAt,
	}
	for k, v := range s.artifacts {
		snap.Artifacts[k] = v
	}
	for k, v := range s.visits {
		snap.Visits[k] = v
	}
	if s.finalOutput != nil {
		t := *s.finalOutput
		snap.FinalOutput = &t
	}
	return snap
}

// Restore rebuilds a State from a Snapshot (for checkpoint resume).
func Restore(snap Snapshot, orchestratorRoles []string) *State {
	s := NewState(snap.RunID, snap.Brief, orchestratorRoles)
	s.outbox = append([]AgentMessage(nil), snap.Outbox...)
	for k, v := range snap.Artifacts {
		s.artifacts[k] = v
	}
	for k, v := range snap.Visits {
		s.visits[k] = v
	}
	if snap.FinalOutput != nil {
		t := *snap.FinalOutput
		s.finalOutput = &t
	}
	if !snap.StartedAt.IsZero() {
		s.StartedAt = snap.StartedAt
	}
	return s
}
