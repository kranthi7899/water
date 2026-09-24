package guards_test

import (
	"context"
	"errors"
	"testing"

	"water/internal/backend"
	"water/internal/runtime"
	"water/internal/twins"
)

// Guard 1 — metered-leak. With a fake registry where a METERED backend is
// registered FIRST and a non-metered one is also available, backend.Select
// must never hand the twin's turn loop the metered one under default config,
// and RunTurn must never call it either.
func TestGuard_MeteredLeak(t *testing.T) {
	metered := backend.NewFake("fake-api")
	metered.Avail = backend.Availability{Installed: true, Authed: true, Metered: true, Detail: "fake metered"}
	sub := backend.NewFake("fake-sub")
	sub.Reply = func(req backend.Request) string { return "ok" }

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

	m, err := twins.Parse([]byte("id: t\nname: T\nusage: {window: 1h, model_calls: 5}\n"))
	if err != nil {
		t.Fatal(err)
	}
	env := runtime.Env{Manifest: m, Backend: sel.Backend}
	var deltas []string
	runtime.RunTurn(context.Background(), env, runtime.Turn{Channel: runtime.ChannelCLI, Prompt: "hello"}, func(e runtime.Event) {
		if e.Kind == runtime.EventDelta {
			deltas = append(deltas, e.Text)
		}
	})
	if metered.Calls() != 0 {
		t.Fatalf("metered backend received %d calls; want 0", metered.Calls())
	}
	if sub.Calls() != 1 {
		t.Fatalf("subscription backend received %d calls; want 1", sub.Calls())
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
