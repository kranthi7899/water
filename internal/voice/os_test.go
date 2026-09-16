package voice

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
)

func TestOSDegradesWithClearMessage(t *testing.T) {
	o := &OS{Look: func(string) (string, error) { return "", errors.New("nope") }}
	o.detect()
	if o.Available() {
		t.Fatal("no binary should mean unavailable")
	}
	err := o.Speak(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "unavailable") && !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("absence message unclear: %v", err)
	}
	if _, err := o.Listen(context.Background()); !errors.Is(err, ErrListenUnavailable) {
		t.Fatal("listen must be a documented no-op")
	}
	if runtime.GOOS == "darwin" {
		real := NewOS()
		if !real.Available() {
			t.Skip("say not found")
		}
		if real.Name() != "os" {
			t.Fatal("name")
		}
	}
}

func TestOSRolesResolveDistinctInstalledVoices(t *testing.T) {
	installed := []string{"Daniel", "Samantha", "Rishi", "Moira", "Ava (Premium)"}
	got := map[string]string{}
	for _, r := range []string{"ceo", "coo", "cto", "design"} {
		o := &OS{bin: "/usr/bin/say"}
		o.voice = pickVoice("", OSProfileFor(r).Voices, installed)
		got[r] = o.voice
	}
	want := map[string]string{"ceo": "Daniel", "coo": "Ava (Premium)", "cto": "Rishi", "design": "Moira"}
	for r, v := range want {
		if got[r] != v {
			t.Fatalf("%s voice = %q, want %q (all: %v)", r, got[r], v, got)
		}
	}
	if v := pickVoice("samantha", OSProfileFor("ceo").Voices, installed); v != "Samantha" {
		t.Fatalf("installed override ignored: %q", v)
	}
	if v := pickVoice("marin", OSProfileFor("ceo").Voices, installed); v != "Daniel" {
		t.Fatalf("uninstalled override must fall back to role default, got %q", v)
	}
	if v := pickVoice("", OSProfileFor("ceo").Voices, nil); v != "" {
		t.Fatalf("no installed voices must mean system default, got %q", v)
	}
}

func TestOSVoiceArgs(t *testing.T) {
	o := &OS{bin: "/usr/bin/say", voice: "Moira", rate: 180}
	if got := strings.Join(o.voiceArgs(), " "); got != "-v Moira -r 180" {
		t.Fatalf("say args = %q", got)
	}
	o = &OS{bin: "/usr/bin/espeak-ng", voice: "en+f4", rate: 180}
	if got := strings.Join(o.voiceArgs(), " "); got != "-v en+f4 -s 180" {
		t.Fatalf("espeak args = %q", got)
	}
	if got := (&OS{bin: "/usr/bin/spd-say"}).voiceArgs(); len(got) != 0 {
		t.Fatalf("spd-say args = %v", got)
	}
}

func TestParseSayVoices(t *testing.T) {
	out := "Albert              en_US    # Hello! My name is Albert.\n" +
		"Eddy (English (UK)) en_GB    # Hello! My name is Eddy.\n" +
		"Ava (Premium)       en_US    # Hello! My name is Ava.\n"
	got := strings.Join(parseSayVoices(out), "|")
	if got != "Albert|Eddy (English (UK))|Ava (Premium)" {
		t.Fatalf("parsed = %q", got)
	}
}

func TestSpeakableDropsMarkup(t *testing.T) {
	in := "## Decision\n\n**Ship it** with `flag`.\n\n- first step\n- see [docs](https://x.y)\n\n```go\nfmt.Println()\n```\n\n| Risk | Owner |\n|---|---|\n| data loss | CTO |"
	got := Speakable(in)
	for _, bad := range []string{"#", "*", "`", "|", "https", "Println", "---"} {
		if strings.Contains(got, bad) {
			t.Fatalf("speakable kept %q:\n%s", bad, got)
		}
	}
	for _, want := range []string{"Decision.", "Ship it with flag.", "first step.", "see docs.", "code block omitted", "data loss, CTO."} {
		if !strings.Contains(got, want) {
			t.Fatalf("speakable lost %q:\n%s", want, got)
		}
	}
}
