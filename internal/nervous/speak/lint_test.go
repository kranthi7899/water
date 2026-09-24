package speak

import (
	"testing"

	"water/internal/nervous/render"
)

func testVoiceStyle() render.VoiceStyle {
	return render.VoiceStyle{
		BannedPhrases: []string{"as an ai", "great question"},
	}
}

func TestLintBannedPhrase(t *testing.T) {
	warnings := Lint("As an AI, I can help with that.", testVoiceStyle(), 280)
	if !containsWarning(warnings, "banned:as an ai") {
		t.Errorf("warnings = %v, want a banned:as an ai warning", warnings)
	}
}

func TestLintMarkdown(t *testing.T) {
	warnings := Lint("# Heading\n- a bullet", testVoiceStyle(), 280)
	if !containsWarning(warnings, "markdown") {
		t.Errorf("warnings = %v, want markdown", warnings)
	}
}

func TestLintEmoji(t *testing.T) {
	warnings := Lint("nice work \U0001F389", testVoiceStyle(), 280)
	if !containsWarning(warnings, "emoji") {
		t.Errorf("warnings = %v, want emoji", warnings)
	}
}

func TestLintURL(t *testing.T) {
	warnings := Lint("see https://example.com for more", testVoiceStyle(), 280)
	if !containsWarning(warnings, "url") {
		t.Errorf("warnings = %v, want url", warnings)
	}
}

func TestLintOverlength(t *testing.T) {
	long := ""
	for i := 0; i < 500; i++ {
		long += "x"
	}
	warnings := Lint(long, testVoiceStyle(), 280)
	if !containsWarning(warnings, "overlength") {
		t.Errorf("warnings = %v, want overlength (len=%d, cap=280*1.5=420)", warnings, len(long))
	}
}

func TestLintNotOverlengthUnderThreshold(t *testing.T) {
	short := "a short reply that fits comfortably."
	warnings := Lint(short, testVoiceStyle(), 280)
	if containsWarning(warnings, "overlength") {
		t.Errorf("warnings = %v, did not expect overlength for a short reply", warnings)
	}
}

func TestLintListOvercap(t *testing.T) {
	in := "- one\n- two\n- three\n- four\n- five"
	warnings := Lint(in, testVoiceStyle(), 280)
	if !containsWarning(warnings, "list_overcap") {
		t.Errorf("warnings = %v, want list_overcap", warnings)
	}
}

func TestLintClean(t *testing.T) {
	warnings := Lint("Your next event is the board sync at 3 PM.", testVoiceStyle(), 280)
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none for clean text", warnings)
	}
}

func TestLintNeverRewrites(t *testing.T) {
	raw := "# As an AI \U0001F389 see https://example.com"
	before := raw
	_ = Lint(raw, testVoiceStyle(), 280)
	if raw != before {
		t.Fatalf("Lint mutated its input string (impossible for a Go string, but guards against a future signature change): got %q, want %q", raw, before)
	}
}

func containsWarning(warnings []string, want string) bool {
	for _, w := range warnings {
		if w == want {
			return true
		}
	}
	return false
}
