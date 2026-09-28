package runtime

import (
	"context"
	"testing"

	"water/internal/backend"
	"water/internal/twins"
)

func testManifest(t *testing.T) *twins.Manifest {
	t.Helper()
	m, err := twins.Parse([]byte("id: t\nname: T\nusage: {window: 1h, model_calls: 50}\n"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestRunTurnCompatWrapperStillWorks: internal/gateway's handleTurn still
// calls RunTurn directly until task R-15 rewires it to internal/nervous's
// Handle (which tries Tier 0 first and falls back to ModelTurn, the same
// primitive RunTurn now wraps). This is the one test left at the RunTurn
// level, proving the transitional wrapper still reproduces the old
// ack+delta+done event shape with no fast path in front of it.
func TestRunTurnCompatWrapperStillWorks(t *testing.T) {
	env, _ := testEnv(t)
	env.Manifest = testManifest(t)
	fake := backend.NewFake("fake")
	fake.Reply = func(req backend.Request) string { return "hello there friend" }
	env.Backend = fake

	var events []Event
	RunTurn(context.Background(), env, Turn{Channel: ChannelCLI, Prompt: "tell me a joke"}, func(e Event) {
		events = append(events, e)
	})
	if fake.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1 (RunTurn has no fast path of its own anymore)", fake.Calls())
	}
	if len(events) < 3 || events[0].Kind != EventAck || events[len(events)-1].Kind != EventDone {
		t.Fatalf("events = %+v, want ack ... done", events)
	}
}

func TestModelTurnStreamsDeltas(t *testing.T) {
	env, _ := testEnv(t)
	env.Manifest = testManifest(t)
	fake := backend.NewFake("fake")
	fake.Reply = func(req backend.Request) string { return "hello there friend" }
	env.Backend = fake

	var events []Event
	resp, err := ModelTurn(context.Background(), env, Turn{Channel: ChannelCLI, Prompt: "tell me a joke"}, func(e Event) {
		events = append(events, e)
	})
	if err != nil {
		t.Fatalf("ModelTurn error: %v", err)
	}
	if resp.Text == "" {
		t.Fatal("empty response text")
	}
	var deltas int
	for _, e := range events {
		if e.Kind == EventDelta {
			deltas++
		}
		if e.Kind == EventAck || e.Kind == EventDone {
			t.Fatalf("ModelTurn must not emit ack/done itself, got %+v", e)
		}
	}
	if deltas == 0 {
		t.Fatal("expected at least one delta")
	}
}

func TestModelTurnVoiceChannelEmitsSentences(t *testing.T) {
	env, _ := testEnv(t)
	env.Manifest = testManifest(t)
	fake := backend.NewFake("fake")
	fake.Reply = func(req backend.Request) string { return "First sentence. Second sentence." }
	env.Backend = fake

	var sentences []string
	if _, err := ModelTurn(context.Background(), env, Turn{Channel: ChannelVoice, Prompt: "status update"}, func(e Event) {
		if e.Kind == EventSentence {
			sentences = append(sentences, e.Text)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(sentences) == 0 {
		t.Fatal("expected sentence events on the voice channel")
	}
}

func TestModelTurnReportsBackendError(t *testing.T) {
	env, _ := testEnv(t)
	env.Manifest = testManifest(t)
	fake := backend.NewFake("fake")
	fake.FailWith = context.DeadlineExceeded
	env.Backend = fake

	_, err := ModelTurn(context.Background(), env, Turn{Channel: ChannelCLI, Prompt: "do something"}, func(Event) {})
	if err == nil {
		t.Fatal("expected an error")
	}
}
