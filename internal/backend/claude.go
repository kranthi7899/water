package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ClaudeSubscription runs the `claude` CLI in headless print mode so the call
// is absorbed by the user's Claude subscription quota.
type ClaudeSubscription struct {
	// Bin overrides the binary name (tests).
	Bin string
	// WorkDir is the subprocess cwd. Set to a neutral directory (water home) so
	// the CLI does not pick up CLAUDE.md context from wherever the user ran water.
	WorkDir string
	// Model optionally pins a model; empty = the CLI's default.
	Model string
}

const ClaudeSubscriptionName = "claude-subscription"

func init() { Default.Register(&ClaudeSubscription{}) }

func (c *ClaudeSubscription) Name() string { return ClaudeSubscriptionName }

func (c *ClaudeSubscription) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "claude"
}

type claudeAuthStatus struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod"`
	APIProvider      string `json:"apiProvider"`
	SubscriptionType string `json:"subscriptionType"`
	Email            string `json:"email"`
}

func (c *ClaudeSubscription) Available(ctx context.Context) Availability {
	path, err := exec.LookPath(c.bin())
	if err != nil {
		return Availability{Detail: "claude CLI not found on PATH (install: https://claude.com/claude-code)"}
	}
	av := Availability{Installed: true}

	fs, err := detectFlags(ctx, path, "--help")
	if err != nil {
		av.Detail = "claude --help failed: " + firstLine(err.Error())
		return av
	}
	if !fs["--print"] || !fs["--output-format"] {
		av.Detail = "claude CLI lacks headless flags (--print/--output-format); upgrade claude"
		return av
	}
	if !fs["--system-prompt"] && !fs["--append-system-prompt"] {
		av.Detail = "claude CLI lacks --system-prompt; upgrade claude"
		return av
	}

	// `claude auth status` prints JSON in recent releases. If the subcommand is
	// missing we cannot confirm auth; report installed-but-unknown rather than
	// guessing.
	out, _, err := runScrubbed(ctx, 20*time.Second, c.WorkDir, "", path, "auth", "status")
	if err != nil && strings.TrimSpace(out) == "" {
		av.Detail = "installed; auth state unknown (`claude auth status` unavailable) — run `claude` once to log in"
		av.Authed = true // optimistic: the CLI itself will fail clearly if not logged in
		return av
	}
	var st claudeAuthStatus
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &st); jerr != nil {
		av.Detail = "installed; could not parse `claude auth status`: " + firstLine(out)
		av.Authed = strings.Contains(strings.ToLower(out), "logged in")
		return av
	}
	av.Authed = st.LoggedIn
	switch {
	case !st.LoggedIn:
		av.Detail = "installed; not logged in — run `claude` and sign in with your subscription"
	case strings.EqualFold(st.AuthMethod, "claude.ai"):
		av.Detail = fmt.Sprintf("subscription login (%s, %s)", st.SubscriptionType, st.Email)
	default:
		// Any non-claude.ai auth method (apiKey, bedrock, vertex…) is metered
		// from the user's point of view.
		av.Metered = true
		av.Detail = fmt.Sprintf("logged in via %s — this is METERED, not subscription", st.AuthMethod)
	}
	return av
}

type claudeResult struct {
	Type       string `json:"type"`
	Subtype    string `json:"subtype"`
	IsError    bool   `json:"is_error"`
	Result     string `json:"result"`
	DurationMS int64  `json:"duration_ms"`
	Usage      struct {
		InputTokens        int `json:"input_tokens"`
		OutputTokens       int `json:"output_tokens"`
		CacheReadInput     int `json:"cache_read_input_tokens"`
		CacheCreationInput int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

func (c *ClaudeSubscription) Run(ctx context.Context, req Request) (Response, error) {
	path, err := exec.LookPath(c.bin())
	if err != nil {
		return Response{}, fmt.Errorf("claude CLI not found: %w", err)
	}
	fs, err := detectFlags(ctx, path, "--help")
	if err != nil {
		return Response{}, err
	}
	args := []string{"--print", "--output-format", "json"}
	switch {
	case fs["--system-prompt"]:
		args = append(args, "--system-prompt", req.System)
	case fs["--append-system-prompt"]:
		args = append(args, "--append-system-prompt", req.System)
	default:
		return Response{}, fmt.Errorf("claude CLI lacks --system-prompt/--append-system-prompt; upgrade claude")
	}
	// Persona agents answer; they do not run tools on the user's machine.
	if fs["--tools"] {
		args = append(args, "--tools", "")
	}
	if fs["--no-session-persistence"] {
		args = append(args, "--no-session-persistence")
	}
	if fs["--disable-slash-commands"] {
		args = append(args, "--disable-slash-commands")
	}
	// No MCP servers: persona agents must not see the user's connectors.
	if fs["--strict-mcp-config"] {
		args = append(args, "--strict-mcp-config")
	}
	if c.Model != "" && fs["--model"] {
		args = append(args, "--model", c.Model)
	}
	args = append(args, "--", req.Prompt)

	start := time.Now()
	stdout, stderr, err := runScrubbed(ctx, req.Timeout, c.WorkDir, "", path, args...)
	dur := time.Since(start)
	resp := Response{Raw: stdout, Backend: c.Name(), Duration: dur}

	var res claudeResult
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); jerr == nil && res.Type == "result" {
		resp.Text = strings.TrimSpace(res.Result)
		resp.InputTokens = res.Usage.InputTokens + res.Usage.CacheReadInput + res.Usage.CacheCreationInput
		resp.OutputTokens = res.Usage.OutputTokens
		if res.IsError {
			return resp, fmt.Errorf("claude returned an error result (%s): %s", res.Subtype, firstLine(res.Result))
		}
		return resp, nil
	}
	if err != nil {
		return resp, fmt.Errorf("claude failed: %w: %s", err, firstLine(stderr+stdout))
	}
	resp.Text = strings.TrimSpace(stdout)
	return resp, nil
}
