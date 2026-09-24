package cli

import (
	"errors"
	"strings"
	"testing"

	"water/internal/config"
	"water/internal/voice"
)

// TestAskVoiceHonorsConfiguredProvider: `water ask --voice` must go through
// the configured voice provider, not a hard-wired bare OS one. With
// voice.provider=noop the flag is refused (ExitUsage) rather than speaking
// through `say` anyway.
func TestAskVoiceHonorsConfiguredProvider(t *testing.T) {
	t.Setenv("WATER_HOME", t.TempDir())
	if err := config.Save(map[string]string{"voice.provider": "noop"}); err != nil {
		t.Fatal(err)
	}
	a := NewApp()
	root := a.rootCmd()
	root.SetArgs([]string{"ask", "--voice", "hi"})
	err := root.Execute()
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != ExitUsage || !strings.Contains(err.Error(), "noop") {
		t.Fatalf("ask --voice with voice.provider=noop: err = %v, want ExitUsage naming the noop provider", err)
	}
}

// TestVoiceProviderAppliesCEOProfile: the configured "os" provider carries the
// CEO profile (rate, and the ceo_voice override when installed) instead of
// being a bare system-default NewOS().
func TestVoiceProviderAppliesCEOProfile(t *testing.T) {
	t.Setenv("WATER_HOME", t.TempDir())
	if err := config.Save(map[string]string{"voice.provider": "os", "voice.ceo_voice": "Samantha"}); err != nil {
		t.Fatal(err)
	}
	a := NewApp()
	cfg, err := a.config()
	if err != nil {
		t.Fatal(err)
	}
	vp, err := a.voiceProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	o, ok := vp.(*voice.OS)
	if !ok {
		t.Fatalf("provider = %T, want *voice.OS", vp)
	}
	if o.Rate() != voice.CEOOSProfile().Rate {
		t.Fatalf("rate = %d, want the CEO profile rate %d", o.Rate(), voice.CEOOSProfile().Rate)
	}
	if want := voice.NewOSVoice("Samantha").Voice(); o.Voice() != want {
		t.Fatalf("voice = %q, want %q", o.Voice(), want)
	}
}

func TestVoiceProviderUnknownListsAllValidNames(t *testing.T) {
	t.Setenv("WATER_HOME", t.TempDir())
	if err := config.Save(map[string]string{"voice.provider": "opneai"}); err != nil {
		t.Fatal(err)
	}
	a := NewApp()
	cfg, err := a.config()
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.voiceProvider(cfg)
	if err == nil {
		t.Fatal("unknown provider should fail")
	}
	for _, n := range []string{"openai", "os", "noop"} {
		if !strings.Contains(err.Error(), n) {
			t.Fatalf("error %q does not list valid provider %q", err, n)
		}
	}
}
