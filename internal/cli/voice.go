package cli

import (
	"fmt"
	"os"

	"water/internal/config"
	"water/internal/voice"
)

// voiceProvider is deliberately separate from backend selection. Expressive
// speech is an optional, explicitly metered render surface; it never borrows
// credentials from Water's Claude/Codex subscription backends.
func (a *App) voiceProvider(cfg *config.Resolved, role string) (voice.Provider, error) {
	if cfg.Voice.Provider == "openai" {
		return voice.NewOpenAI(voice.OpenAIOptions{
			APIKey:       os.Getenv("OPENAI_API_KEY"),
			Role:         role,
			Voice:        cfg.Voice.VoiceFor(role),
			Model:        cfg.Voice.Model,
			AllowMetered: cfg.Voice.AllowMetered,
		}), nil
	}
	if cfg.Voice.Provider == "os" {
		// Free per-role differentiation: same system engine, a distinct
		// installed voice and pace per role.
		return voice.NewOSFor(role, cfg.Voice.VoiceFor(role)), nil
	}
	vp, ok := voice.Open(cfg.Voice.Provider)
	if !ok {
		return nil, fmt.Errorf("provider %q not registered (%v)", cfg.Voice.Provider, voice.Names())
	}
	return vp, nil
}
