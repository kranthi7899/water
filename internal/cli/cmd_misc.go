package cli

import (
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
	return &cobra.Command{
		Use:    "voice",
		Short:  "Voice session (reserved for Phase 4)",
		Hidden: true,
		RunE: func(*cobra.Command, []string) error {
			return exitWith(ExitUsage, fmt.Errorf("%w; providers registered: %v", voice.ErrUnavailable, voice.Names()))
		},
	}
}

func (a *App) dashboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "dashboard",
		Short:  "Read-only local dashboard (reserved for Phase 5)",
		Hidden: true,
		RunE: func(*cobra.Command, []string) error {
			return exitWith(ExitUsage, fmt.Errorf("dashboard is not yet available in this build (Phase 5); traces are in the configured trace_dir"))
		},
	}
}
