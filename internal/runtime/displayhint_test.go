package runtime

import (
	"strings"
	"testing"
)

// TestVoiceHintOffersDisplayShow: only the voice channel hint tells the
// model it may call display.show to put something on screen, while still
// answering briefly out loud; no other channel mentions it.
func TestVoiceHintOffersDisplayShow(t *testing.T) {
	voice := ChannelHint(ChannelVoice, 280)
	if !strings.Contains(voice, "display.show") || !strings.Contains(voice, "out loud") {
		t.Fatalf("voice hint does not offer display.show:\n%s", voice)
	}
	if !strings.HasPrefix(voice, "## Channel: voice (reply in at most about 280 characters; short spoken sentences, no lists or markdown)") {
		t.Fatalf("voice hint lost its existing line:\n%s", voice)
	}
	for _, ch := range []Channel{ChannelTextBar, ChannelCLI, ""} {
		if h := ChannelHint(ch, 600); strings.Contains(h, "display") {
			t.Fatalf("%q hint mentions display: %s", ch, h)
		}
	}
	env, _ := testEnv(t)
	if p := TurnPrompt(env, ChannelVoice, "(state)", "x"); !strings.Contains(p, "display.show") {
		t.Fatalf("voice turn prompt lacks the display hint:\n%s", p)
	}
	if p := TurnPrompt(env, ChannelTextBar, "(state)", "x"); strings.Contains(p, "display.show") {
		t.Fatalf("text-bar turn prompt carries the display hint:\n%s", p)
	}
}
