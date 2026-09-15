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

// Noop is the Phase 1 provider.
type Noop struct{}

func (Noop) Name() string                           { return "noop" }
func (Noop) Available() bool                        { return false }
func (Noop) Speak(context.Context, string) error    { return ErrUnavailable }
func (Noop) Listen(context.Context) (string, error) { return "", ErrUnavailable }

var providers = map[string]func() Provider{
	"noop": func() Provider { return Noop{} },
}

// Register adds a provider (e.g. "os" in Phase 4).
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

// Absence returns a clear explanation of why a provider cannot speak.
func Absence(p Provider) string {
	if o, ok := p.(*OS); ok {
		return o.Absence()
	}
	if o, ok := p.(*OpenAI); ok {
		return o.Absence()
	}
	return ErrUnavailable.Error()
}
