package guards_test

import (
	"context"
	"strings"
	"testing"

	"water/internal/agent"
	"water/internal/backend"
	"water/internal/memory"
	"water/internal/orchestrator"
	"water/internal/persona"
	"water/internal/roles"
)

// hierTree is the canonical four-role tree used by the Part 6 guards.
func hierTree() persona.Source {
	return persona.NewEmbedded(tree(
		roleSpec{slug: "ceo", singleton: true, orchestrator: true},
		roleSpec{slug: "coo"},
		roleSpec{slug: "cto"},
		roleSpec{slug: "design"},
	))
}

func buildHierarchy(t *testing.T, fake backend.Backend, env agent.Env) (*orchestrator.Graph, *orchestrator.State, *roles.Registry, *orchestrator.HierarchyRouter) {
	t.Helper()
	mem, _ := memory.NewMarkdown(memory.Options{Root: t.TempDir()})
	reg, err := roles.Load(hierTree(), mem)
	if err != nil {
		t.Fatal(err)
	}
	h := &orchestrator.HierarchyRouter{CEO: "ceo", COO: "coo", Specialists: []string{"cto", "design"}}
	if env.Backend == nil {
		env.Backend = fake
	}
	g := &orchestrator.Graph{Nodes: map[string]orchestrator.Node{}, Router: h}
	for _, r := range reg.All() {
		g.Nodes[r.Slug] = agent.HierarchyNode(r, env, h)
	}
	st := orchestrator.NewState("", "the brief", reg.OrchestratorSlugs())
	st.SetEdges(h.Graph())
	return g, st, reg, h
}

// scriptedBackend plays the four roles through one full hierarchy run.
func scriptedBackend(dissent string) *backend.Fake {
	f := backend.NewFake("fake-sub")
	f.Reply = func(req backend.Request) string {
		switch req.Role {
		case "ceo":
			if strings.Contains(req.Prompt, "[status]") {
				return agent.RouteFinal + "\nFinal answer: proceed in two phases. Tradeoff: speed for certainty. Reverse if throughput tests fail."
			}
			return agent.RouteDelegate + "\nFind out whether the migration is feasible and what it exposes."
		case "coo":
			if strings.Contains(req.Prompt, "[deliverable]") {
				return "VERIFIED: cto measured 400 rps (quoted). UNCONFIRMED: design's timeline rests on their word."
			}
			return "## cto\nMeasure the throughput ceiling.\n## design\nCheck the checkout flow against WCAG 2.2."
		case "cto":
			return "Throughput ceiling measured at 400 rps.\n\n" + agent.MarkDissent + " " + dissent
		case "design":
			return "Checkout fails 1.4.3 contrast.\n\n" + agent.MarkEscalate + " Legal exposure: the checkout fails WCAG 1.4.3 and ships irreversibly on Monday."
		}
		return "?"
	}
	return f
}

// TestDissentForwardedVerbatim — a Verbatim message from a specialist arrives
// at the CEO byte-identical, with attribution, and the CEO's prompt contains
// it word for word. Escalations bypass the COO and also arrive verbatim.
func TestDissentForwardedVerbatim(t *testing.T) {
	const dissent = "I disagree with the March deadline; measured delivery rates cannot reach it — every word of this must survive, including the em-dash and this odd  double space."
	fake := scriptedBackend(dissent)
	g, st, _, _ := buildHierarchy(t, fake, agent.Env{})
	if err := (&orchestrator.Executor{MaxParallel: 4, MaxSteps: 24}).Run(context.Background(), g, st); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.FinalOutput(); !ok {
		t.Fatal("no final output")
	}
	var forwarded, escalated bool
	for _, m := range st.Inbox("ceo") {
		if m.Topic == orchestrator.TopicDissent && m.Verbatim && m.ForwardedFrom == "cto" && m.From == "coo" && m.Payload == dissent {
			forwarded = true
		}
		if m.Topic == orchestrator.TopicEscalation && m.From == "design" && m.Verbatim && strings.HasPrefix(m.Payload, "Legal exposure") {
			escalated = true
		}
	}
	if !forwarded {
		t.Fatalf("dissent did not reach the CEO byte-identical with attribution: %+v", st.Inbox("ceo"))
	}
	if !escalated {
		t.Fatalf("escalation did not reach the CEO directly: %+v", st.Inbox("ceo"))
	}
	// The CEO's adjudication prompt literally contains the dissent text.
	var ceoPrompts []string
	for _, req := range fake.Requests() {
		if req.Role == "ceo" {
			ceoPrompts = append(ceoPrompts, req.Prompt)
		}
	}
	if len(ceoPrompts) != 2 || !strings.Contains(ceoPrompts[1], dissent) || !strings.Contains(ceoPrompts[1], "forwarded verbatim by coo") {
		t.Fatalf("CEO adjudication prompt lacks the verbatim dissent: %v", ceoPrompts)
	}
	// No specialist ever received another specialist's traffic.
	for _, m := range st.Messages() {
		if (m.From == "cto" && m.To == "design") || (m.From == "design" && m.To == "cto") {
			t.Fatalf("specialists messaged each other: %+v", m)
		}
	}
	// Memory isolation and metered-leak hold under the new topology too.
	if fake.Calls() != 6 { // ceo frame, coo assign, cto, design, coo rollup, ceo final
		t.Fatalf("calls = %d, want 6", fake.Calls())
	}
}

// TestCEOAnswersAloneSkipsDelegation — a single-domain brief ends at the CEO.
func TestCEOAnswersAloneSkipsDelegation(t *testing.T) {
	fake := backend.NewFake("fake-sub")
	fake.Reply = func(req backend.Request) string { return agent.RouteAnswer + "\nNo. Here is why." }
	g, st, _, _ := buildHierarchy(t, fake, agent.Env{})
	if err := (&orchestrator.Executor{MaxParallel: 4}).Run(context.Background(), g, st); err != nil {
		t.Fatal(err)
	}
	if f, ok := st.FinalOutput(); !ok || f != "No. Here is why." {
		t.Fatalf("final %q %v", f, ok)
	}
	if fake.Calls() != 1 {
		t.Fatalf("CEO answering alone should cost one call, got %d", fake.Calls())
	}
}

// TestPerRoleBackend — with CEO on one subscription and CTO on another, a run
// traces each node to its backend, with zero metered calls (Part 8 gate).
func TestPerRoleBackend(t *testing.T) {
	claude := scriptedBackend("none")
	claude.FakeName = "claude-subscription"
	codex := scriptedBackend("none")
	codex.FakeName = "codex-subscription"
	metered := backend.NewFake("api")
	metered.Avail.Metered = true

	reg := backend.NewRegistry()
	reg.Register(metered)
	reg.Register(claude)
	reg.Register(codex)
	ctx := context.Background()
	// Resolution goes through the single Select function with per-role input.
	ceoSel, err := backend.Select(ctx, reg, backend.SelectConfig{Preferred: "claude-subscription", Role: "", RoleSlug: "ceo"})
	if err != nil || ceoSel.Backend.Name() != "claude-subscription" {
		t.Fatalf("%v %v", ceoSel, err)
	}
	ctoSel, err := backend.Select(ctx, reg, backend.SelectConfig{Preferred: "claude-subscription", Role: "codex-subscription", RoleSlug: "cto"})
	if err != nil || ctoSel.Backend.Name() != "codex-subscription" || !strings.Contains(ctoSel.Reason, "role.yaml") {
		t.Fatalf("role.yaml backend did not win: %+v %v", ctoSel, err)
	}
	flagSel, _ := backend.Select(ctx, reg, backend.SelectConfig{Flag: "claude-subscription", Role: "codex-subscription", RoleSlug: "cto"})
	if flagSel.Backend.Name() != "claude-subscription" {
		t.Fatal("--backend flag must outrank role.yaml")
	}
	if _, err := backend.Select(ctx, reg, backend.SelectConfig{Role: "api", RoleSlug: "cto"}); err == nil {
		t.Fatal("a role.yaml pointing at a metered backend must still be refused without opt-in")
	}

	env := agent.Env{Backend: claude, RoleBackends: map[string]backend.Backend{"cto": codex}}
	g, st, _, _ := buildHierarchy(t, claude, env)
	if err := (&orchestrator.Executor{MaxParallel: 4}).Run(ctx, g, st); err != nil {
		t.Fatal(err)
	}
	if metered.Calls() != 0 {
		t.Fatalf("metered calls %d", metered.Calls())
	}
	for _, req := range codex.Requests() {
		if req.Role != "cto" {
			t.Fatalf("codex served %s", req.Role)
		}
	}
	for _, req := range claude.Requests() {
		if req.Role == "cto" {
			t.Fatal("claude served cto, which was bound to codex")
		}
	}
	if codex.Calls() != 1 || claude.Calls() != 5 {
		t.Fatalf("calls: codex %d claude %d", codex.Calls(), claude.Calls())
	}
}

// TestConsultNoMemoryLeak — /consult on role B from role A must not expose
// B's memory to A's prompt assembly, nor A's memory to B's.
func TestConsultNoMemoryLeak(t *testing.T) {
	mem, _ := memory.NewMarkdown(memory.Options{Root: t.TempDir()})
	reg, err := roles.Load(hierTree(), mem)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a, _ := reg.Get("ceo")
	b, _ := reg.Get("cto")
	_ = a.Memory().Add(ctx, memory.Entry{Text: "SECRET-A-91bd"})
	_ = b.Memory().Add(ctx, memory.Entry{Text: "SECRET-B-44ef"})
	fake := backend.NewFake("fake-sub")
	fake.Reply = func(req backend.Request) string { return "answer from " + req.Role }
	env := agent.Env{Backend: fake}

	answer, _, err := agent.Consult(ctx, a.Slug, b, env, "what is the throughput ceiling?")
	if err != nil {
		t.Fatal(err)
	}
	if answer.To != "ceo" || answer.From != "cto" || answer.Topic != orchestrator.TopicAnswer {
		t.Fatalf("answer not addressed as an outbox message: %+v", answer)
	}
	// A's next turn includes the answer as an inbox message and its own memory only.
	_, p, err := agent.RunTurn(ctx, a, env, []orchestrator.AgentMessage{answer}, "thanks, decide", nil)
	if err != nil {
		t.Fatal(err)
	}
	full := p.System + "\n" + p.User
	if strings.Contains(full, "SECRET-B-44ef") {
		t.Fatal("consulting role's memory leaked into the asker's prompt")
	}
	if !strings.Contains(full, "SECRET-A-91bd") || !strings.Contains(full, "answer from cto") {
		t.Fatal("asker lost its own memory or the answer")
	}
	for _, req := range fake.Requests() {
		if req.Role == "cto" && strings.Contains(req.System+req.Prompt, "SECRET-A-91bd") {
			t.Fatal("asker's memory leaked into the consulted role's prompt")
		}
	}
	// Smuggling another role's message into RunTurn is refused.
	if _, _, err := agent.RunTurn(ctx, a, env, []orchestrator.AgentMessage{{ID: "x", To: "cto", Payload: "not yours"}}, "hi", nil); err == nil {
		t.Fatal("RunTurn accepted a message addressed to another role")
	}
}
