package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"runtime"

	"github.com/spf13/cobra"

	"water/internal/dashboard"
	"water/internal/identity"
	"water/internal/roles"
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
		Use:   "voice [text]",
		Short: "Speak text through the OS provider (test the voice path)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			vp, ok := voice.Open(cfg.Voice.Provider)
			if !ok {
				return exitWith(ExitUsage, fmt.Errorf("provider %q not registered (%v)", cfg.Voice.Provider, voice.Names()))
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
}

func (a *App) dashboardCmd() *cobra.Command {
	var addr string
	var noOpen bool
	c := &cobra.Command{
		Use:   "dashboard",
		Short: "Read-only local web view of roles, runs, diagnostics and tool invocations",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			reg, _ := a.roleRegistry()
			var key []byte
			if k := a.keys(); k != nil {
				key = k.Key
			}
			fileStatus := func(r *roles.Role) []identity.FileStatus {
				var out []identity.FileStatus
				fsys := a.source().FS()
				for _, n := range []string{"soul.md", "experience.md"} {
					b, err := fs.ReadFile(fsys, path.Join(r.Dir, n))
					if err != nil {
						continue
					}
					out = append(out, identity.Status(n, b, r.RoleID, key, false))
				}
				if r.Persona != nil {
					for _, sk := range r.Persona.Skills {
						b, err := fs.ReadFile(fsys, path.Join(sk.Dir, "SKILL.md"))
						if err != nil {
							continue
						}
						out = append(out, identity.Status(path.Join("skills", sk.Slug, "SKILL.md"), b, r.RoleID, key, false))
					}
				}
				return out
			}
			backendFor := func(slug string) string {
				if reg == nil {
					return ""
				}
				if r, ok := reg.Get(slug); ok && r.Backend != "" {
					return r.Backend + " (role.yaml)"
				}
				return cfg.Backend.Preferred
			}
			srv, err := dashboard.New(cfg.Telemetry.TraceDir, cfg.Orchestration.CheckpointDir, reg, fileStatus, backendFor)
			if err != nil {
				return err
			}
			url, err := srv.Serve(addr)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "dashboard (read-only) at %s — ctrl+c to stop\n", url)
			if !noOpen {
				openBrowser(url)
			}
			select {}
		},
	}
	c.Flags().StringVar(&addr, "addr", "127.0.0.1:0", "listen address (loopback by default)")
	c.Flags().BoolVar(&noOpen, "no-open", false, "do not open a browser")
	return c
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return
	}
	_ = cmd.Start()
}
