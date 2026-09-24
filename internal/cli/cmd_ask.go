package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"water/internal/runtime"
	"water/internal/voice"
)

// askCmd streams one turn to stdout. With --voice, it also speaks each
// "sentence" event through the OS voice provider as it arrives — the free
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
			client, err := newDaemonClient()
			if err != nil {
				return exitWith(ExitError, err)
			}
			ch := runtime.ChannelCLI
			var speaker voice.Provider
			if speak {
				ch = runtime.ChannelVoice
				p, ok := voice.Open("os")
				if !ok || !p.Available() {
					return exitWith(ExitUsage, errors.New("--voice requires the OS voice provider to be available"))
				}
				speaker = p
			}
			ctx := context.Background()
			var turnErr error
			err = client.Turn(ctx, string(ch), prompt, false, func(e runtime.Event) {
				switch e.Kind {
				case runtime.EventDelta:
					if !speak {
						fmt.Fprint(os.Stdout, e.Text)
					}
				case runtime.EventSentence:
					if speak && speaker != nil {
						_ = speaker.Speak(ctx, e.Text)
					}
				case runtime.EventError:
					turnErr = errors.New(e.Error)
				case runtime.EventDone:
					if speak {
						fmt.Fprintln(os.Stdout, e.Text)
					} else {
						fmt.Fprintln(os.Stdout)
					}
				}
			})
			if err != nil {
				return exitWith(ExitError, err)
			}
			if turnErr != nil {
				return exitWith(ExitError, turnErr)
			}
			return nil
		},
	}
	return c
}
