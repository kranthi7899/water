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

// SupportsAttachments: images via --image when the installed codex has it.
func (c *CodexSubscription) SupportsAttachments() bool { return true }

// SupportsTools: codex can load MCP servers from config (-c mcp_servers.*),
// but that path is NOT wired in this build because it could not be verified
// on a machine without codex. Roles with tools on this backend get none, and
// the call proceeds without them.
func (c *CodexSubscription) SupportsTools() bool { return false }

func extFor(mt string) string {
	switch mt {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	return ".bin"
}

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
	model := req.Model
	if model == "" {
		model = c.Model
	}
	if model != "" && fs["--model"] {
		args = append(args, "--model", model)
	}
	// Attachments: codex exec exposes --image for image files in recent
	// releases; other kinds are inlined as untrusted text. Not verified on
	// this machine (codex is not installed) — detected at runtime, not assumed.
	var imgs []string
	var inlined []string
	for _, a := range req.Attachments {
		if a.Kind == "image" && fs["--image"] {
			f, ferr := os.CreateTemp("", "water-codex-img-*"+extFor(a.MediaType))
			if ferr == nil {
				_, _ = f.Write(a.Data)
				f.Close()
				imgs = append(imgs, f.Name())
				defer os.Remove(f.Name())
				continue
			}
		}
		if a.Kind == "text" {
			inlined = append(inlined, InlineTextAttachment(a))
		}
	}
	for _, p := range imgs {
		args = append(args, "--image", p)
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
	if len(inlined) > 0 {
		prompt += "\n\n" + strings.Join(inlined, "\n\n")
	}
	if strings.TrimSpace(req.System) != "" {
		prompt = "<system>\n" + req.System + "\n</system>\n\n" + req.Prompt
	}
	args = append(args, "--", prompt)

	start := time.Now()
	stdout, stderr, err := runScrubbed(ctx, req.Timeout, c.WorkDir, "", path, args...)
	resp := Response{Raw: stdout, Backend: c.Name(), Duration: time.Since(start), Model: model}
	resp.AttachmentsDelivered = len(imgs) > 0 || len(inlined) > 0
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
