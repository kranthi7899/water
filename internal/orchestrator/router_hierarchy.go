package orchestrator

import "sort"

// HierarchyRouter is the Part 6 two-tier topology:
//
//	CEO → COO → {specialists} → COO → CEO
//
// Phase is derived entirely from State (visit counts, unconsumed inboxes,
// pending assignments), so the router holds no hidden counters and resumes
// from a checkpoint by re-deriving where it was.
//
// Bounds (6.1): the CEO may answer alone (it writes FinalOutput on its first
// visit and the run ends); COO may re-assign at most MaxRounds times; a hard
// step budget lives in the Executor; a step that changes nothing is a stall.
type HierarchyRouter struct {
	CEO         string
	COO         string
	Specialists []string
	MaxRounds   int // COO assignment rounds before work is forced back to CEO; 0 = 2
}

// HierarchyName is the config value selecting this router.
const HierarchyName = "hierarchy"

func (r *HierarchyRouter) Name() string  { return HierarchyName }
func (r *HierarchyRouter) Entry() string { return r.CEO }

func (r *HierarchyRouter) rounds() int {
	if r.MaxRounds <= 0 {
		return 2
	}
	return r.MaxRounds
}

// MaxRoundsOrDefault exposes the effective assignment-round cap.
func (r *HierarchyRouter) MaxRoundsOrDefault() int { return r.rounds() }

// Graph returns the permission graph this router expects installed on State.
func (r *HierarchyRouter) Graph() *PermissionGraph { return Hierarchy(r.CEO, r.COO, r.Specialists) }

// PendingSpecialists returns specialists holding an assignment they have not
// yet answered (no deliverable/status/dissent from them with the same
// correlation id, or — for uncorrelated assignments — no reply at all after
// the assignment).
func (r *HierarchyRouter) PendingSpecialists(s *State) []string {
	msgs := s.Messages()
	var pending []string
	for _, sp := range r.Specialists {
		if hasPending(msgs, sp) {
			pending = append(pending, sp)
		}
	}
	sort.Strings(pending)
	return pending
}

func hasPending(msgs []AgentMessage, sp string) bool {
	for i, m := range msgs {
		if m.To != sp || m.Topic != TopicAssignment {
			continue
		}
		answered := false
		for _, later := range msgs[i+1:] {
			if later.From != sp {
				continue
			}
			if m.CorrelationID != "" && later.CorrelationID != m.CorrelationID {
				continue
			}
			answered = true
			break
		}
		if !answered {
			return true
		}
	}
	return false
}

// Next derives the next superstep from state.
func (r *HierarchyRouter) Next(s *State) []string {
	if _, done := s.FinalOutput(); done {
		return nil
	}
	if s.Visits(r.CEO) == 0 {
		return []string{r.CEO} // frame: answer alone, or direct COO
	}
	if r.COO == "" {
		// Degenerate deployment with no COO role: CEO must answer alone.
		return nil
	}
	if p := r.PendingSpecialists(s); len(p) > 0 && s.Visits(r.COO) <= r.rounds() {
		return p
	}
	if len(s.Unconsumed(r.COO)) > 0 && s.Visits(r.COO) <= r.rounds() {
		return []string{r.COO}
	}
	if len(s.Unconsumed(r.CEO)) > 0 {
		return []string{r.CEO} // adjudicate + final
	}
	// COO exhausted its rounds but never reported: force CEO to close out.
	if s.Visits(r.COO) > r.rounds() {
		return []string{r.CEO}
	}
	return nil
}

func init() {
	RegisterRouter(HierarchyName, func(ceo string, delegates []string) Router {
		var coo string
		var specialists []string
		for _, d := range delegates {
			if d == "coo" && coo == "" {
				coo = d
				continue
			}
			specialists = append(specialists, d)
		}
		return &HierarchyRouter{CEO: ceo, COO: coo, Specialists: specialists}
	})
}
