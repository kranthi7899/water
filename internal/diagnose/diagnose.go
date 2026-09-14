// Package diagnose validates the orchestration topology empirically from a
// run's trace and checkpoint (Part 6.7). It reads JSONL traces and the state
// snapshot — never a second source of truth — and is consumed by
// `water diagnose <run-id>` and the dashboard.
package diagnose

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"water/internal/orchestrator"
	"water/internal/trace"
)

// Finding is one diagnostic result.
type Finding struct {
	Check    string `json:"check"`
	Severity string `json:"severity"` // ok | info | warn | fail
	Detail   string `json:"detail"`
}

// Report is the full diagnostic output for a run.
type Report struct {
	RunID    string    `json:"run_id"`
	Findings []Finding `json:"findings"`
	// Derived facts the dashboard also renders.
	Messages      int                    `json:"messages"`
	Escalations   int                    `json:"escalations"`
	Dissents      int                    `json:"dissents"`
	DissentRate   float64                `json:"dissent_survival_rate"`
	Verified      int                    `json:"verified_items"`
	Unconfirmed   int                    `json:"unconfirmed_items"`
	NodeTimings   map[string]int64       `json:"node_timings_ms"`
	VisitCounts   map[string]int         `json:"visit_counts"`
	Influence     map[string]float64     `json:"influence"`
	ToolCalls     int                    `json:"tool_calls"`
	ToolDenials   int                    `json:"tool_denials"`
	FailedVerif   int                    `json:"failed_verification_items"`
	RateLimited   int                    `json:"rate_limit_interruptions"`
	EdgeViolation []string               `json:"edge_violations,omitempty"`
	Events        []trace.Event          `json:"-"`
	Snapshot      *orchestrator.Snapshot `json:"-"`
}

// ReadTrace parses a JSONL trace file.
func ReadTrace(path string) ([]trace.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []trace.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var e trace.Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// ReadSnapshot parses a checkpoint file; a missing file yields nil, nil.
func ReadSnapshot(path string) (*orchestrator.Snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var s orchestrator.Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Analyze runs the seven checks over a trace and optional snapshot.
func Analyze(runID string, events []trace.Event, snap *orchestrator.Snapshot, edges *orchestrator.PermissionGraph) *Report {
	r := &Report{RunID: runID, NodeTimings: map[string]int64{}, VisitCounts: map[string]int{}, Influence: map[string]float64{}, Events: events, Snapshot: snap}
	var msgs []orchestrator.AgentMessage
	var final string
	if snap != nil {
		msgs = snap.Outbox
		if snap.FinalOutput != nil {
			final = *snap.FinalOutput
		}
		for k, v := range snap.Visits {
			r.VisitCounts[k] = v
		}
	}
	if len(msgs) == 0 {
		for _, e := range events {
			if e.Type == "message" && e.Message != nil {
				msgs = append(msgs, *e.Message)
			}
		}
	}
	r.Messages = len(msgs)
	nodeStarts := map[string]int{}
	for _, e := range events {
		switch e.Type {
		case "node_finished":
			r.NodeTimings[e.Role] += e.DurationMS
			nodeStarts[e.Role]++
		case "tool_call":
			r.ToolCalls++
			if e.Tool != nil && !e.Tool.Allowed {
				r.ToolDenials++
			}
		case "rate_limited":
			r.RateLimited++
		}
	}
	if len(r.VisitCounts) == 0 {
		r.VisitCounts = nodeStarts
	}

	add := func(check, sev, detail string) { r.Findings = append(r.Findings, Finding{check, sev, detail}) }
	if r.RateLimited > 0 {
		add("rate-limit-interruption", "warn", fmt.Sprintf("%d call(s) refused by the subscription window; depth is hitting the budget, consider fewer delegates or a higher bar for delegating", r.RateLimited))
	}

	// 1. Delegate-with-no-influence: did each specialist's deliverable leave a
	// lexical footprint in the final answer? Measured as the fraction of its
	// distinctive terms that appear in FinalOutput.
	specialists := map[string]bool{}
	for _, m := range msgs {
		if m.Topic == orchestrator.TopicDeliverable || m.Topic == orchestrator.TopicReport {
			specialists[m.From] = true
		}
	}
	if final != "" && len(specialists) > 0 {
		ft := termSet(final)
		for sp := range specialists {
			var text strings.Builder
			for _, m := range msgs {
				if m.From == sp {
					text.WriteString(m.Payload + " ")
				}
			}
			terms := distinctive(termSet(text.String()), msgs, sp)
			hit := 0
			for t := range terms {
				if ft[t] {
					hit++
				}
			}
			score := 0.0
			if len(terms) > 0 {
				score = float64(hit) / float64(len(terms))
			}
			r.Influence[sp] = score
			if score < 0.05 && len(terms) >= 10 {
				add("delegate-with-no-influence", "warn", fmt.Sprintf("%s's output left no trace in the final answer (%.0f%% of its distinctive terms)", sp, score*100))
			} else {
				add("delegate-with-no-influence", "ok", fmt.Sprintf("%s: %.0f%% of distinctive terms reached the final", sp, score*100))
			}
		}
	} else if final != "" {
		add("delegate-with-no-influence", "info", "no specialist deliverables in this run (CEO answered alone or single-role run)")
	}

	// 2. Synthesis ≈ single-agent: the final is (near-)identical to one node's
	// own output — the structure did not earn its cost.
	if final != "" && snap != nil {
		for name, art := range snap.Artifacts {
			if strings.HasPrefix(name, "ceo/") {
				continue
			}
			if sim := jaccard(termSet(art), termSet(final)); sim > 0.9 {
				add("synthesis-equals-single-agent", "warn", fmt.Sprintf("final output is %.0f%% identical to artifact %s", sim*100, name))
			}
		}
		if ceoFrame, ok := snap.Artifacts["ceo/frame"]; ok && len(specialists) > 0 {
			if sim := jaccard(termSet(ceoFrame), termSet(final)); sim > 0.9 {
				add("synthesis-equals-single-agent", "warn", fmt.Sprintf("final output is %.0f%% identical to the CEO's own framing; delegation changed nothing", sim*100))
			}
		}
	}

	// 3. Dissent survival rate: fraction of specialist escalations/dissent that
	// reached the CEO byte-identical.
	sent, survived := 0, 0
	ceo := ""
	for _, m := range msgs {
		if m.Topic == orchestrator.TopicDirection || m.Topic == orchestrator.TopicDelegation {
			ceo = m.From
		}
	}
	for _, m := range msgs {
		if m.Topic != orchestrator.TopicDissent && m.Topic != orchestrator.TopicEscalation {
			continue
		}
		if m.From == ceo || m.ForwardedFrom != "" {
			continue
		}
		if m.Topic == orchestrator.TopicEscalation {
			r.Escalations++
		} else {
			r.Dissents++
		}
		sent++
		if m.To == ceo {
			survived++
			continue
		}
		for _, f := range msgs {
			if f.To == ceo && f.Verbatim && f.Payload == m.Payload && (f.ForwardedFrom == m.From) {
				survived++
				break
			}
		}
	}
	if sent > 0 {
		r.DissentRate = float64(survived) / float64(sent)
		sev := "ok"
		if r.DissentRate < 1 {
			sev = "fail"
		}
		add("dissent-survival", sev, fmt.Sprintf("%d/%d specialist dissent/escalation messages reached the CEO verbatim", survived, sent))
	} else {
		add("dissent-survival", "info", "no dissent or escalation was raised")
	}

	// 4. Step repetition / non-termination.
	for role, n := range r.VisitCounts {
		if n > 3 {
			add("step-repetition", "warn", fmt.Sprintf("%s ran %d times", role, n))
		}
	}
	finished := false
	for _, e := range events {
		if e.Type == "run_finished" {
			finished = true
		}
		if e.Type == "error" && strings.Contains(e.Error, "stalled") {
			add("non-termination", "fail", e.Error)
		}
	}
	if !finished {
		add("non-termination", "warn", "trace has no run_finished event (killed or still running)")
	}

	// 5. Unverified done: COO status items without evidence marks.
	for _, m := range msgs {
		if m.Topic != orchestrator.TopicStatus {
			continue
		}
		up := strings.ToUpper(m.Payload)
		f := strings.Count(up, "FAILED VERIFICATION:")
		v := strings.Count(up, "VERIFIED:") - f // "FAILED VERIFICATION:" contains "VERIFICATION:", not "VERIFIED:"; keep the subtraction defensive
		if v < 0 {
			v = 0
		}
		u := strings.Count(up, "UNCONFIRMED:")
		r.Verified += v
		r.Unconfirmed += u
		r.FailedVerif += f
		if f > 0 {
			add("failed-verification", "fail", fmt.Sprintf("status %s: %d evidence reference(s) did not resolve in this run's trace", m.ID, f))
		}
		if v+u == 0 {
			add("unverified-done", "warn", fmt.Sprintf("status %s from %s carries no VERIFIED/UNCONFIRMED marks", m.ID, m.From))
		} else {
			add("unverified-done", "ok", fmt.Sprintf("status %s: %d verified, %d unconfirmed", m.ID, v, u))
		}
	}

	// 6. Role violation: edges outside the permission graph; a CEO doing
	// assignment mechanics; a specialist writing final output.
	if edges != nil {
		for _, m := range msgs {
			if m.From == orchestrator.UserSender {
				continue
			}
			if err := edges.Check(m); err != nil {
				r.EdgeViolation = append(r.EdgeViolation, fmt.Sprintf("%s: %v", m.ID, err))
			}
		}
	}
	for _, m := range msgs {
		if m.From == ceo && m.Topic == orchestrator.TopicAssignment {
			r.EdgeViolation = append(r.EdgeViolation, fmt.Sprintf("%s: CEO issued an assignment (execution mechanics belong to the COO)", m.ID))
		}
	}
	if len(r.EdgeViolation) > 0 {
		add("role-violation", "fail", strings.Join(r.EdgeViolation, "; "))
	} else {
		add("role-violation", "ok", "every message travelled a permitted edge")
	}

	// 7. Convergence: all specialists saying substantially the same thing.
	var outs []string
	for sp := range specialists {
		var text strings.Builder
		for _, m := range msgs {
			if m.From == sp && (m.Topic == orchestrator.TopicDeliverable || m.Topic == orchestrator.TopicReport) {
				text.WriteString(m.Payload + " ")
			}
		}
		outs = append(outs, text.String())
	}
	if len(outs) >= 2 {
		for i := 0; i < len(outs); i++ {
			for j := i + 1; j < len(outs); j++ {
				if sim := jaccard(termSet(outs[i]), termSet(outs[j])); sim > 0.6 {
					add("convergence", "warn", fmt.Sprintf("two specialists' deliverables are %.0f%% similar; personas may not be differentiated or decomposition was wrong", sim*100))
				}
			}
		}
	}
	sort.SliceStable(r.Findings, func(i, j int) bool { return sevRank(r.Findings[i].Severity) < sevRank(r.Findings[j].Severity) })
	return r
}

func sevRank(s string) int {
	switch s {
	case "fail":
		return 0
	case "warn":
		return 1
	case "info":
		return 2
	}
	return 3
}

func termSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.Fields(strings.ToLower(s)) {
		f = strings.Trim(f, ".,;:!?()[]{}\"'`*#-")
		if len(f) >= 5 {
			out[f] = true
		}
	}
	return out
}

// distinctive keeps terms that appear in sp's output and in no other
// specialist's output, so shared vocabulary does not count as influence.
func distinctive(terms map[string]bool, msgs []orchestrator.AgentMessage, sp string) map[string]bool {
	others := map[string]bool{}
	for _, m := range msgs {
		if m.From != sp && m.From != orchestrator.UserSender && (m.Topic == orchestrator.TopicDeliverable || m.Topic == orchestrator.TopicReport || m.Topic == orchestrator.TopicAssignment || m.Topic == orchestrator.TopicDirection || m.Topic == orchestrator.TopicBrief) {
			for t := range termSet(m.Payload) {
				others[t] = true
			}
		}
	}
	out := map[string]bool{}
	for t := range terms {
		if !others[t] {
			out[t] = true
		}
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	inter := 0
	for t := range a {
		if b[t] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
