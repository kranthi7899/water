package guards_test

import (
	"context"
	"errors"
	"testing"

	"water/internal/agent"
	"water/internal/backend"
	"water/internal/memory"
	"water/internal/orchestrator"
	"water/internal/persona"
	"water/internal/roles"
)

// Guard 1 — metered-leak. With a fake registry where a METERED backend is
// registered FIRST and a non-metered one is also available, no node may ever
// obtain the metered backend under default config.
func TestGuard_MeteredLeak(t *testing.T) {
	metered := backend.NewFake("fake-api")
	metered.Avail = backend.Availability{Installed: true, Authed: true, Metered: true, Detail: "fake metered"}
	sub := backend.NewFake("fake-sub")

	reg := backend.NewRegistry()
	reg.Register(metered) // registered first on purpose
	reg.Register(sub)

	sel, err := backend.Select(context.Background(), reg, backend.SelectConfig{Preferred: "auto", AllowMetered: false})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if sel.Backend.Name() != "fake-sub" {
		t.Fatalf("selected %s, want the non-metered backend", sel.Backend.Name())
	}

	mem, _ := memory.NewMarkdown(memory.Options{Root: t.TempDir()})
	reg2, err := roles.Load(persona.NewEmbedded(standardTree()), mem)
	if err != nil {
		t.Fatal(err)
	}
	g := &orchestrator.Graph{Nodes: map[string]orchestrator.Node{}}
	var delegates []string
	for _, r := range reg2.Delegates() {
		delegates = append(delegates, r.Slug)
	}
	env := agent.Env{Backend: sel.Backend}
	for _, r := range reg2.All() {
		g.Nodes[r.Slug] = agent.Node(r, env, delegates)
	}
	g.Router = &orchestrator.CEOFanoutRouter{CEO: "ceo", Delegates: delegates}
	st := orchestrator.NewState("", "test brief", reg2.OrchestratorSlugs())
	if err := (&orchestrator.Executor{MaxParallel: 4}).Run(context.Background(), g, st); err != nil {
		t.Fatal(err)
	}
	if metered.Calls() != 0 {
		t.Fatalf("metered backend received %d calls; want 0", metered.Calls())
	}
	if sub.Calls() != 5 { // ceo decompose + 3 delegates + ceo synthesis
		t.Fatalf("subscription backend received %d calls; want 5", sub.Calls())
	}
	if _, ok := st.FinalOutput(); !ok {
		t.Fatal("FinalOutput not written")
	}
}

// Only-metered-available must be refused unless allow_metered is set, and an
// explicit metered preference is still refused without the opt-in.
func TestGuard_MeteredRefusedWithoutOptIn(t *testing.T) {
	metered := backend.NewFake("fake-api")
	metered.Avail = backend.Availability{Installed: true, Authed: true, Metered: true}
	reg := backend.NewRegistry()
	reg.Register(metered)

	if _, err := backend.Select(context.Background(), reg, backend.SelectConfig{}); !errors.Is(err, backend.ErrMeteredRefused) {
		t.Fatalf("auto: got %v, want ErrMeteredRefused", err)
	}
	if _, err := backend.Select(context.Background(), reg, backend.SelectConfig{Preferred: "fake-api"}); !errors.Is(err, backend.ErrMeteredRefused) {
		t.Fatalf("explicit: got %v, want ErrMeteredRefused", err)
	}
	sel, err := backend.Select(context.Background(), reg, backend.SelectConfig{AllowMetered: true})
	if err != nil || sel.Backend.Name() != "fake-api" {
		t.Fatalf("opt-in: got %v / %v", sel, err)
	}
}

// Subprocess environments must never carry metered keys.
func TestGuard_ScrubbedEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("OPENAI_API_KEY", "sk-test")
	for _, kv := range backend.ScrubbedEnv() {
		for _, k := range backend.MeteredKeyVars {
			if len(kv) > len(k) && kv[:len(k)+1] == k+"=" {
				t.Fatalf("scrubbed env still contains %s", k)
			}
		}
	}
	if got := backend.LeakedKeys(); len(got) != 2 {
		t.Fatalf("LeakedKeys = %v, want both test keys", got)
	}
}
