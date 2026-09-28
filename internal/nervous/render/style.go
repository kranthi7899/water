package render

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// channels is the fixed, exhaustive set of channels every style.yaml must
// declare max_chars/max_list_items for. It is not open-ended: a new
// channel needs a code change here, not just a new YAML key, since the
// facade branches on these same three strings.
var channels = []string{"cli", "text-bar", "voice"}

// requiredVoiceErrors are the error phrases later tasks (the facade, voice
// approval) assume always exist, so every style.yaml must supply them.
var requiredVoiceErrors = []string{"generic", "timeout", "tap_required", "readback_stale", "nothing_pending"}

// styleFile is the strict on-disk shape of twins/<id>/style.yaml.
type styleFile struct {
	Tone         string                   `yaml:"tone"`
	MaxChars     map[string]int           `yaml:"max_chars"`
	MaxListItems map[string]int           `yaml:"max_list_items"`
	Confirmation string                   `yaml:"confirmation"`
	PromptBlock  string                   `yaml:"prompt_block"`
	Responses    map[string]responseEntry `yaml:"responses"`
	Voice        voiceFile                `yaml:"voice"`
}

type responseEntry struct {
	Header string `yaml:"header"`
	Item   string `yaml:"item"`
	Empty  string `yaml:"empty"`
	More   string `yaml:"more"`
}

type voiceFile struct {
	Name             string            `yaml:"name"`
	Tone             string            `yaml:"tone"`
	Handoff          []string          `yaml:"handoff"`
	Errors           map[string]string `yaml:"errors"`
	BannedPhrases    []string          `yaml:"banned_phrases"`
	MaxSentenceChars int               `yaml:"max_sentence_chars"`
	TTS              ttsFile           `yaml:"tts"`
}

type ttsFile struct {
	Voice   string `yaml:"voice"`
	RateWPM int    `yaml:"rate_wpm"`
	// KokoroVoice optionally names the Kokoro-82M voice (FluidAudio's
	// KokoroAneManager, Slice V's V-voice sub-slice) to use when the client
	// has selected the Kokoro TTS engine. Empty means "let the client pick
	// its own default voice" — unlike Voice/RateWPM this has no OS-voice
	// fallback semantics of its own, so it is never required.
	KokoroVoice string `yaml:"kokoro_voice"`
}

// VoiceStyle is the parsed voice: section, handed to the facade and (via
// GET /v1/voice/profile, a later task) to clients for their TTS settings.
type VoiceStyle struct {
	Name             string
	Tone             string
	Handoff          []string
	Errors           map[string]string
	BannedPhrases    []string
	MaxSentenceChars int
	TTS              struct {
		Voice       string
		RateWPM     int
		KokoroVoice string
	}
}

// Style is a loaded, validated style.yaml. Every accessor is safe to call
// concurrently: a Style is immutable once built.
type Style struct {
	tone         string
	maxChars     map[string]int
	maxListItems map[string]int
	confirmation string
	promptBlock  string
	responses    map[string]responseEntry
	voice        VoiceStyle
}

// LoadStyle reads twins/<twinID>/style.yaml from fsys. A missing file is
// not an error: it returns DefaultStyle(), so a twin with no style.yaml
// (or a fresh checkout mid-slice) still renders sensibly.
func LoadStyle(fsys fs.FS, twinID string) (*Style, error) {
	b, err := fs.ReadFile(fsys, path.Join("twins", twinID, "style.yaml"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return DefaultStyle(), nil
		}
		return nil, fmt.Errorf("style: %w", err)
	}
	return parseStyle(b)
}

// parseStyle strictly decodes and validates style.yaml bytes. Unknown keys
// are errors, as in twins.Load and decisions.ParseType: a typo must fail,
// not be silently ignored.
func parseStyle(b []byte) (*Style, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var f styleFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("style: yaml: %w", err)
	}
	if err := validateStyleFile(f); err != nil {
		return nil, fmt.Errorf("style: %w", err)
	}
	return &Style{
		tone:         f.Tone,
		maxChars:     f.MaxChars,
		maxListItems: f.MaxListItems,
		confirmation: f.Confirmation,
		promptBlock:  f.PromptBlock,
		responses:    f.Responses,
		voice:        voiceFromFile(f.Voice),
	}, nil
}

func voiceFromFile(v voiceFile) VoiceStyle {
	out := VoiceStyle{
		Name:             v.Name,
		Tone:             v.Tone,
		Handoff:          v.Handoff,
		Errors:           v.Errors,
		BannedPhrases:    v.BannedPhrases,
		MaxSentenceChars: v.MaxSentenceChars,
	}
	out.TTS.Voice = v.TTS.Voice
	out.TTS.RateWPM = v.TTS.RateWPM
	out.TTS.KokoroVoice = v.TTS.KokoroVoice
	return out
}

// validateStyleFile enforces everything parseStyle relies on later tasks
// being able to assume without a nil/missing-key check at every call site.
//
// Known gap: a response entry's header/empty can't yet be checked for
// referencing every one of its intent's "{<slot>_spoken}" placeholders,
// because no intent registry exists yet in this slice (that lands in
// R-9/R-11). Only structural non-emptiness is checked here.
func validateStyleFile(f styleFile) error {
	if strings.TrimSpace(f.Tone) == "" {
		return errors.New("tone is required")
	}
	if strings.TrimSpace(f.PromptBlock) == "" {
		return errors.New("prompt_block is required")
	}
	if strings.TrimSpace(f.Confirmation) == "" {
		return errors.New("confirmation is required")
	}
	if err := requireExactChannels("max_chars", f.MaxChars); err != nil {
		return err
	}
	if err := requireExactChannels("max_list_items", f.MaxListItems); err != nil {
		return err
	}
	for ch, n := range f.MaxChars {
		if n <= 0 {
			return fmt.Errorf("max_chars.%s must be positive", ch)
		}
	}
	for ch, n := range f.MaxListItems {
		if n <= 0 {
			return fmt.Errorf("max_list_items.%s must be positive", ch)
		}
	}
	for id, entry := range f.Responses {
		if strings.TrimSpace(entry.Header) == "" {
			return fmt.Errorf("responses.%s: header is required", id)
		}
		if strings.TrimSpace(entry.Empty) == "" {
			return fmt.Errorf("responses.%s: empty is required", id)
		}
	}
	if strings.TrimSpace(f.Voice.Name) == "" {
		return errors.New("voice.name is required")
	}
	if strings.TrimSpace(f.Voice.Tone) == "" {
		return errors.New("voice.tone is required")
	}
	if len(f.Voice.Handoff) == 0 {
		return errors.New("voice.handoff must have at least one phrase")
	}
	for _, want := range requiredVoiceErrors {
		if strings.TrimSpace(f.Voice.Errors[want]) == "" {
			return fmt.Errorf("voice.errors.%s is required", want)
		}
	}
	if f.Voice.MaxSentenceChars <= 0 {
		return errors.New("voice.max_sentence_chars must be positive")
	}
	if f.Voice.TTS.RateWPM <= 0 {
		return errors.New("voice.tts.rate_wpm must be positive")
	}
	return nil
}

func requireExactChannels(field string, m map[string]int) error {
	if len(m) != len(channels) {
		return fmt.Errorf("%s must declare exactly %s", field, strings.Join(channels, ", "))
	}
	for _, ch := range channels {
		if _, ok := m[ch]; !ok {
			return fmt.Errorf("%s is missing channel %q", field, ch)
		}
	}
	return nil
}

// PromptBlock is appended to the main path's system prompt. It composes
// the raw prompt_block text with the tone and the banned-phrase list, so
// the model's own system prompt states the same voice contract the
// renderer enforces on quick answers. It is pure and deterministic: the
// same Style always produces the same block.
func (s *Style) PromptBlock() string {
	var b strings.Builder
	b.WriteString(s.promptBlock)
	b.WriteString("\n\nTone: ")
	b.WriteString(s.voice.Tone)
	if len(s.voice.BannedPhrases) > 0 {
		phrases := append([]string(nil), s.voice.BannedPhrases...)
		sort.Strings(phrases)
		b.WriteString("\nNever use these phrases: ")
		b.WriteString(strings.Join(phrases, ", "))
	}
	return b.String()
}

// Voice returns the twin's voice contract (tone, phrases, TTS profile).
func (s *Style) Voice() VoiceStyle { return s.voice }

// MaxChars returns the character cap for a channel ("cli", "text-bar" or
// "voice"). An unknown channel returns 0, since validation guarantees
// every real style.yaml declares exactly these three.
func (s *Style) MaxChars(ch string) int { return s.maxChars[ch] }

// MaxListItems returns the list-item cap for a channel.
func (s *Style) MaxListItems(ch string) int { return s.maxListItems[ch] }

// Confirmation is the generic confirmation phrasing.
func (s *Style) Confirmation() string { return s.confirmation }

// Tone is the twin's general (non-voice-specific) tone line.
func (s *Style) Tone() string { return s.tone }
