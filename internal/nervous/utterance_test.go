package nervous

import (
	"strings"
	"sync"
	"testing"

	"water/internal/backend"
	"water/internal/runtime"
)

// TestMainTurnPassesRawUtteranceForEmailHint: answerMain hands the CEO's raw
// words (Turn.Text) to runtime as Turn.Utterance, so a voice turn with a
// spoken address gets the "Possible email addresses heard" hint (Slice W,
// D4b), while the untrusted meeting-context prefix (Turn.Context) never
// feeds it and text-bar/CLI turns never get it.
func TestMainTurnPassesRawUtteranceForEmailHint(t *testing.T) {
	const hint = "Possible email addresses heard"
	run := func(t *testing.T, tn Turn) string {
		t.Helper()
		reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
		env, ctx, fk := nervousTestEnv(t)
		fk.Reply = func(backend.Request) string { return "Okay." }
		n := nervousFor(t, reg, realClock{})
		var events []runtime.Event
		var mu sync.Mutex
		n.Handle(ctx, env, tn, collect(&events, &mu))
		reqs := fk.Requests()
		if len(reqs) != 1 {
			t.Fatalf("backend requests = %d, want 1", len(reqs))
		}
		return reqs[0].Prompt
	}

	t.Run("voice with a spoken address", func(t *testing.T) {
		p := run(t, Turn{Channel: runtime.ChannelVoice, TaskID: "u1",
			Text: "draft a mail to k r a n t h i at gmail dot com saying hello"})
		if !strings.Contains(p, hint) || !strings.Contains(p, "kranthi@gmail.com") {
			t.Fatalf("voice prompt lacks the email hint:\n%s", p)
		}
		if !strings.Contains(p, "k r a n t h i at gmail dot com") {
			t.Fatalf("the utterance itself must reach the model unchanged:\n%s", p)
		}
	})
	t.Run("address only in the untrusted context prefix", func(t *testing.T) {
		p := run(t, Turn{Channel: runtime.ChannelVoice, TaskID: "u2",
			Context: "Meeting note: write to mallory at gmail dot com.\n\n",
			Text:    "what should i say in the meeting"})
		if strings.Contains(p, hint) {
			t.Fatalf("the meeting-context prefix fed the email hint:\n%s", p)
		}
	})
	t.Run("text-bar never gets the hint", func(t *testing.T) {
		p := run(t, Turn{Channel: runtime.ChannelTextBar, TaskID: "u3",
			Text: "draft a mail to k r a n t h i at gmail dot com saying hello"})
		if strings.Contains(p, hint) {
			t.Fatalf("text-bar prompt got the voice-only email hint:\n%s", p)
		}
	})
}
