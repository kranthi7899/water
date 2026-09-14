package guards_test

import (
	"errors"
	"strings"
	"testing"

	"water/internal/orchestrator"
	"water/internal/persona"
	"water/internal/roles"
)

// Guard 3 — singleton. Two singleton roles must fail loudly at load, and a
// non-orchestrator role writing FinalOutput must error.
func TestGuard_TwoSingletonsFailLoudly(t *testing.T) {
	src := persona.NewEmbedded(tree(
		roleSpec{slug: "ceo", singleton: true, orchestrator: true},
		roleSpec{slug: "coo", singleton: true, orchestrator: true},
		roleSpec{slug: "cfo"},
	))
	_, err := roles.Load(src, nil)
	if err == nil {
		t.Fatal("expected load failure with two singletons")
	}
	var le *roles.LoadError
	if !errors.As(err, &le) || !strings.Contains(err.Error(), "singleton") {
		t.Fatalf("error does not name the singleton violation: %v", err)
	}
}

func TestGuard_NoOrchestratorFails(t *testing.T) {
	_, err := roles.Load(persona.NewEmbedded(tree(roleSpec{slug: "cfo"}, roleSpec{slug: "cto"})), nil)
	if err == nil || !strings.Contains(err.Error(), "orchestrator") {
		t.Fatalf("expected orchestrator-missing failure, got %v", err)
	}
}

func TestGuard_SlugMismatchFails(t *testing.T) {
	m := tree(roleSpec{slug: "ceo", singleton: true, orchestrator: true})
	m["cfo/role.yaml"] = m["ceo/role.yaml"] // folder cfo claims slug ceo
	_, err := roles.Load(persona.NewEmbedded(m), nil)
	if err == nil || !strings.Contains(err.Error(), "does not match folder") {
		t.Fatalf("expected slug/folder mismatch failure, got %v", err)
	}
}

func TestGuard_NonOrchestratorCannotWriteFinalOutput(t *testing.T) {
	st := orchestrator.NewState("", "brief", []string{"ceo"})
	if err := st.SetFinalOutput("cfo", "nope"); !errors.Is(err, orchestrator.ErrNotOrchestrator) {
		t.Fatalf("got %v, want ErrNotOrchestrator", err)
	}
	if _, ok := st.FinalOutput(); ok {
		t.Fatal("FinalOutput was written by a non-orchestrator")
	}
	if err := st.SetFinalOutput("ceo", "yes"); err != nil {
		t.Fatal(err)
	}
}
