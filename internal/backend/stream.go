package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"water/internal/tools"
)

// runScrubbedStream is runScrubbed with an onLine callback invoked for each
// line of stdout as it arrives, instead of buffering the whole call before
// returning. The full stdout is still collected and returned, so callers that
// need the final transcript (parseClaudeResult, parseRateLimit) keep working
// unchanged.
func runScrubbedStream(ctx context.Context, timeout time.Duration, dir, stdin, bin string, args []string, onLine func(string)) (stdout, stderr string, err error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, args...)
	cmd.Env = ScrubbedEnv()
	if dir != "" {
		cmd.Dir = dir
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	containProcessGroup(cmd)
	cmd.WaitDelay = time.Second
	stdoutPipe, perr := cmd.StdoutPipe()
	if perr != nil {
		return "", "", perr
	}
	var eb strings.Builder
	cmd.Stderr = &eb
	shown := redactArgs(args)
	start := time.Now()
	if err = cmd.Start(); err != nil {
		return "", "", err
	}
	inflight.Store(cmd, InFlightCall{PID: cmd.Process.Pid, Bin: bin, Args: shown, Started: start})
	defer inflight.Delete(cmd)
	if Debug != nil {
		fmt.Fprintf(Debug, "[debug] exec (stream) pid=%d %s %s (cwd=%s, timeout=%s)\n", cmd.Process.Pid, bin, shown, dir, timeout)
	}
	var ob strings.Builder
	sc := bufio.NewScanner(stdoutPipe)
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		ob.WriteString(line)
		ob.WriteByte('\n')
		if onLine != nil {
			onLine(line)
		}
	}
	scanErr := sc.Err()
	err = cmd.Wait()
	if err == nil && scanErr != nil {
		err = scanErr
	}
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("%w: %s killed after %s", ErrCallTimeout, bin, timeout)
	}
	if Debug != nil {
		fmt.Fprintf(Debug, "[debug] exit (stream) pid=%d after %s err=%v stdout=%dB stderr=%dB\n",
			cmd.Process.Pid, time.Since(start).Round(time.Millisecond), err, ob.Len(), eb.Len())
	}
	return ob.String(), eb.String(), err
}

// buildStreamArgs is BuildArgs plus --include-partial-messages, inserted only
// when the installed CLI advertises it. BuildArgs itself stays a pure,
// guard-tested function with an unchanged signature; streaming is additive.
func (c *ClaudeSubscription) buildStreamArgs(fs flagSet, req Request, mcpCfg string) ([]string, error) {
	args, err := c.BuildArgs(fs, req, mcpCfg)
	if err != nil {
		return nil, err
	}
	if fs["--include-partial-messages"] {
		args = insertAfter(args, "--verbose", "--include-partial-messages")
	}
	return args, nil
}

func insertAfter(args []string, after, flag string) []string {
	for i, a := range args {
		if a == after {
			out := make([]string, 0, len(args)+1)
			out = append(out, args[:i+1]...)
			out = append(out, flag)
			out = append(out, args[i+1:]...)
			return out
		}
	}
	return append(args, flag)
}

// parseTextDelta extracts assistant text from one stream-json partial-message
// line: {"type":"stream_event","event":{"type":"content_block_delta",
// "delta":{"type":"text_delta","text":"..."}}}. Any other line, or a
// non-text delta, reports ok=false.
func parseTextDelta(line string) (text string, ok bool) {
	var ev struct {
		Type  string `json:"type"`
		Event struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		} `json:"event"`
	}
	if json.Unmarshal([]byte(line), &ev) != nil {
		return "", false
	}
	if ev.Type != "stream_event" || ev.Event.Type != "content_block_delta" || ev.Event.Delta.Type != "text_delta" {
		return "", false
	}
	return ev.Event.Delta.Text, ev.Event.Delta.Text != ""
}

// RunStream is ClaudeSubscription's Streamer implementation. It streams
// stdout line by line, calling onDelta with each piece of assistant text in
// order, and returns the same Response Run would have (built from the final
// stream-json result line, falling back to the concatenated deltas).
func (c *ClaudeSubscription) RunStream(ctx context.Context, req Request, onDelta func(string)) (Response, error) {
	path, err := exec.LookPath(c.bin())
	if err != nil {
		return Response{}, fmt.Errorf("claude CLI not found: %w", err)
	}
	fs, err := detectFlags(ctx, path, "--help")
	if err != nil {
		return Response{}, err
	}

	mcpCfg, logPath, cleanup, err := c.setupTools(req)
	if err != nil {
		return Response{}, err
	}
	defer cleanup()

	args, err := c.buildStreamArgs(fs, req, mcpCfg)
	if err != nil {
		return Response{}, err
	}
	stdin := ""
	if len(req.Attachments) > 0 && fs["--input-format"] {
		stdin = streamJSONUserMessage(req)
	}

	start := time.Now()
	var textBuf strings.Builder
	onLine := func(line string) {
		if d, ok := parseTextDelta(line); ok {
			textBuf.WriteString(d)
			if onDelta != nil {
				onDelta(d)
			}
		}
	}
	stdout, stderr, err := runScrubbedStream(ctx, req.Timeout, c.WorkDir, stdin, path, args, onLine)
	dur := time.Since(start)
	resp := Response{Raw: stdout, Backend: c.Name(), Duration: dur, Model: req.Model}
	if stdin != "" {
		resp.AttachmentsDelivered = true
	}
	if logPath != "" {
		resp.ToolEvents, _ = tools.ReadEvents(logPath)
	}
	resp.RateLimit = parseRateLimit(stdout)
	if err != nil && errors.Is(err, ErrCallTimeout) {
		return resp, fmt.Errorf("claude failed: %w", err)
	}
	if res, ok := parseClaudeResult(stdout); ok {
		resp.Text = strings.TrimSpace(res.Result)
		if resp.Text == "" {
			resp.Text = strings.TrimSpace(textBuf.String())
		}
		resp.InputTokens = res.Usage.InputTokens + res.Usage.CacheReadInput + res.Usage.CacheCreationInput
		resp.OutputTokens = res.Usage.OutputTokens
		if res.IsError {
			if IsRateLimitText(res.Result) || (resp.RateLimit != nil && resp.RateLimit.Status != "" && resp.RateLimit.Status != "allowed") {
				return resp, fmt.Errorf("%w: %s", ErrRateLimited, firstLine(res.Result))
			}
			if req.Model != "" && looksLikeBadModel(res.Result) {
				warnBadModelOnce(req.Model, res.Result)
				retry := req
				retry.Model = ""
				return c.RunStream(ctx, retry, onDelta)
			}
			return resp, fmt.Errorf("claude reported an error: %s", firstLine(res.Result))
		}
		return resp, nil
	}
	if err != nil {
		if errors.Is(err, ErrCallTimeout) {
			return resp, fmt.Errorf("claude failed: %w", err)
		}
		return resp, fmt.Errorf("claude failed: %w: %s", err, ErrorSummary(stderr, stdout))
	}
	resp.Text = strings.TrimSpace(textBuf.String())
	if resp.Text == "" {
		resp.Text = strings.TrimSpace(stdout)
	}
	return resp, nil
}

// badModelHint is what a rejected --model value looks like in the CLI's
// error text. Kept narrow so a genuine unrelated error is never swallowed.
func looksLikeBadModel(s string) bool {
	l := strings.ToLower(s)
	if !strings.Contains(l, "model") {
		return false
	}
	for _, hint := range []string{"not found", "unknown model", "invalid model", "unrecognized", "does not exist", "no such model"} {
		if strings.Contains(l, hint) {
			return true
		}
	}
	return false
}

var badModelLogged sync.Map // model name -> struct{}

// warnBadModelOnce logs, once per model name per process, that the CLI
// rejected it and the call is falling back to the CLI's default model.
func warnBadModelOnce(model, detail string) {
	if model == "" {
		return
	}
	if _, loaded := badModelLogged.LoadOrStore(model, struct{}{}); loaded {
		return
	}
	if Debug != nil {
		fmt.Fprintf(Debug, "[debug] model %q rejected by claude CLI, falling back to the CLI default: %s\n", model, firstLine(detail))
	}
}
