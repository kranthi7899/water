package render

import (
	"strings"
	"testing"
	"testing/fstest"

	"water"
)

const validStyleYAML = `
tone: "Calm, brief, plain."
max_chars: {cli: 2000, text-bar: 600, voice: 280}
max_list_items: {cli: 20, text-bar: 8, voice: 3}
confirmation: "Got it."
prompt_block: "Be brief."
responses:
  schedule.on_date:
    header: "Your calendar {when_spoken}:"
    item: "{time} {title}"
    empty: "Nothing on your calendar {when_spoken}."
    more: "and {n} more"
voice:
  name: Water
  tone: "Calm, brief, plain."
  handoff: ["One moment."]
  errors:
    generic: "I can't answer that right now."
    timeout: "That's taking too long."
    tap_required: "That needs a tap."
    readback_stale: "That changed."
    nothing_pending: "Nothing pending."
  banned_phrases: ["as an ai", "certainly!"]
  max_sentence_chars: 200
  tts: {voice: "", rate_wpm: 185}
`

func mustStyle(t *testing.T, y string) *Style {
	t.Helper()
	s, err := parseStyle([]byte(y))
	if err != nil {
		t.Fatalf("parseStyle: %v", err)
	}
	return s
}

func TestMaxCharsAndMaxListItemsPerChannel(t *testing.T) {
	s := mustStyle(t, validStyleYAML)
	cases := []struct {
		ch            string
		wantChars     int
		wantListItems int
	}{
		{"cli", 2000, 20},
		{"text-bar", 600, 8},
		{"voice", 280, 3},
	}
	for _, c := range cases {
		if got := s.MaxChars(c.ch); got != c.wantChars {
			t.Errorf("MaxChars(%q) = %d, want %d", c.ch, got, c.wantChars)
		}
		if got := s.MaxListItems(c.ch); got != c.wantListItems {
			t.Errorf("MaxListItems(%q) = %d, want %d", c.ch, got, c.wantListItems)
		}
	}
	if got := s.MaxChars("unknown"); got != 0 {
		t.Errorf("MaxChars(unknown) = %d, want 0", got)
	}
}

func TestRenderTruncatesListWithAndNMore(t *testing.T) {
	s := mustStyle(t, validStyleYAML)
	items := []Item{
		{"time": "9:00", "title": "Standup"},
		{"time": "10:00", "title": "Board sync"},
		{"time": "11:00", "title": "1:1"},
		{"time": "14:00", "title": "Review"},
		{"time": "16:00", "title": "Retro"},
	}
	out := s.Render(Result{
		Intent: "schedule.on_date",
		Facts:  map[string]string{"when_spoken": "Today"},
		Items:  items,
	}, "voice") // voice caps max_list_items at 3
	if strings.Count(out, "9:00 Standup") != 1 {
		t.Fatalf("expected first item rendered, got: %q", out)
	}
	if strings.Contains(out, "16:00 Retro") {
		t.Fatalf("expected 5th item truncated, got: %q", out)
	}
	if !strings.Contains(out, "and 2 more") {
		t.Fatalf("expected 'and 2 more', got: %q", out)
	}
}

func TestRenderClarification(t *testing.T) {
	s := mustStyle(t, validStyleYAML)
	out := s.Render(Result{
		NeedsClarification: &Clarification{
			Question: "Which Alex did you mean?",
			Options:  []string{"Alex Chen", "Alex Rivera"},
		},
	}, "cli")
	want := "Which Alex did you mean?\n1. Alex Chen\n2. Alex Rivera"
	if out != want {
		t.Fatalf("Render clarification = %q, want %q", out, want)
	}
}

func TestRenderWarnings(t *testing.T) {
	s := mustStyle(t, validStyleYAML)
	out := s.Render(Result{
		Intent:   "schedule.on_date",
		Facts:    map[string]string{"when_spoken": "Today"},
		Items:    []Item{{"time": "9:00", "title": "Standup"}},
		Warnings: []string{"voice_overlength"},
	}, "cli")
	if !strings.Contains(out, "(voice_overlength)") {
		t.Fatalf("expected warning surfaced, got: %q", out)
	}
}

func TestRenderEmptyUsesEmptyTemplate(t *testing.T) {
	s := mustStyle(t, validStyleYAML)
	out := s.Render(Result{
		Intent: "schedule.on_date",
		Facts:  map[string]string{"when_spoken": "tomorrow"},
		Items:  nil,
	}, "cli")
	want := "Your calendar tomorrow:\nNothing on your calendar tomorrow."
	if out != want {
		t.Fatalf("Render empty = %q, want %q", out, want)
	}
}

// TestRenderInertOnExternalBraces proves that a value being substituted in
// (an external event title, say) is never re-scanned for further {name}
// placeholders: substitution is one pass over the ORIGINAL template only.
func TestRenderInertOnExternalBraces(t *testing.T) {
	s := mustStyle(t, validStyleYAML)
	out := s.Render(Result{
		Intent: "schedule.on_date",
		Facts:  map[string]string{"when_spoken": "today"},
		Items: []Item{
			{"time": "9:00", "title": "Standup {sync}"},
		},
	}, "cli")
	if !strings.Contains(out, "9:00 Standup {sync}") {
		t.Fatalf("expected literal braces preserved, got: %q", out)
	}
}

func TestRenderFallsBackWithNoResponseEntry(t *testing.T) {
	s := mustStyle(t, validStyleYAML)
	out := s.Render(Result{
		Intent:         "mail.latest",
		Interpretation: "Your latest email",
		Items:          []Item{{"from": "alex", "subject": "Hi"}},
	}, "cli")
	if !strings.HasPrefix(out, "Your latest email\n- from: alex, subject: Hi") {
		t.Fatalf("unexpected fallback render: %q", out)
	}
}

func TestRejectsUnknownTopLevelKey(t *testing.T) {
	bad := validStyleYAML + "\nbogus_key: true\n"
	if _, err := parseStyle([]byte(bad)); err == nil {
		t.Fatal("expected error for unknown top-level key")
	}
}

func TestRejectsUnknownNestedKey(t *testing.T) {
	bad := strings.Replace(validStyleYAML, `tts: {voice: "", rate_wpm: 185}`, `tts: {voice: "", rate_wpm: 185, bogus: 1}`, 1)
	if _, err := parseStyle([]byte(bad)); err == nil {
		t.Fatal("expected error for unknown nested key under voice.tts")
	}
}

func TestRejectsMissingChannelInMaxChars(t *testing.T) {
	bad := strings.Replace(validStyleYAML, `max_chars: {cli: 2000, text-bar: 600, voice: 280}`, `max_chars: {cli: 2000, text-bar: 600}`, 1)
	if _, err := parseStyle([]byte(bad)); err == nil {
		t.Fatal("expected error for missing voice channel in max_chars")
	}
}

func TestRejectsMissingChannelInMaxListItems(t *testing.T) {
	bad := strings.Replace(validStyleYAML, `max_list_items: {cli: 20, text-bar: 8, voice: 3}`, `max_list_items: {text-bar: 8, voice: 3}`, 1)
	if _, err := parseStyle([]byte(bad)); err == nil {
		t.Fatal("expected error for missing cli channel in max_list_items")
	}
}

func TestRejectsMissingVoiceErrorKey(t *testing.T) {
	bad := strings.Replace(validStyleYAML, `nothing_pending: "Nothing pending."`, "", 1)
	if _, err := parseStyle([]byte(bad)); err == nil {
		t.Fatal("expected error for missing voice.errors.nothing_pending")
	}
}

func TestDefaultStyleIsValid(t *testing.T) {
	s := DefaultStyle()
	if s == nil {
		t.Fatal("DefaultStyle() returned nil")
	}
	if s.MaxChars("voice") != 280 {
		t.Errorf("DefaultStyle voice max_chars = %d, want 280", s.MaxChars("voice"))
	}
	if s.Voice().Name != "Water" {
		t.Errorf("DefaultStyle voice.name = %q, want Water", s.Voice().Name)
	}
	// DefaultStyle must be idempotent and go through the same validation
	// path as any real style.yaml: calling it twice returns a style built
	// from the exact same (already-validated) constant.
	if DefaultStyle() != s {
		t.Error("DefaultStyle() should return the same cached instance")
	}
}

func TestLoadStyleMissingFileReturnsDefault(t *testing.T) {
	fsys := fstest.MapFS{} // no twins/ceo/style.yaml at all
	s, err := LoadStyle(fsys, "ceo")
	if err != nil {
		t.Fatalf("LoadStyle with missing file: unexpected error %v", err)
	}
	if s != DefaultStyle() {
		t.Error("LoadStyle with missing file should return DefaultStyle()")
	}
}

func TestLoadStyleReadsRealFile(t *testing.T) {
	fsys := fstest.MapFS{
		"twins/ceo/style.yaml": {Data: []byte(validStyleYAML)},
	}
	s, err := LoadStyle(fsys, "ceo")
	if err != nil {
		t.Fatalf("LoadStyle: %v", err)
	}
	if s.MaxChars("voice") != 280 {
		t.Errorf("loaded style voice max_chars = %d, want 280", s.MaxChars("voice"))
	}
}

func TestLoadStyleRejectsInvalidFile(t *testing.T) {
	fsys := fstest.MapFS{
		"twins/ceo/style.yaml": {Data: []byte(validStyleYAML + "\nbogus_key: true\n")},
	}
	if _, err := LoadStyle(fsys, "ceo"); err == nil {
		t.Fatal("expected error loading an invalid style.yaml")
	}
}

func TestPromptBlockStableAndContainsToneAndBannedPhrases(t *testing.T) {
	s := mustStyle(t, validStyleYAML)
	b1 := s.PromptBlock()
	b2 := s.PromptBlock()
	if b1 != b2 {
		t.Fatalf("PromptBlock() not stable: %q vs %q", b1, b2)
	}
	if !strings.Contains(b1, "Calm, brief, plain.") {
		t.Errorf("PromptBlock() missing tone: %q", b1)
	}
	if !strings.Contains(b1, "as an ai") || !strings.Contains(b1, "certainly!") {
		t.Errorf("PromptBlock() missing banned phrases: %q", b1)
	}
}

func TestEmbeddedCEOStyleLoads(t *testing.T) {
	// Guards against the embedded twins/ceo/style.yaml drifting out of
	// this package's schema.
	s, err := LoadStyle(water.TwinsFS(), "ceo")
	if err != nil {
		t.Fatalf("LoadStyle(ceo): %v", err)
	}
	if s.Voice().Name != "Water" {
		t.Errorf("ceo style voice.name = %q, want Water", s.Voice().Name)
	}
}
