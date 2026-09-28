package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"water/internal/voice"
)

func (a *App) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if a.jsonMode() {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{"version": Version, "commit": Commit, "date": Date, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH})
			}
			fmt.Printf("water %s (%s, %s) %s %s/%s\n", Version, Commit, Date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}

func (a *App) voiceCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "voice [text]",
		Short: "Speak text in the CEO twin's voice (test the voice path)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			vp, err := a.voiceProvider(cfg)
			if err != nil {
				return exitWith(ExitUsage, err)
			}
			if !vp.Available() {
				return exitWith(ExitUsage, fmt.Errorf("%s", voice.Absence(vp)))
			}
			text := "water is ready"
			if len(args) == 1 {
				text = args[0]
			}
			return vp.Speak(context.Background(), text)
		},
	}
	return c
}
