package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"water/internal/approvals"
	"water/internal/backend"
)

// TestTurnPromptVoiceHintOnlyOnVoice (V-events, D5): the per-turn channel
// hint tells the model which channel it is on and that channel's style.yaml
// character budget. Only voice asks for spoken sentences with no lists or
// markdown; the other channels get their length budget alone.
func TestTurnPromptVoiceHintOnlyOnVoice(t *testing.T) {
	env, _ := testEnv(t)
	env.MaxChars = map[Channel]int{ChannelVoice: 280, ChannelTextBar: 600, ChannelCLI: 2000}

	voice := TurnPrompt(env, ChannelVoice, "(state)", "what's up")
	if !strings.Contains(voice, "## Channel: voice") || !strings.Contains(voice, "280 characters") ||
		!strings.Contains(voice, "no lists or markdown") {
		t.Fatalf("voice prompt missing the voice hint:\n%s", voice)
	}
	for _, ch := range []Channel{ChannelTextBar, ChannelCLI} {
		p := TurnPrompt(env, ch, "(state)", "what's up")
		if strings.Contains(p, "no lists or markdown") || strings.Contains(p, "spoken") {
			t.Fatalf("%s prompt carries the voice hint:\n%s", ch, p)
		}
		if !strings.Contains(p, "## Channel: "+string(ch)) {
			t.Fatalf("%s prompt has no channel line:\n%s", ch, p)
		}
	}
	if !strings.Contains(TurnPrompt(env, ChannelTextBar, "", "x"), "600 characters") {
		t.Fatal("text-bar hint does not carry max_chars.text-bar")
	}
	// The CEO's request stays last, after the hint.
	if !strings.HasSuffix(voice, "## CEO\nwhat's up") {
		t.Fatalf("voice prompt does not end with the request:\n%s", voice)
	}
	// No cap known: the voice hint still asks for brevity, with no number.
	env.MaxChars = nil
	bare := TurnPrompt(env, ChannelVoice, "", "x")
	if !strings.Contains(bare, "no lists or markdown") || strings.Contains(bare, " 0 characters") {
		t.Fatalf("voice prompt with no cap:\n%s", bare)
	}
}

// TestModelTurnSendsStyleBlockAndChannelHint: the request ModelTurn hands
// the backend carries the style block in its system prompt (byte-identical
// across channels, so the warm session keeps its process) and the channel
// hint in the turn's own message.
func TestModelTurnSendsStyleBlockAndChannelHint(t *testing.T) {
	env, ctx := testEnv(t)
	env.StyleBlock = "Be direct. Lead with the answer."
	env.MaxChars = map[Channel]int{ChannelVoice: 280, ChannelTextBar: 600, ChannelCLI: 2000}
	fb := env.Backend.(*backend.Fake)
	fb.Reply = func(backend.Request) string { return "Fine." }

	for _, ch := range []Channel{ChannelVoice, ChannelTextBar} {
		if _, err := ModelTurn(ctx, env, Turn{Channel: ch, Prompt: "hi"}, func(Event) {}); err != nil {
			t.Fatal(err)
		}
	}
	reqs := fb.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	if !strings.Contains(reqs[0].System, env.StyleBlock) {
		t.Fatalf("system prompt lacks the style block:\n%s", reqs[0].System)
	}
	if reqs[0].System != reqs[1].System {
		t.Fatal("system prompt differs between channels; the warm session would restart")
	}
	if !strings.Contains(reqs[0].Prompt, "## Channel: voice") || strings.Contains(reqs[1].Prompt, "## Channel: voice") {
		t.Fatalf("channel hints wrong: voice=%q text-bar=%q", reqs[0].Prompt, reqs[1].Prompt)
	}
}

// TestApprovalRequiredEventCarriesEverything: the one constructor every
// approval_required path uses fills the fields a client needs to show and
// decide the envelope, with the read-back built by approvals.ReadBack.
func TestApprovalRequiredEventCarriesEverything(t *testing.T) {
	env := approvals.Envelope{ID: "env_1", Action: "gcal.create_event", Risk: "medium", PayloadHash: "sha256:ab",
		Payload: map[string]any{"title": "Sync"}}
	e := ApprovalRequiredEvent(env)
	if e.Kind != EventApprovalRequired || e.ApprovalID != "env_1" || e.Action != env.Action || e.Text != env.Action ||
		e.Risk != "medium" || e.PayloadHash != "sha256:ab" || e.ReadBack != approvals.ReadBack(env) || e.ReadBack == "" {
		t.Fatalf("event = %+v", e)
	}
}

// TestStepEventWireShape pins the tool_start/tool_end JSON keys, and that
// the new fields are omitted from every other kind, so an older client's
// events are byte-identical to before.
func TestStepEventWireShape(t *testing.T) {
	b, _ := json.Marshal(Event{Kind: EventToolEnd, StepID: "stp_1", Tool: "linear.list_issues", Label: "Checking Linear", Status: StepOK})
	if got, want := string(b), `{"kind":"tool_end","step_id":"stp_1","tool":"linear.list_issues","label":"Checking Linear","status":"ok"}`; got != want {
		t.Fatalf("tool_end = %s, want %s", got, want)
	}
	b, _ = json.Marshal(Event{Kind: EventDelta, Text: "x"})
	if got := string(b); got != `{"kind":"delta","text":"x"}` {
		t.Fatalf("delta = %s", got)
	}
}
