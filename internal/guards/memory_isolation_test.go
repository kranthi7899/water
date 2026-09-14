package guards_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"water/internal/agent"
	"water/internal/backend"
	"water/internal/memory"
	"water/internal/orchestrator"
	"water/internal/persona"
	"water/internal/roles"
)

// Guard 2 — memory isolation, behavioural half. Seed each role with a unique
// secret, run the graph, and assert every request a role sent contains its own
// secret and no other role's secret, and no outbox message not addressed to it.
func TestGuard_MemoryIsolation_Prompts(t *testing.T) {
	mem, err := memory.NewMarkdown(memory.Options{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := roles.Load(persona.NewEmbedded(standardTree()), mem)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	secret := map[string]string{}
	for _, r := range reg.All() {
		secret[r.Slug] = "SECRET-" + strings.ToUpper(r.Slug) + "-7f3a"
		if err := r.Memory().Add(ctx, memory.Entry{Text: secret[r.Slug]}); err != nil {
			t.Fatal(err)
		}
	}

	fake := backend.NewFake("fake-sub")
	fake.Reply = func(req backend.Request) string {
		// The CEO's decomposition addresses each delegate by heading; each
		// payload carries a marker so we can detect leakage across inboxes.
		if req.Role == "ceo" && !strings.Contains(req.Prompt, "[report]") {
			return "# cfo\nPRIVATE-FOR-CFO\n# cto\nPRIVATE-FOR-CTO\n# fourth\nPRIVATE-FOR-FOURTH\n"
		}
		return "report from " + req.Role + " PRIVATE-REPORT-" + strings.ToUpper(req.Role)
	}

	g := &orchestrator.Graph{Nodes: map[string]orchestrator.Node{}}
	var delegates []string
	for _, r := range reg.Delegates() {
		delegates = append(delegates, r.Slug)
	}
	for _, r := range reg.All() {
		g.Nodes[r.Slug] = agent.Node(r, agent.Env{Backend: fake}, delegates)
	}
	g.Router = &orchestrator.CEOFanoutRouter{CEO: "ceo", Delegates: delegates}
	st := orchestrator.NewState("", "the brief", reg.OrchestratorSlugs())
	if err := (&orchestrator.Executor{}).Run(ctx, g, st); err != nil {
		t.Fatal(err)
	}

	msgs := st.Messages()
	for _, req := range fake.Requests() {
		full := req.System + "\n" + req.Prompt
		for slug, sec := range secret {
			has := strings.Contains(full, sec)
			if slug == req.Role && !has {
				t.Errorf("%s prompt is missing its own memory", req.Role)
			}
			if slug != req.Role && has {
				t.Errorf("%s prompt contains %s's memory", req.Role, slug)
			}
		}
		for _, m := range msgs {
			if m.To != req.Role && m.Payload != "" && strings.Contains(req.Prompt, m.Payload) {
				// The brief is legitimately re-quoted by the CEO's decomposition
				// only if the CEO chose to; here the fake does not, so any
				// cross-inbox payload is a leak.
				t.Errorf("%s prompt contains message %s addressed to %s", req.Role, m.ID, m.To)
			}
		}
	}
	// Delegates must never see the raw brief (it was routed to the CEO only).
	for _, req := range fake.Requests() {
		if req.Role != "ceo" && strings.Contains(req.Prompt, "the brief") {
			t.Errorf("%s received the brief, which was addressed to ceo", req.Role)
		}
	}
}

// Guard 2 — structural half. The memory handle a role exposes has no method
// that accepts a role identifier, and roles.Role exposes nothing typed as the
// unscoped memory.Provider. If someone adds one, this fails.
func TestGuard_MemoryIsolation_APISurface(t *testing.T) {
	scoped := reflect.TypeOf((*memory.Scoped)(nil)).Elem()
	want := map[string]int{"Role": 0, "Snapshot": 1, "Add": 2, "Replace": 3, "Remove": 2}
	if scoped.NumMethod() != len(want) {
		t.Fatalf("memory.Scoped has %d methods; want %d — review any addition for a role parameter", scoped.NumMethod(), len(want))
	}
	for i := 0; i < scoped.NumMethod(); i++ {
		m := scoped.Method(i)
		n, ok := want[m.Name]
		if !ok {
			t.Errorf("unexpected method %s on memory.Scoped", m.Name)
			continue
		}
		if m.Type.NumIn() != n {
			t.Errorf("memory.Scoped.%s takes %d params; want %d", m.Name, m.Type.NumIn(), n)
		}
	}

	providerT := reflect.TypeOf((*memory.Provider)(nil)).Elem()
	roleT := reflect.TypeOf(&roles.Role{})
	for i := 0; i < roleT.NumMethod(); i++ {
		m := roleT.Method(i)
		for j := 0; j < m.Type.NumOut(); j++ {
			if out := m.Type.Out(j); out == providerT || out.Implements(providerT) {
				t.Errorf("roles.Role.%s returns an unscoped memory provider", m.Name)
			}
		}
	}
	rt := roleT.Elem()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.IsExported() && (f.Type == providerT || f.Type.Implements(providerT)) {
			t.Errorf("roles.Role exported field %s is an unscoped memory provider", f.Name)
		}
	}
}

// Two Scoped handles over one provider never see each other's entries.
func TestGuard_MemoryIsolation_ScopedHandles(t *testing.T) {
	mem, _ := memory.NewMarkdown(memory.Options{Root: t.TempDir()})
	a, b := memory.Bind(mem, "cfo"), memory.Bind(mem, "cto")
	ctx := context.Background()
	_ = a.Add(ctx, memory.Entry{Text: "cfo-only"})
	_ = b.Add(ctx, memory.Entry{Text: "cto-only"})
	as, _ := a.Snapshot(ctx)
	bs, _ := b.Snapshot(ctx)
	if len(as) != 1 || as[0].Text != "cfo-only" || len(bs) != 1 || bs[0].Text != "cto-only" {
		t.Fatalf("scoped handles leaked: %v / %v", as, bs)
	}
}
