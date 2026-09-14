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
//
// Part 6.4 grew this type deliberately, for two recorded needs and nothing
// beyond them: request/response pairing with timeouts (CorrelationID,
// Deadline) and dissent that cannot be paraphrased (Verbatim). Part 5.5 added
// Untrusted so a node that consumed external content (a file, an image) cannot
// emit an unmarked message. Resist growing it further without a demonstrated
// need, and record the need here when you do.
type AgentMessage struct {
	ID            string    `json:"id"`
	From          string    `json:"from"`
	To            string    `json:"to"`
	Topic         string    `json:"topic"`
	Payload       string    `json:"payload"`
	At            time.Time `json:"at"`
	CorrelationID string    `json:"correlation_id,omitempty"` // request/response pairing; enables timeout
	Deadline      time.Time `json:"deadline,omitempty"`       // zero = none
	Verbatim      bool      `json:"verbatim,omitempty"`       // forwarding nodes MUST NOT paraphrase
	Untrusted     bool      `json:"untrusted,omitempty"`      // sender consumed external (tool/attachment) content this turn
	ForwardedFrom string    `json:"forwarded_from,omitempty"` // original sender when a hub forwards a Verbatim message
}

// UserSender is the From value for the brief injected at graph entry.
const UserSender = "user"

// Well-known topics. The first three are the Phase 1 fan-out vocabulary and
// remain valid; the rest are the Part 6 hierarchy vocabulary.
const (
	TopicBrief      = "brief"
	TopicDelegation = "delegation" // Phase 1 ceo-fanout
	TopicReport     = "report"     // Phase 1 ceo-fanout

	TopicDirection   = "direction"   // CEO → COO
	TopicAssignment  = "assignment"  // COO → specialist
	TopicStatus      = "status"      // COO → CEO, specialist → COO
	TopicDeliverable = "deliverable" // specialist → COO
	TopicEscalation  = "escalation"  // specialist → CEO (terminal safety valve)
	TopicDissent     = "dissent"     // specialist → COO, forwarded verbatim to CEO
	TopicDecision    = "decision"    // CEO → COO (adjudication)
	TopicConsult     = "consult"     // interactive /consult: question to another role
	TopicAnswer      = "answer"      // interactive /consult: reply
)

// MustForwardVerbatim reports whether a message may never be paraphrased by a
// forwarding node (Part 6.5 mechanism 1).
func (m AgentMessage) MustForwardVerbatim() bool {
	return m.Verbatim || m.Topic == TopicEscalation || m.Topic == TopicDissent
}

// ErrNotOrchestrator is returned when a non-orchestrator role tries to write
// FinalOutput.
var ErrNotOrchestrator = errors.New("only an orchestrator role may write FinalOutput")

// ErrEdgeForbidden is returned when a message violates the permission graph.
var ErrEdgeForbidden = errors.New("message violates the permission graph")

// ErrUnmarkedUntrusted is returned when a node that consumed untrusted content
// tries to emit a message without Untrusted: true.
var ErrUnmarkedUntrusted = errors.New("node consumed untrusted content this turn; outbox message must be marked Untrusted")

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
	consumed      map[string]int // inbox length a role had processed at its last visit
	untrusted     map[string]bool
	orchestrators map[string]bool
	edges         *PermissionGraph
	onMessage     func(AgentMessage)
	StartedAt     time.Time
	stepCount     int
	nextNodes     []string
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
		consumed:      map[string]int{},
		untrusted:     map[string]bool{},
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

// SetEdges installs the message permission graph. With no graph, any edge is
// allowed (Phase 1 behaviour). The Part 6 hierarchy always installs one.
func (s *State) SetEdges(g *PermissionGraph) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.edges = g
}

// Edges returns the installed permission graph (nil if none).
func (s *State) Edges() *PermissionGraph {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.edges
}

// AppendMessage validates m against the permission graph and the untrusted
// rule, assigns ID/At if missing, and appends it to the outbox. It returns
// ErrEdgeForbidden or ErrUnmarkedUntrusted on violation; nothing is appended
// in that case.
func (s *State) AppendMessage(m AgentMessage) (AgentMessage, error) {
	s.mu.Lock()
	if s.edges != nil && m.From != UserSender {
		if err := s.edges.Check(m); err != nil {
			s.mu.Unlock()
			return m, err
		}
	}
	if s.untrusted[m.From] && !m.Untrusted {
		s.mu.Unlock()
		return m, fmt.Errorf("%w (from %s)", ErrUnmarkedUntrusted, m.From)
	}
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
	return m, nil
}

// MustAppend is AppendMessage for callers that have already validated the
// edge (tests, the user brief). It panics on a violation so a programming
// error cannot pass silently.
func (s *State) MustAppend(m AgentMessage) AgentMessage {
	out, err := s.AppendMessage(m)
	if err != nil {
		panic(err)
	}
	return out
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

// Unconsumed returns the inbox messages role has not yet processed (those
// appended after its last MarkConsumed). Routers use it to decide whether a
// hub node has new work.
func (s *State) Unconsumed(role string) []AgentMessage {
	in := s.Inbox(role)
	s.mu.Lock()
	n := s.consumed[role]
	s.mu.Unlock()
	if n > len(in) {
		n = len(in)
	}
	return in[n:]
}

// MarkConsumed records that role has processed the first n messages of its
// inbox. Nodes call it with len(inbox) after assembling their prompt.
func (s *State) MarkConsumed(role string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n > s.consumed[role] {
		s.consumed[role] = n
	}
}

// Consumed reports how many inbox messages role has processed.
func (s *State) Consumed(role string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.consumed[role]
}

// MarkUntrusted records that role consumed external content during this run.
// Every subsequent message from role must carry Untrusted: true.
func (s *State) MarkUntrusted(role string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.untrusted[role] = true
}

// IsUntrusted reports whether role has consumed external content.
func (s *State) IsUntrusted(role string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.untrusted[role]
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

// VisitCounts returns a copy of all visit counts.
func (s *State) VisitCounts() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.visits))
	for k, v := range s.visits {
		out[k] = v
	}
	return out
}

// StepCount reports completed supersteps.
func (s *State) StepCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stepCount
}

func (s *State) setStep(step int, next []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stepCount = step
	s.nextNodes = append([]string(nil), next...)
}

// NextNodes reports the nodes the router scheduled for the step after the
// last checkpoint (informational; the router re-derives it from state).
func (s *State) NextNodes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.nextNodes...)
}

// Snapshot is the serialisable form of State used by checkpointers/traces.
type Snapshot struct {
	RunID       string            `json:"run_id"`
	Brief       string            `json:"brief"`
	Outbox      []AgentMessage    `json:"outbox"`
	Artifacts   map[string]string `json:"artifacts"`
	FinalOutput *string           `json:"final_output"`
	Visits      map[string]int    `json:"visit_counts"`
	Consumed    map[string]int    `json:"consumed,omitempty"`
	Untrusted   []string          `json:"untrusted,omitempty"`
	StepCount   int               `json:"step_count"`
	NextNodes   []string          `json:"next_node,omitempty"`
	StartedAt   time.Time         `json:"started_at"`
	SavedAt     time.Time         `json:"saved_at"`
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
		Consumed:  map[string]int{},
		StepCount: s.stepCount,
		NextNodes: append([]string(nil), s.nextNodes...),
		StartedAt: s.StartedAt,
		SavedAt:   time.Now(),
	}
	for k, v := range s.artifacts {
		snap.Artifacts[k] = v
	}
	for k, v := range s.visits {
		snap.Visits[k] = v
	}
	for k, v := range s.consumed {
		snap.Consumed[k] = v
	}
	for k, v := range s.untrusted {
		if v {
			snap.Untrusted = append(snap.Untrusted, k)
		}
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
	for k, v := range snap.Consumed {
		// A role that never completed a visit cannot have consumed anything;
		// repair checkpoints written before the consume-after-success rule.
		if snap.Visits[k] == 0 {
			continue
		}
		s.consumed[k] = v
	}
	for _, r := range snap.Untrusted {
		s.untrusted[r] = true
	}
	s.stepCount = snap.StepCount
	s.nextNodes = append([]string(nil), snap.NextNodes...)
	if snap.FinalOutput != nil {
		t := *snap.FinalOutput
		s.finalOutput = &t
	}
	if !snap.StartedAt.IsZero() {
		s.StartedAt = snap.StartedAt
	}
	return s
}
