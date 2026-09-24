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

func TestRunTurnFastPathMakesNoProviderCall(t *testing.T) {
	env, _ := testEnv(t)
	env.Manifest = testManifest(t)
	fake := backend.NewFake("fake")
	env.Backend = fake

	var events []Event
	RunTurn(context.Background(), env, Turn{Channel: ChannelCLI, Prompt: "any pending approvals"}, func(e Event) {
		events = append(events, e)
	})
	if fake.Calls() != 0 {
		t.Fatalf("fast path made %d provider calls, want 0", fake.Calls())
	}
	if len(events) < 2 || events[0].Kind != EventAck || events[len(events)-1].Kind != EventDone {
		t.Fatalf("events = %+v", events)
	}
}

func TestRunTurnStreamsDeltasBeforeDone(t *testing.T) {
	env, _ := testEnv(t)
	env.Manifest = testManifest(t)
	fake := backend.NewFake("fake")
	fake.Reply = func(req backend.Request) string { return "hello there friend" }
	env.Backend = fake

	var events []Event
	doneSeen := false
	RunTurn(context.Background(), env, Turn{Channel: ChannelCLI, Prompt: "tell me a joke"}, func(e Event) {
		if doneSeen {
			t.Fatalf("event %+v arrived after done", e)
		}
		if e.Kind == EventDone {
			doneSeen = true
		}
		events = append(events, e)
	})
	if !doneSeen {
		t.Fatal("no done event")
	}
	var deltas int
	for _, e := range events {
		if e.Kind == EventDelta {
			deltas++
		}
	}
	if deltas == 0 {
		t.Fatal("expected at least one delta before done")
	}
	if events[0].Kind != EventAck {
		t.Fatalf("first event = %+v, want ack", events[0])
	}
}

func TestRunTurnVoiceChannelEmitsSentences(t *testing.T) {
	env, _ := testEnv(t)
	env.Manifest = testManifest(t)
	fake := backend.NewFake("fake")
	fake.Reply = func(req backend.Request) string { return "First sentence. Second sentence." }
	env.Backend = fake

	var sentences []string
	RunTurn(context.Background(), env, Turn{Channel: ChannelVoice, Prompt: "status update"}, func(e Event) {
		if e.Kind == EventSentence {
			sentences = append(sentences, e.Text)
		}
	})
	if len(sentences) == 0 {
		t.Fatal("expected sentence events on the voice channel")
	}
}

func TestRunTurnReportsBackendError(t *testing.T) {
	env, _ := testEnv(t)
	env.Manifest = testManifest(t)
	fake := backend.NewFake("fake")
	fake.FailWith = context.DeadlineExceeded
	env.Backend = fake

	var gotErr bool
	RunTurn(context.Background(), env, Turn{Channel: ChannelCLI, Prompt: "do something"}, func(e Event) {
		if e.Kind == EventError {
			gotErr = true
		}
	})
	if !gotErr {
		t.Fatal("expected an error event")
	}
}
