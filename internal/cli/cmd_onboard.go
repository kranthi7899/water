package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"water/internal/auth"
	"water/internal/backend"
	"water/internal/config"
)

func (a *App) onboardCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "onboard",
		Short: "Detect claude/codex, sign in from inside water, verify a real round trip, write config",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return a.runOnboard(cmd) },
	}
	c.Flags().Bool("no-login", false, "never launch a login flow; only detect")
	c.Flags().Bool("headless", false, "force the no-browser login path (setup-token / device code)")
	c.Flags().Bool("no-picker", false, "do not open the agent picker after success")
	return c
}

type onboardReport struct {
	Backends   map[string]backend.Availability `json:"backends"`
	Selected   string                          `json:"selected,omitempty"`
	Metered    bool                            `json:"metered"`
	LoginRan   []string                        `json:"login_ran,omitempty"`
	Verified   bool                            `json:"verified"`
	VerifyText string                          `json:"verify_reply,omitempty"`
	Warnings   []string                        `json:"warnings,omitempty"`
	Config     string                          `json:"config"`
	Written    bool                            `json:"written"`
}

// runOnboard: detect → (login) → verify with a real conversation → succeed.
// Success is never declared without one clean round trip.
func (a *App) runOnboard(cmd *cobra.Command) error {
	ctx := context.Background()
	cfg, err := a.config()
	if err != nil {
		return err
	}
	noLogin, _ := cmd.Flags().GetBool("no-login")
	headless, _ := cmd.Flags().GetBool("headless")
	noPicker, _ := cmd.Flags().GetBool("no-picker")
	headless = headless || !auth.HasBrowser(os.Getenv)
	a.configureBackends(cfg)
	rep := onboardReport{Backends: map[string]backend.Availability{}, Config: config.Path()}
	probe := func() {
		for _, b := range backend.Default.All() {
			rep.Backends[b.Name()] = b.Available(ctx)
		}
	}
	probe()
	if !a.jsonMode() {
		fmt.Fprintln(os.Stderr, styleDim.Render("water — a CEO digital twin"))
		a.printBackends(rep)
	}

	// Trigger login for an installed-but-logged-out subscription CLI.
	interactive := isTTY(os.Stdin) && !a.jsonMode()
	for _, name := range []string{backend.ClaudeSubscriptionName, backend.CodexSubscriptionName} {
		av := rep.Backends[name]
		if !av.Installed || av.Authed || noLogin {
			continue
		}
		plan, perr := auth.Plan(name, headless)
		if perr != nil {
			continue
		}
		if !interactive && !a.flags.yes {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s is installed but not logged in; run `%s`", name, joinCmd(plan.Command)))
			continue
		}
		if interactive && !auth.Confirm(os.Stdin, os.Stderr, fmt.Sprintf("  %s is installed but not logged in. Sign in now (%s)?", name, plan.Note), a.flags.yes) {
			continue
		}
		fmt.Fprintf(os.Stderr, "\n  %s %s\n\n", styleDim.Render("running"), joinCmd(plan.Command))
		if _, lerr := auth.Login(ctx, nil, name, headless); lerr != nil {
			rep.Warnings = append(rep.Warnings, lerr.Error())
			continue
		}
		rep.LoginRan = append(rep.LoginRan, joinCmd(plan.Command))
		probe()
		if !a.jsonMode() {
			fmt.Fprintln(os.Stderr)
			a.printBackends(rep)
		}
	}

	sel, selErr := backend.Select(ctx, backend.Default, backend.SelectConfig{Preferred: "auto", AllowMetered: false})
	if selErr != nil {
		msg := "no subscription CLI is installed and logged in.\n" +
			"  install one and sign in with your plan:\n" +
			"    claude  → https://claude.com/claude-code   then `water onboard` signs you in\n" +
			"    codex   → npm i -g @openai/codex            then `water onboard` signs you in\n" +
			"  then run `water onboard` again."
		if a.jsonMode() {
			_ = json.NewEncoder(os.Stdout).Encode(rep)
		}
		return exitWith(ExitUsage, errors.New(msg))
	}
	rep.Selected = sel.Backend.Name()
	rep.Metered = sel.Availability.Metered
	if w := meteredLeakWarning(sel); w != "" {
		rep.Warnings = append(rep.Warnings, w)
	}

	// The gate: one clean real round trip.
	if !a.jsonMode() {
		fmt.Fprintf(os.Stderr, "\n  %s verifying %s with a real round trip…", styleDim.Render("check"), rep.Selected)
	}
	resp, verr := auth.VerifyRoundTrip(ctx, sel.Backend, 2*time.Minute)
	if verr != nil {
		if !a.jsonMode() {
			fmt.Fprintln(os.Stderr, " "+styleErr.Render("failed"))
		}
		if a.jsonMode() {
			_ = json.NewEncoder(os.Stdout).Encode(rep)
		}
		return exitWith(ExitBackend, fmt.Errorf("setup is NOT complete: %w", verr))
	}
	rep.Verified, rep.VerifyText = true, resp.Text
	if !a.jsonMode() {
		fmt.Fprintf(os.Stderr, " %s (%s, %s)\n", styleOK.Render("ok"), resp.Duration.Round(time.Millisecond), styleDim.Render("0 metered"))
	}

	set := map[string]string{"backend.preferred": rep.Selected, "backend.allow_metered": "false", "onboard.verified_at": time.Now().UTC().Format(time.RFC3339)}
	if err := config.Save(set); err != nil {
		return err
	}
	for _, d := range []string{config.MemoryDir(), cfg.Telemetry.TraceDir, cfg.Orchestration.CheckpointDir} {
		_ = os.MkdirAll(d, 0o755)
	}
	rep.Written = true

	if a.jsonMode() {
		return json.NewEncoder(os.Stdout).Encode(rep)
	}
	fmt.Fprintf(os.Stderr, "\n  %s %s\n", styleDim.Render("selected"), styleAccent.Render(rep.Selected))
	fmt.Fprintf(os.Stderr, "  %s %s\n", styleDim.Render("config  "), rep.Config)
	for _, w := range rep.Warnings {
		fmt.Fprintf(os.Stderr, "\n  %s %s\n", styleWarn.Render("warning"), w)
	}
	fmt.Fprintf(os.Stderr, "\n  %s\n", styleDim.Render("next: water daemon · water chat · water ask \"<prompt>\" · water status"))
	if interactive && !noPicker && isTTY(os.Stdout) {
		a.cfg = nil
		return a.runChat(ctx)
	}
	return nil
}

func (a *App) printBackends(rep onboardReport) {
	for _, name := range backend.Default.Names() {
		av := rep.Backends[name]
		mark := styleDim.Render("○")
		if av.Usable() && !av.Metered {
			mark = styleAccent.Render("●")
		} else if av.Usable() {
			mark = styleWarn.Render("$")
		}
		fmt.Fprintf(os.Stderr, "  %s %-20s %s\n", mark, name, styleDim.Render(av.Detail))
	}
}

func joinCmd(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " "
		}
		out += p
	}
	return out
}
