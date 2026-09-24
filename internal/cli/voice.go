package cli

import (
	"fmt"
	"os"
	"sort"

	"water/internal/config"
	"water/internal/voice"
)

// voiceProvider is deliberately separate from backend selection. Expressive
// speech is an optional, explicitly metered render surface; it never borrows
// credentials from Water's Claude/Codex subscription backends.
func (a *App) voiceProvider(cfg *config.Resolved) (voice.Provider, error) {
	switch cfg.Voice.Provider {
	case "openai":
		return voice.NewOpenAI(voice.OpenAIOptions{
			APIKey:       os.Getenv("OPENAI_API_KEY"),
			Voice:        cfg.Voice.CEOVoice,
			Model:        cfg.Voice.Model,
			AllowMetered: cfg.Voice.AllowMetered,
		}), nil
	case "os":
		// The system engine with the CEO's preferred installed voice and
		// pace; voice.ceo_voice overrides the voice when it is installed.
		return voice.NewOSVoice(cfg.Voice.CEOVoice), nil
	}
	vp, ok := voice.Open(cfg.Voice.Provider)
	if !ok {
		return nil, fmt.Errorf("voice provider %q not registered (valid: %v)", cfg.Voice.Provider, voiceProviderNames())
	}
	return vp, nil
}

// voiceProviderNames lists every valid voice.provider value: the registered
// ones plus "openai", which voiceProvider builds directly.
func voiceProviderNames() []string {
	names := append([]string{"openai"}, voice.Names()...)
	sort.Strings(names)
	return names
}

// replySpeaker resolves the configured voice provider for --voice replies
// (`water ask`, `water chat`) and fails with the provider's own explanation
// when it cannot speak, so --voice uses the same voice as `water voice`.
func (a *App) replySpeaker() (voice.Provider, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	vp, err := a.voiceProvider(cfg)
	if err != nil {
		return nil, err
	}
	if !vp.Available() {
		return nil, fmt.Errorf("%s", voice.Absence(vp))
	}
	return vp, nil
}
