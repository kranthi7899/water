package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"water/internal/runtime"
	"water/internal/voice"
)

// askCmd streams one turn to stdout. With --voice, it also speaks each
// "sentence" event through the configured voice provider as it arrives — the free
// interim voice path a macOS Shortcut can call before the native client
// exists.
func (a *App) askCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "ask <text>",
		Short: "Stream one turn from the water daemon",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prompt := args[0]
			for _, a := range args[1:] {
				prompt += " " + a
			}
			speak := a.flags.voice
			ch := runtime.ChannelCLI
			var speaker voice.Provider
			if speak {
				// Resolve the voice before contacting the daemon so a
				// misconfigured --voice fails fast as a usage error.
				ch = runtime.ChannelVoice
				p, err := a.replySpeaker()
				if err != nil {
					return exitWith(ExitUsage, fmt.Errorf("--voice: %w", err))
				}
				speaker = p
			}
			client, err := newDaemonClient()
			if err != nil {
				return exitWith(ExitError, err)
			}
			ctx := context.Background()
			if speak {
				// Best-effort: the twin's TTS profile (R-15's GET
				// /v1/voice/profile) picks the voice/rate style.yaml
				// declares, so every client sounds the same regardless of
				// which tier answered. A profile that can't be fetched, or
				// that leaves tts.voice empty, is not an error — the
				// already-resolved voice.ceo_voice-based speaker from
				// replySpeaker above is a complete fallback on its own.
				if profile, perr := client.VoiceProfile(ctx); perr == nil {
					applyVoiceProfile(speaker, profile)
				}
			}
			h := &askEventHandler{ctx: ctx, speak: speak, speaker: speaker, out: os.Stdout}
			err = client.Turn(ctx, string(ch), prompt, false, h.handle)
			if err != nil {
				return exitWith(ExitError, err)
			}
			if h.err != nil {
				return exitWith(ExitError, h.err)
			}
			return nil
		},
	}
	return c
}

// applyVoiceProfile overrides an already-resolved voice.OS speaker's
// voice/rate with the daemon's twin-wide TTS profile. Only voice.OS carries
// a settable voice/rate today (the OpenAI provider is metered and opt-in,
// out of R-26's scope); any other provider, or an empty tts.voice, is left
// exactly as replySpeaker resolved it — voice.ceo_voice remains the
// fallback.
func applyVoiceProfile(speaker voice.Provider, profile VoiceProfileResult) {
	osVoice, ok := speaker.(*voice.OS)
	if !ok {
		return
	}
	if profile.TTS.Voice != "" {
		osVoice.SetVoice(profile.TTS.Voice)
	}
	if profile.TTS.RateWPM > 0 {
		osVoice.SetRate(profile.TTS.RateWPM)
	}
}

// askEventHandler consumes one turn's NDJSON event stream (daemonClient.Turn
// already tolerates any Kind value at the JSON-decode layer, since
// runtime.EventKind is just a string; this switch is the second half of
// that tolerance — an event kind this build doesn't recognize yet, such as
// a future "handoff" event, simply matches no case and is ignored, rather
// than erroring or aborting the turn). Kept as its own type (not an inline
// closure) so both cases are directly unit-testable without a real daemon.
type askEventHandler struct {
	ctx     context.Context
	speak   bool
	speaker voice.Provider
	out     io.Writer
	err     error
}

func (h *askEventHandler) handle(e runtime.Event) {
	switch e.Kind {
	case runtime.EventDelta:
		if !h.speak {
			fmt.Fprint(h.out, e.Text)
		}
	case runtime.EventSentence:
		if h.speak && h.speaker != nil {
			_ = h.speaker.Speak(h.ctx, e.Text)
		}
	case runtime.EventError:
		h.err = errors.New(e.Error)
	case runtime.EventDone:
		if h.speak {
			fmt.Fprintln(h.out, e.Text)
		} else {
			fmt.Fprintln(h.out)
		}
	default:
		// Unknown/future event kinds (e.g. "handoff") are ignored by
		// design: a client must never fail a turn over an event it simply
		// doesn't understand yet.
	}
}
