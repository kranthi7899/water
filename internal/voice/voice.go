// Package voice is extension point 6. Audio is NEVER reasoned over: STT feeds
// the text pipeline, TTS renders text output. The model layer never sees audio.
package voice

import (
	"context"
	"errors"
	"sort"
)

// Provider is the voice seam.
type Provider interface {
	Name() string
	Available() bool
	Speak(ctx context.Context, text string) error
	Listen(ctx context.Context) (string, error)
}

// ErrUnavailable is returned by the no-op provider.
var ErrUnavailable = errors.New("voice provider is noop; set voice.provider to \"os\"")

// Noop is the disabled provider (voice.provider: noop).
type Noop struct{}

func (Noop) Name() string                           { return "noop" }
func (Noop) Available() bool                        { return false }
func (Noop) Speak(context.Context, string) error    { return ErrUnavailable }
func (Noop) Listen(context.Context) (string, error) { return "", ErrUnavailable }

var providers = map[string]func() Provider{
	"noop": func() Provider { return Noop{} },
}

// Register adds a provider by name. Only providers that need no
// configuration live here (noop, and a bare "os"); the configured OS voice and
// the metered OpenAI voice are built directly by cli.voiceProvider.
func Register(name string, f func() Provider) { providers[name] = f }

// Open constructs the named provider.
func Open(name string) (Provider, bool) {
	f, ok := providers[name]
	if !ok {
		return nil, false
	}
	return f(), true
}

// Names lists registered providers.
func Names() []string {
	out := make([]string, 0, len(providers))
	for n := range providers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Absence returns a clear explanation of why a provider cannot speak. A
// provider explains itself by implementing Absence() string; anything else
// (noop) gets the generic "turn voice on" message.
func Absence(p Provider) string {
	if a, ok := p.(interface{ Absence() string }); ok {
		return a.Absence()
	}
	return ErrUnavailable.Error()
}
