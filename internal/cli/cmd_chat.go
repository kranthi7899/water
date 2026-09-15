package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"water/internal/agent"
	"water/internal/backend"
	"water/internal/chat"
	"water/internal/config"
	"water/internal/roles"
	"water/internal/session"
	"water/internal/tools"
	"water/internal/voice"
)

func (a *App) chatCmd() *cobra.Command {
	var resume string
	var picker bool
	c := &cobra.Command{
		Use:   "chat [role]",
		Short: "Interactive session with one role (slash commands, per-role theme, resumable transcripts)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isTTY(os.Stdin) || !isTTY(os.Stdout) {
				return exitWith(ExitUsage, errors.New("chat needs a terminal; use `water run <role> \"<prompt>\"` for one-shot turns"))
			}
			start := ""
			if len(args) == 1 {
				start = strings.ToLower(args[0])
			}
			return a.runChat(context.Background(), start, resume, picker || start == "")
		},
	}
	c.Flags().StringVar(&resume, "resume", "", "resume a session by slug")
	c.Flags().BoolVar(&picker, "picker", false, "start at the agent picker")
	return c
}

// runChat wires the TUI. Backend resolution per role goes through Select.
func (a *App) runChat(ctx context.Context, start, resume string, picker bool) error {
	cfg, err := a.config()
	if err != nil {
		return err
	}
	reg, err := a.roleRegistry()
	if err != nil {
		return err
	}
	if start != "" {
		if _, ok := reg.Get(start); !ok {
			return exitWith(ExitUsage, fmt.Errorf("unknown role %q (known: %s)", start, strings.Join(reg.Slugs(), ", ")))
		}
	}
	def, err := a.selectBackend(ctx)
	if err != nil {
		// Logged out? Route to the login flow right here (like `claude`
		// does) instead of failing with an error to decode.
		if lerr := a.loginIfLoggedOut(ctx); lerr != nil {
			return lerr
		}
		if def, err = a.selectBackend(ctx); err != nil {
			return err
		}
	}
	if w := meteredLeakWarning(def); w != "" {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	env, res, err := a.roleEnv(ctx, reg, def)
	if err != nil {
		return err
	}
	auth := "subscription"
	if def.Availability.Metered {
		auth = "METERED"
	}
	sessionsRoot := filepath.Join(config.Home(), "sessions")
	opts := chat.Options{
		Registry:   reg,
		Themes:     a.themes,
		ForceTheme: cfg.UI.Theme,
		StartRole:  start,
		ResumeSlug: resume,
		Picker:     picker,
		Retention:  session.Retention{Keep: cfg.Sessions.Keep, MaxAge: cfg.SessionMaxAge()},
		EnvFor: func(r *roles.Role) (agent.Env, chat.BackendInfo, error) {
			rr := res[r.Slug]
			return env, chat.BackendInfo{Name: rr.Backend, Reason: rr.Reason, Auth: auth}, nil
		},
		StoreFor:    func(r *roles.Role) *session.Store { return session.New(sessionsRoot, r.Slug) },
		BudgetLine:  func() string { return backend.LoadRateLimit(config.Home()).Summary() },
		OnRateLimit: func(rl *backend.RateLimit) { _ = backend.SaveRateLimit(config.Home(), rl) },
		SwitchBackend: func(r *roles.Role, name string) (backend.Backend, string, error) {
			sel, err := backend.Select(ctx, backend.Default, backend.SelectConfig{Flag: name, RoleSlug: r.Slug, AllowMetered: cfg.Backend.AllowMetered})
			if err != nil {
				return nil, "", err
			}
			return sel.Backend, "session override (/backend)", nil
		},
	}
	// Interactive chat follows the same broad shape as Codex's ask-for-
	// approval profile: the directory the user launched from is the only
	// workspace, while writes and commands require an explicit per-action
	// decision. Headless runs and orchestration do not receive this overlay.
	if workspace, werr := os.Getwd(); werr == nil {
		if approvals, aerr := tools.NewApprovalBroker(); aerr == nil {
			defer approvals.Close()
			opts.Approvals = approvals
			opts.WorkspacePolicy = func(r *roles.Role) *tools.Policy {
				return tools.InteractiveWorkspacePolicy(r.Slug, r.RoleID, workspace, config.Home(), approvals.Socket())
			}
		} else {
			fmt.Fprintln(os.Stderr, "warning: interactive action approvals unavailable; local tools remain disabled:", aerr)
		}
	}
	// Voice is wired whenever a provider is ready, so /voice on and Ctrl+B
	// work without restarting. The factory is role-aware: a CEO session and a
	// Design session may use distinct expressive profiles without changing the
	// text/model path.
	if vp, verr := a.voiceProvider(cfg, "ceo"); verr == nil && vp.Available() {
		opts.VoiceFor = func(r *roles.Role) func(text string) error {
			p, err := a.voiceProvider(cfg, r.Slug)
			if err != nil || !p.Available() {
				return nil
			}
			return func(text string) error { return p.Speak(ctx, text) }
		}
		opts.VoiceOn = a.flags.voice
	} else if a.flags.voice {
		msg := voice.ErrUnavailable.Error()
		if verr != nil {
			msg = verr.Error()
		} else {
			msg = voice.Absence(vp)
		}
		fmt.Fprintln(os.Stderr, "voice:", msg, "— continuing without speech")
	}
	return chat.Run(ctx, opts)
}
