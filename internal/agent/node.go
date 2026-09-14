// Package agent turns a roles.Role into an orchestrator.Node. This is the ONE
// place a prompt is assembled, and therefore the one place memory isolation and
// inbox filtering are enforced. Nothing here takes a role slug for memory; the
// only memory handle is role.Memory(), already scoped.
package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"water/internal/backend"
	"water/internal/memory"
	"water/internal/orchestrator"
	"water/internal/persona"
	"water/internal/roles"
	"water/internal/surface"
	"water/internal/trace"
)

// Env is what every node shares: the backend, observers, and knobs. It carries
// no state and no memory.
type Env struct {
	Backend  backend.Backend
	Surface  surface.Surface
	Trace    *trace.Recorder
	Selector persona.SkillSelector
	Timeout  time.Duration
}

func (e Env) selector() persona.SkillSelector {
	if e.Selector != nil {
		return e.Selector
	}
	return persona.KeywordSelector{}
}

// Prompt is an assembled request before it reaches the backend, kept as a
// value so tests can inspect exactly what a node would send.
type Prompt struct {
	System string
	User   string
	Skills []string
}

// Assemble builds the request for role from ITS OWN persona, ITS OWN memory
// snapshot, and ONLY the inbox messages passed in (which the caller has
// already filtered to To == role). task is the instruction for this turn.
func Assemble(role *roles.Role, mem []memory.Entry, inbox []orchestrator.AgentMessage, task string, sel persona.SkillSelector) Prompt {
	if sel == nil {
		sel = persona.KeywordSelector{}
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("You are %s, the %s role-agent in the water system.\n", role.Name, role.Slug))
	if role.Orchestrator {
		sb.WriteString("You are the orchestrator: you decompose briefs, delegate to other roles through typed messages, and you alone synthesise the final output.\n")
	} else {
		sb.WriteString("You act on delegated work addressed to you and report back to the orchestrator through a typed message.\n")
	}
	if role.Description != "" {
		sb.WriteString(role.Description + "\n")
	}
	sb.WriteString("\n")

	// Skill selection considers the task plus inbox payloads (the work this
	// turn actually concerns), never other roles' content.
	var taskText strings.Builder
	taskText.WriteString(task)
	for _, m := range inbox {
		taskText.WriteString(" " + m.Payload)
	}
	var selected []persona.Skill
	var skillNames []string
	if role.Persona != nil {
		selected = sel.Select(taskText.String(), role.Persona.Skills)
		for _, s := range selected {
			skillNames = append(skillNames, s.Slug)
		}
		if p := role.Persona.Render(selected); p != "" {
			sb.WriteString(p + "\n\n")
		} else {
			sb.WriteString("<!-- persona unwritten: Phase 1 scaffold -->\n\n")
		}
	}
	if len(mem) > 0 {
		sb.WriteString("# Memory (frozen snapshot, " + role.Slug + " only)\n\n")
		for _, e := range mem {
			sb.WriteString("- " + strings.TrimSpace(e.Text) + "\n")
		}
		sb.WriteString("\n")
	}

	var ub strings.Builder
	if len(inbox) > 0 {
		ub.WriteString("# Inbox\n\n")
		for _, m := range inbox {
			ub.WriteString(fmt.Sprintf("## from %s [%s]\n%s\n\n", m.From, m.Topic, strings.TrimSpace(m.Payload)))
		}
	}
	ub.WriteString("# Task\n\n" + strings.TrimSpace(task) + "\n")
	return Prompt{System: strings.TrimSpace(sb.String()), User: strings.TrimSpace(ub.String()), Skills: skillNames}
}

// Node builds the graph node for role. delegates are the slugs an orchestrator
// fans out to; ignored for non-orchestrator roles.
func Node(role *roles.Role, env Env, delegates []string) orchestrator.Node {
	var once sync.Once
	var snapshot []memory.Entry
	var snapErr error
	return func(ctx context.Context, s *orchestrator.State) error {
		// Memory: loaded ONCE per run per role, then frozen.
		once.Do(func() {
			if role.Memory() != nil {
				snapshot, snapErr = role.Memory().Snapshot(ctx)
			}
		})
		if snapErr != nil {
			return fmt.Errorf("memory snapshot: %w", snapErr)
		}

		// Inbox: the filtered slice is the only shared state that reaches the prompt.
		inbox := s.Inbox(role.Slug)

		var task string
		var phase string
		switch {
		case role.Orchestrator && hasTopic(inbox, orchestrator.TopicReport):
			phase = "synthesis"
			task = "Synthesise the reports in your inbox into a single final answer to the original brief. Output only the final answer."
		case role.Orchestrator:
			phase = "decompose"
			task = fmt.Sprintf("Decompose the brief in your inbox into delegated work. For each of these roles — %s — write a short, specific instruction they can act on independently. Format: one section per role, headed by the role slug.", strings.Join(delegates, ", "))
		default:
			phase = "delegate"
			task = "Carry out the delegation addressed to you and reply with a concise report for the orchestrator."
		}

		p := Assemble(role, snapshot, inbox, task, env.selector())
		req := backend.Request{System: p.System, Prompt: p.User, Role: role.Slug, Timeout: env.Timeout}
		start := time.Now()
		resp, err := env.Backend.Run(ctx, req)
		if resp.Duration == 0 {
			resp.Duration = time.Since(start)
		}
		if env.Trace != nil {
			env.Trace.BackendCall(role.Slug, resp.Backend, resp.Metered, resp.Duration, resp.InputTokens, resp.OutputTokens, err)
		}
		if err != nil {
			if env.Surface != nil {
				env.Surface.NodeFailed(role.Slug, err)
			}
			return err
		}
		if env.Surface != nil {
			env.Surface.NodeFinished(role.Slug, resp)
		}

		s.SetArtifact(role.Slug+"/"+phase, resp.Text)
		switch phase {
		case "decompose":
			for _, d := range delegates {
				s.AppendMessage(orchestrator.AgentMessage{
					From: role.Slug, To: d, Topic: orchestrator.TopicDelegation,
					Payload: sectionFor(resp.Text, d),
				})
			}
		case "delegate":
			to := findOrchestratorSender(inbox)
			if to == "" {
				return fmt.Errorf("no delegation in inbox; cannot address report")
			}
			s.AppendMessage(orchestrator.AgentMessage{From: role.Slug, To: to, Topic: orchestrator.TopicReport, Payload: resp.Text})
		case "synthesis":
			// Runtime check: only an orchestrator role may write FinalOutput.
			if err := s.SetFinalOutput(role.Slug, resp.Text); err != nil {
				return err
			}
		}
		return nil
	}
}

// RunSingle is `water run <role> <prompt>`: one role, no graph. The prompt
// enters as a brief message so the node contract is identical.
func RunSingle(ctx context.Context, role *roles.Role, env Env, prompt string) (backend.Response, Prompt, error) {
	var snap []memory.Entry
	if role.Memory() != nil {
		var err error
		if snap, err = role.Memory().Snapshot(ctx); err != nil {
			return backend.Response{}, Prompt{}, fmt.Errorf("memory snapshot: %w", err)
		}
	}
	inbox := []orchestrator.AgentMessage{{From: orchestrator.UserSender, To: role.Slug, Topic: orchestrator.TopicBrief, Payload: prompt, At: time.Now()}}
	p := Assemble(role, snap, inbox, "Respond to the brief in your inbox.", env.selector())
	start := time.Now()
	resp, err := env.Backend.Run(ctx, backend.Request{System: p.System, Prompt: p.User, Role: role.Slug, Timeout: env.Timeout})
	if resp.Duration == 0 {
		resp.Duration = time.Since(start)
	}
	if env.Trace != nil {
		env.Trace.BackendCall(role.Slug, resp.Backend, resp.Metered, resp.Duration, resp.InputTokens, resp.OutputTokens, err)
	}
	return resp, p, err
}

func hasTopic(inbox []orchestrator.AgentMessage, topic string) bool {
	for _, m := range inbox {
		if m.Topic == topic {
			return true
		}
	}
	return false
}

func findOrchestratorSender(inbox []orchestrator.AgentMessage) string {
	for i := len(inbox) - 1; i >= 0; i-- {
		if inbox[i].Topic == orchestrator.TopicDelegation {
			return inbox[i].From
		}
	}
	return ""
}

// sectionFor extracts the part of a decomposition addressed to slug: the text
// under a heading containing the slug, up to the next heading. Falls back to
// the whole text so a delegate always receives something actionable.
func sectionFor(text, slug string) string {
	lines := strings.Split(text, "\n")
	var out []string
	in := false
	for _, ln := range lines {
		trim := strings.TrimSpace(ln)
		isHead := strings.HasPrefix(trim, "#") || strings.HasPrefix(trim, "**") || strings.HasSuffix(trim, ":")
		if isHead {
			if in {
				break
			}
			if strings.Contains(strings.ToLower(trim), strings.ToLower(slug)) {
				in = true
				continue
			}
		}
		if in {
			out = append(out, ln)
		}
	}
	if s := strings.TrimSpace(strings.Join(out, "\n")); s != "" {
		return s
	}
	return strings.TrimSpace(text)
}
