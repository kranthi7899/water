package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"water/internal/auth"
	"water/internal/backend"
	"water/internal/surface"
)

// loginIfLoggedOut finds an installed subscription CLI that is not signed in
// and runs its login flow from inside water, with the browser or the headless
// fallback. It returns an error only when nothing can be logged into.
func (a *App) loginIfLoggedOut(ctx context.Context) error {
	cfg, err := a.config()
	if err != nil {
		return err
	}
	a.configureBackends(cfg)
	headless := !auth.HasBrowser(os.Getenv)
	interactive := isTTY(os.Stdin) && !a.jsonMode()
	var candidates []string
	for _, name := range []string{backend.ClaudeSubscriptionName, backend.CodexSubscriptionName} {
		b, ok := backend.Default.Get(name)
		if !ok {
			continue
		}
		av := b.Available(ctx)
		if av.Installed && !av.Authed {
			candidates = append(candidates, name)
		}
	}
	if len(candidates) == 0 {
		return exitWith(ExitBackend, errors.New("no subscription CLI is installed; run `water onboard`"))
	}
	for _, name := range candidates {
		plan, perr := auth.Plan(name, headless)
		if perr != nil {
			continue
		}
		if !interactive && !a.flags.yes {
			return exitWith(ExitBackend, fmt.Errorf("%s is installed but not logged in; run `%s`", name, joinCmd(plan.Command)))
		}
		fmt.Fprintf(os.Stderr, "\n  %s %s is installed but not logged in.\n", surface.StyleWarn.Render("login"), name)
		if !auth.Confirm(os.Stdin, os.Stderr, fmt.Sprintf("  Sign in now? (%s)", plan.Note), a.flags.yes) {
			continue
		}
		fmt.Fprintf(os.Stderr, "  %s %s\n\n", surface.StyleDim.Render("running"), joinCmd(plan.Command))
		if _, lerr := auth.Login(ctx, nil, name, headless); lerr != nil {
			fmt.Fprintln(os.Stderr, "  login failed:", lerr)
			continue
		}
		if b, ok := backend.Default.Get(name); ok && b.Available(ctx).Authed {
			fmt.Fprintf(os.Stderr, "  %s signed in to %s\n\n", surface.StyleOK.Render("ok"), name)
			return nil
		}
	}
	return exitWith(ExitBackend, errors.New("not signed in to any subscription CLI"))
}
