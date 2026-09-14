// Package auth triggers subscription logins from inside Water (Part 3B).
//
// onboard/doctor used to only detect login state; now they run `claude login`
// / `codex login` as subprocesses with inherited stdio so the browser flow
// opens from inside Water, falling back to headless paths when no browser is
// available. Setup is never declared successful until one clean real
// round-trip completes.
package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"water/internal/backend"
)

// Runner runs an interactive subprocess with inherited stdio. Overridable in
// tests.
type Runner func(ctx context.Context, name string, args ...string) error

// DefaultRunner inherits the terminal so browser/device-code flows work.
func DefaultRunner(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = backend.ScrubbedEnv()
	return cmd.Run()
}

// HasBrowser guesses whether a browser can be opened from this session.
func HasBrowser(env func(string) string) bool {
	if env("WATER_HEADLESS") != "" || env("SSH_CONNECTION") != "" || env("CI") != "" {
		return false
	}
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	default:
		return env("DISPLAY") != "" || env("WAYLAND_DISPLAY") != "" || env("BROWSER") != ""
	}
}

// LoginPlan describes what Login will run for a backend.
type LoginPlan struct {
	Backend  string
	Command  []string
	Headless bool
	Note     string
}

// Plan chooses the login command for a backend name.
func Plan(name string, headless bool) (LoginPlan, error) {
	switch name {
	case backend.ClaudeSubscriptionName:
		if headless {
			return LoginPlan{Backend: name, Command: []string{"claude", "setup-token"}, Headless: true,
				Note: "no browser detected: `claude setup-token` prints a URL to open elsewhere and asks for the token"}, nil
		}
		return LoginPlan{Backend: name, Command: []string{"claude", "login"}, Note: "opens your browser to sign in with your Claude subscription"}, nil
	case backend.CodexSubscriptionName:
		if headless {
			return LoginPlan{Backend: name, Command: []string{"codex", "login", "--device-auth"}, Headless: true,
				Note: "no browser detected: codex device-code login prints a code to enter on another device"}, nil
		}
		return LoginPlan{Backend: name, Command: []string{"codex", "login"}, Note: "opens your browser to sign in with ChatGPT"}, nil
	}
	return LoginPlan{}, fmt.Errorf("no login flow for backend %q", name)
}

// Login runs the login flow for a backend. It returns nil only if the
// subprocess exited cleanly.
func Login(ctx context.Context, run Runner, name string, headless bool) (LoginPlan, error) {
	plan, err := Plan(name, headless)
	if err != nil {
		return plan, err
	}
	if run == nil {
		run = DefaultRunner
	}
	if _, err := exec.LookPath(plan.Command[0]); err != nil {
		return plan, fmt.Errorf("%s is not installed", plan.Command[0])
	}
	if err := run(ctx, plan.Command[0], plan.Command[1:]...); err != nil {
		return plan, fmt.Errorf("%s exited with an error: %w", strings.Join(plan.Command, " "), err)
	}
	return plan, nil
}

// ErrRoundTripFailed is returned when the verification conversation fails.
var ErrRoundTripFailed = errors.New("verification round trip failed")

// VerifyRoundTrip runs one real, minimal conversation through the backend and
// checks a coherent, non-empty reply came back. This is the gate for
// "setup successful": detect → login → verify → succeed.
func VerifyRoundTrip(ctx context.Context, b backend.Backend, timeout time.Duration) (backend.Response, error) {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	resp, err := b.Run(ctx, backend.Request{
		System:  "You are a connectivity check. Reply with exactly the single word OK and nothing else.",
		Prompt:  "ping",
		Role:    "onboard",
		Timeout: timeout,
	})
	if err != nil {
		return resp, fmt.Errorf("%w: %v", ErrRoundTripFailed, err)
	}
	if strings.TrimSpace(resp.Text) == "" {
		return resp, fmt.Errorf("%w: empty reply", ErrRoundTripFailed)
	}
	if resp.Metered {
		return resp, fmt.Errorf("%w: the reply was METERED; a subscription login was expected", ErrRoundTripFailed)
	}
	return resp, nil
}

// Confirm asks a yes/no question on the terminal; yes=true skips asking.
func Confirm(in io.Reader, out io.Writer, question string, yes bool) bool {
	if yes {
		return true
	}
	fmt.Fprintf(out, "%s [Y/n] ", question)
	var buf [64]byte
	n, _ := in.Read(buf[:])
	ans := strings.ToLower(strings.TrimSpace(string(buf[:n])))
	return ans == "" || ans == "y" || ans == "yes"
}
