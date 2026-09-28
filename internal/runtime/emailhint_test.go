package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"water/internal/approvals"
	"water/internal/backend"
)

// Spoken-address fixtures (docs/slices/W.md §7 acceptance: the hint on 4/4
// of these, 0/10 on ordinary voice utterances).
var spokenAddressFixtures = map[string]string{
	"draft a mail to kranti get a job at the right gmail dot com":    "krantigetajob@gmail.com",
	"The correct email address is Kranthetjob at the rightgmail.com": "kranthetjob@gmail.com",
	"draft a mail to k r a n t h i at gmail dot com":                 "kranthi@gmail.com",
	"draft a mail to john underscore doe at outlook dot com":         "john_doe@outlook.com",
}

var ordinaryVoiceUtterances = []string{
	"what's on my calendar tomorrow",
	"meet at the office at 5",
	"how's the weather in Dublin today",
	"what's the latest in AI this week",
	"send it",
	"I approve the message",
	"move my 3pm to 4",
	"what's our cash position",
	"tell me a quick joke",
	"we are at the right place, dot the i's",
}

func TestEmailHintOnlyOnVoiceWithACandidate(t *testing.T) {
	for utt, want := range spokenAddressFixtures {
		h := EmailHint(ChannelVoice, utt)
		if !strings.HasPrefix(h, "## Possible email addresses heard (from speech, unverified; spell back and confirm before use): ") ||
			!strings.Contains(h, want) {
			t.Errorf("EmailHint(voice, %q) = %q, want it to list %q", utt, h, want)
		}
		if strings.Contains(h, "therightgmail") {
			t.Errorf("hint for %q invents the glued domain: %q", utt, h)
		}
		for _, ch := range []Channel{ChannelTextBar, ChannelCLI, ""} {
			if h := EmailHint(ch, utt); h != "" {
				t.Errorf("EmailHint(%q, %q) = %q, want none off voice", ch, utt, h)
			}
		}
	}
	for _, utt := range ordinaryVoiceUtterances {
		if h := EmailHint(ChannelVoice, utt); h != "" {
			t.Errorf("EmailHint(voice, %q) = %q, want none", utt, h)
		}
	}
}

// The hint rides in the turn's own message, after the channel hint and
// before the CEO's words, which are sent exactly as heard; the system
// prompt is byte-identical with and without it (the warm session keeps its
// process).
func TestModelTurnAddsEmailHintWithoutRewriting(t *testing.T) {
	env, ctx := testEnv(t)
	fb := env.Backend.(*backend.Fake)
	fb.Reply = func(backend.Request) string { return "Let me spell that back." }
	utt := "The correct email address is Kranthetjob at the rightgmail.com."
	if _, err := ModelTurn(ctx, env, Turn{Channel: ChannelVoice, Prompt: "(context)\n" + utt, Utterance: utt}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := ModelTurn(ctx, env, Turn{Channel: ChannelVoice, Prompt: "hi", Utterance: "hi"}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := ModelTurn(ctx, env, Turn{Channel: ChannelTextBar, Prompt: utt, Utterance: utt}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	reqs := fb.Requests()
	if len(reqs) != 3 {
		t.Fatalf("requests = %d", len(reqs))
	}
	p := reqs[0].Prompt
	hint := strings.Index(p, "## Possible email addresses heard")
	if hint < 0 || !strings.Contains(p, "kranthetjob@gmail.com") {
		t.Fatalf("voice prompt lacks the hint:\n%s", p)
	}
	if ch := strings.Index(p, "## Channel: voice"); ch < 0 || ch > hint || hint > strings.Index(p, "## CEO") {
		t.Fatalf("hint out of place:\n%s", p)
	}
	if !strings.HasSuffix(p, "## CEO\n(context)\n"+utt) {
		t.Fatalf("the CEO's words were changed:\n%s", p)
	}
	if strings.Contains(reqs[1].Prompt, "Possible email") || strings.Contains(reqs[2].Prompt, "Possible email") {
		t.Fatal("hint on a turn with no address, or off voice")
	}
	if reqs[0].System != reqs[1].System || reqs[0].System != reqs[2].System {
		t.Fatal("system prompt changed with the hint; the warm session would restart")
	}
	// TurnPrompt (no utterance) is unchanged for its existing callers.
	if strings.Contains(TurnPrompt(env, ChannelVoice, "", utt), "Possible email") {
		t.Fatal("TurnPrompt without an utterance must not add the hint")
	}
}

func TestApprovalRequiredEventCarriesWarnings(t *testing.T) {
	env := approvals.Envelope{ID: "env_1", Action: "gmail.send_message", Risk: "high", PayloadHash: "h",
		Payload:  map[string]any{"to": []any{"x@no-mail.io"}, "subject": "s", "body": "b"},
		Warnings: []string{"no-mail.io has no mail server; the message would bounce"}}
	e := ApprovalRequiredEvent(env)
	if len(e.Warnings) != 1 || e.Warnings[0] != env.Warnings[0] || e.ConfirmPhrase != "" {
		t.Fatalf("event = %+v", e)
	}
	if !strings.Contains(e.ReadBack, "Warning: no-mail.io has no mail server") {
		t.Fatalf("read_back lacks the warning: %q", e.ReadBack)
	}
	b, _ := json.Marshal(e)
	if !strings.Contains(string(b), `"warnings":["no-mail.io has no mail server; the message would bounce"]`) {
		t.Fatalf("wire: %s", b)
	}
	// Without warnings the wire shape is unchanged.
	env.Warnings = nil
	b, _ = json.Marshal(ApprovalRequiredEvent(env))
	if strings.Contains(string(b), "warnings") || strings.Contains(string(b), "confirm_phrase") {
		t.Fatalf("empty fields not omitted: %s", b)
	}
	b, _ = json.Marshal(Event{Kind: EventApprovalRequired, ConfirmPhrase: "confirm send"})
	if !strings.Contains(string(b), `"confirm_phrase":"confirm send"`) {
		t.Fatalf("confirm_phrase wire: %s", b)
	}
}
