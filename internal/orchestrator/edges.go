package orchestrator

import (
	"fmt"
	"sort"
	"strings"
)

// Edge is one permitted From → To channel, optionally restricted to topics.
// An empty Topics list permits any topic on that edge.
type Edge struct {
	From   string
	To     string
	Topics []string
}

// PermissionGraph is the Part 6.3 DAG: messages flow only along declared
// edges. Specialists do not message each other; escalation edges to the CEO
// are terminal safety valves restricted to escalation/dissent topics.
type PermissionGraph struct {
	edges map[string][]Edge // keyed by From
}

// NewPermissionGraph builds a graph from explicit edges.
func NewPermissionGraph(edges ...Edge) *PermissionGraph {
	g := &PermissionGraph{edges: map[string][]Edge{}}
	for _, e := range edges {
		g.edges[e.From] = append(g.edges[e.From], e)
	}
	return g
}

// Hierarchy returns the canonical two-tier graph:
//
//	CEO      → COO                    direction, decisions
//	COO      → specialists            assignments, research requests
//	spec     → COO                    deliverables, status, dissent
//	COO      → CEO                    status with evidence, forwarded dissent
//	spec     → CEO   (escalation only, terminal)
//
// Every role may also message the user (final output travels separately via
// SetFinalOutput, so no edge is needed for it).
func Hierarchy(ceo, coo string, specialists []string) *PermissionGraph {
	edges := []Edge{
		{From: ceo, To: coo, Topics: []string{TopicDirection, TopicDecision, TopicAssignment}},
		{From: coo, To: ceo, Topics: []string{TopicStatus, TopicDissent, TopicEscalation, TopicDeliverable}},
	}
	for _, sp := range specialists {
		edges = append(edges,
			Edge{From: coo, To: sp, Topics: []string{TopicAssignment}},
			Edge{From: sp, To: coo, Topics: []string{TopicDeliverable, TopicStatus, TopicDissent}},
			Edge{From: sp, To: ceo, Topics: []string{TopicEscalation}},
		)
	}
	return NewPermissionGraph(edges...)
}

// Check returns nil if m travels a declared edge with a permitted topic.
func (g *PermissionGraph) Check(m AgentMessage) error {
	if g == nil {
		return nil
	}
	for _, e := range g.edges[m.From] {
		if e.To != m.To {
			continue
		}
		if len(e.Topics) == 0 {
			return nil
		}
		for _, t := range e.Topics {
			if t == m.Topic {
				return nil
			}
		}
		return fmt.Errorf("%w: %s → %s does not permit topic %q (allowed: %s)", ErrEdgeForbidden, m.From, m.To, m.Topic, strings.Join(e.Topics, ", "))
	}
	return fmt.Errorf("%w: no edge %s → %s", ErrEdgeForbidden, m.From, m.To)
}

// Allowed reports whether from may send to with topic.
func (g *PermissionGraph) Allowed(from, to, topic string) bool {
	return g.Check(AgentMessage{From: from, To: to, Topic: topic}) == nil
}

// Edges returns every declared edge, sorted, for status and dashboard output.
func (g *PermissionGraph) Edges() []Edge {
	var out []Edge
	for _, es := range g.edges {
		out = append(out, es...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}
