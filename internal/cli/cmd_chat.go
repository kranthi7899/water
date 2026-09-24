package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"water/internal/runtime"
	"water/internal/voice"
)

// chatCmd is an interactive session with the CEO twin: a line at a time in,
// a streamed reply out, against the running water daemon. It replaces the
// former multi-role TUI (persona picker, per-role themes, /switch, /consult)
// now that there is one twin: `water ask` covers one-shot turns, and this
// covers a back-and-forth conversation.
func (a *App) chatCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "chat",
		Short: "Interactive conversation with the CEO twin (against the water daemon)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !isTTY(os.Stdin) || !isTTY(os.Stdout) {
				return exitWith(ExitUsage, errors.New("chat needs a terminal; use `water ask \"<prompt>\"` for one-shot turns"))
			}
			return a.runChat(context.Background())
		},
	}
	return c
}

func (a *App) runChat(ctx context.Context) error {
	client, err := newDaemonClient()
	if err != nil {
		return exitWith(ExitError, err)
	}
	speak := a.flags.voice
	var speaker voice.Provider
	if speak {
		if p, err := a.replySpeaker(); err == nil {
			speaker = p
		} else {
			fmt.Fprintf(os.Stderr, "voice: %v — continuing without speech\n", err)
			speak = false
		}
	}

	fmt.Println("water chat — the CEO twin. /clear resets context, /quit (or Ctrl-D) ends the session.")
	in := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("> ")
		line, rerr := in.ReadString('\n')
		line = strings.TrimSpace(line)
		if line != "" {
			if code := a.chatCommand(ctx, client, line); code != chatContinue {
				if code == chatQuit {
					return nil
				}
				continue
			}
			if err := a.chatTurn(ctx, client, line, speak, speaker); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
			}
		}
		if rerr != nil { // EOF (Ctrl-D) or a read error
			fmt.Println()
			return nil
		}
	}
}

type chatCode int

const (
	chatContinue chatCode = iota
	chatHandled
	chatQuit
)

func (a *App) chatCommand(ctx context.Context, client *daemonClient, line string) chatCode {
	switch strings.ToLower(line) {
	case "/quit", "/exit":
		return chatQuit
	case "/clear":
		if err := client.Turn(ctx, "cli", "", true, func(runtime.Event) {}); err != nil {
			fmt.Fprintln(os.Stderr, "clear failed:", err)
		} else {
			fmt.Println("context cleared.")
		}
		return chatHandled
	}
	return chatContinue
}

func (a *App) chatTurn(ctx context.Context, client *daemonClient, prompt string, speak bool, speaker voice.Provider) error {
	channel := "cli"
	if speak {
		channel = "voice"
	}
	var turnErr error
	err := client.Turn(ctx, channel, prompt, false, func(e runtime.Event) {
		switch e.Kind {
		case runtime.EventDelta:
			if !speak {
				fmt.Print(e.Text)
			}
		case runtime.EventSentence:
			if speak && speaker != nil {
				_ = speaker.Speak(ctx, e.Text)
			}
		case runtime.EventError:
			turnErr = errors.New(e.Error)
		case runtime.EventDone:
			if speak {
				fmt.Println(e.Text)
			} else {
				fmt.Println()
			}
		}
	})
	if err != nil {
		return err
	}
	return turnErr
}
