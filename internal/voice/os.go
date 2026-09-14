package voice

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"water/internal/backend"
)

func init() { Register("os", func() Provider { return NewOS() }) }

// OS is the operating-system provider (Part 3D): `say` on macOS, `espeak` or
// `spd-say` on Linux, a documented absence elsewhere.
//
// Architectural rule: audio is never reasoned over. Speak renders text the
// model already produced; Listen would feed text into the pipeline. The model
// layer never sees audio. Listen is a no-op in this build: no local
// speech-to-text binding was judged reliable enough to ship, and shipping an
// unreliable one is worse than none (spec 3D).
type OS struct {
	bin  string
	args []string
	Look func(string) (string, error) // exec.LookPath; overridable in tests
}

// NewOS detects the platform TTS binary.
func NewOS() *OS {
	o := &OS{Look: exec.LookPath}
	o.detect()
	return o
}

func (o *OS) detect() {
	candidates := [][]string{}
	switch runtime.GOOS {
	case "darwin":
		candidates = append(candidates, []string{"say"})
	case "linux":
		candidates = append(candidates, []string{"spd-say", "--wait"}, []string{"espeak"}, []string{"espeak-ng"})
	}
	for _, c := range candidates {
		if p, err := o.Look(c[0]); err == nil {
			o.bin, o.args = p, c[1:]
			return
		}
	}
}

func (o *OS) Name() string { return "os" }

// Available reports whether a TTS binary was found.
func (o *OS) Available() bool { return o.bin != "" }

// Absence explains why voice output is unavailable on this platform.
func (o *OS) Absence() string {
	switch runtime.GOOS {
	case "darwin":
		return "text-to-speech unavailable: `say` not found (it ships with macOS; check PATH)"
	case "linux":
		return "text-to-speech unavailable: install `espeak-ng` or `speech-dispatcher` (spd-say)"
	default:
		return fmt.Sprintf("text-to-speech is not supported on %s in this build", runtime.GOOS)
	}
}

// Speak renders text through the OS TTS binary. Long text is spoken as-is;
// the caller decides what to speak (typically the assistant's reply).
func (o *OS) Speak(ctx context.Context, text string) error {
	if o.bin == "" {
		return errors.New(o.Absence())
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, o.bin, o.args...)
	cmd.Stdin = strings.NewReader(text)
	cmd.Env = backend.ScrubbedEnv()
	if runtime.GOOS == "darwin" {
		// `say` reads stdin when no text argument is given.
		cmd = exec.CommandContext(ctx, o.bin)
		cmd.Stdin = strings.NewReader(text)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", o.bin, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ErrListenUnavailable documents the deliberate no-op.
var ErrListenUnavailable = errors.New("speech-to-text is not available in this build: no reliable local STT binding shipped; type your message instead")

// Listen is a documented no-op (see type comment).
func (o *OS) Listen(context.Context) (string, error) { return "", ErrListenUnavailable }
