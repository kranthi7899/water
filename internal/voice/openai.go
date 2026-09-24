package voice

// Optional expressive cloud speech. It is deliberately separate from Water's
// model backends: a ChatGPT/Codex subscription does not imply API speech
// access. The caller must opt in with both allow_metered and OPENAI_API_KEY.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const openAISpeechEndpoint = "https://api.openai.com/v1/audio/speech"

var ErrMeteredVoiceDisabled = errors.New("expressive OpenAI voice is disabled: set voice.allow_metered: true explicitly")

// RoleProfile gives the CEO twin a durable sonic identity without asking a
// model to invent one. The voice label is a built-in OpenAI voice, not a
// claim to imitate a real person.
type RoleProfile struct {
	Voice        string
	Instructions string
}

// ProfileFor returns the CEO voice profile. Water only ever speaks as "ceo";
// any other role gets a neutral fallback.
func ProfileFor(role string) RoleProfile {
	if role == "ceo" {
		return RoleProfile{"marin", "Composed, confident and measured. Deliver decisions clearly, with restrained warmth and deliberate pacing."}
	}
	return RoleProfile{"marin", "Clear, natural, measured speech."}
}

// OpenAIOptions contains no implicit credentials. APIKey is expected to come
// from OPENAI_API_KEY at the CLI boundary, never Water's model API fallback.
type OpenAIOptions struct {
	APIKey       string
	Role         string
	Voice        string
	Model        string
	AllowMetered bool
	Endpoint     string // tests only; defaults to the official endpoint
	HTTPClient   *http.Client
	Player       func(context.Context, string) error // tests only
}

// OpenAI is a speech-only provider. It does not receive prompts, memory,
// personas, inboxes, tools, or audio input—only completed response text.
type OpenAI struct {
	apiKey       string
	role         string
	voice        string
	model        string
	instructions string
	allowed      bool
	endpoint     string
	client       *http.Client
	player       func(context.Context, string) error
	customPlayer bool
}

func NewOpenAI(o OpenAIOptions) *OpenAI {
	p := ProfileFor(o.Role)
	if o.Voice != "" {
		p.Voice = o.Voice
	}
	if o.Model == "" {
		o.Model = "gpt-4o-mini-tts"
	}
	if o.Endpoint == "" {
		o.Endpoint = openAISpeechEndpoint
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 90 * time.Second}
	}
	v := &OpenAI{apiKey: o.APIKey, role: o.Role, voice: p.Voice, model: o.Model, instructions: p.Instructions, allowed: o.AllowMetered, endpoint: o.Endpoint, client: o.HTTPClient, player: o.Player, customPlayer: o.Player != nil}
	if v.player == nil {
		v.player = playAudio
	}
	return v
}

func (o *OpenAI) Name() string { return "openai" }

func (o *OpenAI) Available() bool {
	return o.allowed && strings.TrimSpace(o.apiKey) != "" && (o.customPlayer || playerAvailable())
}

func (o *OpenAI) Absence() string {
	if !o.allowed {
		return ErrMeteredVoiceDisabled.Error()
	}
	if strings.TrimSpace(o.apiKey) == "" {
		return "expressive OpenAI voice needs OPENAI_API_KEY (it is a separately billed API feature)"
	}
	return "expressive OpenAI voice needs a local audio player (afplay on macOS)"
}

func (o *OpenAI) Speak(ctx context.Context, text string) error {
	if !o.Available() {
		return errors.New(o.Absence())
	}
	for _, chunk := range speechChunks(Speakable(text), 4096) {
		if err := o.speakChunk(ctx, chunk); err != nil {
			return err
		}
	}
	return nil
}

func (o *OpenAI) speakChunk(ctx context.Context, text string) error {
	body, _ := json.Marshal(map[string]any{
		"model": o.model, "voice": o.voice, "input": text,
		"instructions": o.instructions, "response_format": "mp3",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return fmt.Errorf("expressive voice request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("expressive voice API: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	f, err := os.CreateTemp("", "water-voice-*.mp3")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return o.player(ctx, name)
}

// Listen remains intentionally unavailable. Realtime voice would replace the
// backend contract and violates this provider's render-only role.
func (*OpenAI) Listen(context.Context) (string, error) { return "", ErrListenUnavailable }

func playerAvailable() bool {
	switch runtime.GOOS {
	case "darwin":
		_, err := exec.LookPath("afplay")
		return err == nil
	case "linux":
		_, err := exec.LookPath("paplay")
		return err == nil
	default:
		return false
	}
}

func playAudio(ctx context.Context, path string) error {
	bin := ""
	switch runtime.GOOS {
	case "darwin":
		bin = "afplay"
	case "linux":
		bin = "paplay"
	default:
		return fmt.Errorf("no audio player supported on %s", runtime.GOOS)
	}
	cmd := exec.CommandContext(ctx, bin, filepath.Clean(path))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", bin, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// speechChunks preserves text boundaries as much as possible while enforcing
// the audio endpoint's 4096-character limit without silently dropping text.
func speechChunks(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	r := []rune(text)
	var out []string
	for len(r) > limit {
		cut := limit
		for i := limit - 1; i > limit/2; i-- {
			if strings.ContainsRune(".?!;\n", r[i]) {
				cut = i + 1
				break
			}
		}
		out = append(out, strings.TrimSpace(string(r[:cut])))
		r = r[cut:]
	}
	if tail := strings.TrimSpace(string(r)); tail != "" {
		out = append(out, tail)
	}
	return out
}
