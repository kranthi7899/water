package backend

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CodexSubscription runs `codex exec` so the call is absorbed by a ChatGPT
// Plus/Pro/Team plan ("Sign in with ChatGPT"). Codex has no system-prompt flag,
// so the persona context is prepended to the prompt under a clear delimiter.
type CodexSubscription struct {
	Bin     string
	WorkDir string
	Model   string
}

const CodexSubscriptionName = "codex-subscription"

func init() { Default.Register(&CodexSubscription{}) }

func (c *CodexSubscription) Name() string { return CodexSubscriptionName }

func (c *CodexSubscription) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "codex"
}

func (c *CodexSubscription) Available(ctx context.Context) Availability {
	path, err := exec.LookPath(c.bin())
	if err != nil {
		return Availability{Detail: "codex CLI not found on PATH (install: npm i -g @openai/codex)"}
	}
	av := Availability{Installed: true}
	fs, err := detectFlags(ctx, path, "exec", "--help")
	if err != nil {
		av.Detail = "codex exec --help failed: " + firstLine(err.Error())
		return av
	}
	if len(fs) == 0 {
		av.Detail = "codex CLI has no `exec` subcommand; upgrade codex"
		return av
	}
	out, errOut, err := runScrubbed(ctx, 20*time.Second, c.WorkDir, "", path, "login", "status")
	text := strings.ToLower(out + " " + errOut)
	switch {
	case strings.Contains(text, "not logged in"):
		av.Detail = "installed; not logged in — run `codex login`"
	case strings.Contains(text, "api key"):
		av.Authed, av.Metered = true, true
		av.Detail = "logged in with an API key — this is METERED, not subscription"
	case strings.Contains(text, "logged in"):
		av.Authed = true
		av.Detail = "subscription login (ChatGPT)"
	case err != nil:
		av.Detail = "installed; auth state unknown: " + firstLine(errOut+out)
	default:
		av.Authed = true
		av.Detail = "installed; auth state unknown (assumed logged in)"
	}
	return av
}

func (c *CodexSubscription) Run(ctx context.Context, req Request) (Response, error) {
	path, err := exec.LookPath(c.bin())
	if err != nil {
		return Response{}, fmt.Errorf("codex CLI not found: %w", err)
	}
	fs, err := detectFlags(ctx, path, "exec", "--help")
	if err != nil {
		return Response{}, err
	}
	args := []string{"exec"}
	if fs["--skip-git-repo-check"] {
		args = append(args, "--skip-git-repo-check")
	}
	if fs["--sandbox"] {
		args = append(args, "--sandbox", "read-only")
	}
	if fs["--color"] {
		args = append(args, "--color", "never")
	}
	if c.Model != "" && fs["--model"] {
		args = append(args, "--model", c.Model)
	}
	var lastMsg string
	if fs["--output-last-message"] {
		f, ferr := os.CreateTemp("", "water-codex-*.txt")
		if ferr == nil {
			lastMsg = f.Name()
			f.Close()
			defer os.Remove(lastMsg)
			args = append(args, "--output-last-message", lastMsg)
		}
	}
	prompt := req.Prompt
	if strings.TrimSpace(req.System) != "" {
		prompt = "<system>\n" + req.System + "\n</system>\n\n" + req.Prompt
	}
	args = append(args, "--", prompt)

	start := time.Now()
	stdout, stderr, err := runScrubbed(ctx, req.Timeout, c.WorkDir, "", path, args...)
	resp := Response{Raw: stdout, Backend: c.Name(), Duration: time.Since(start)}
	if err != nil {
		return resp, fmt.Errorf("codex failed: %w: %s", err, firstLine(stderr+stdout))
	}
	if lastMsg != "" {
		if b, rerr := os.ReadFile(filepath.Clean(lastMsg)); rerr == nil && strings.TrimSpace(string(b)) != "" {
			resp.Text = strings.TrimSpace(string(b))
			return resp, nil
		}
	}
	resp.Text = strings.TrimSpace(stdout)
	return resp, nil
}
