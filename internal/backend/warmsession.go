package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// defaultMaxTurns bounds how long one warm process lives before it is
// recycled, so a stuck or slowly-leaking CLI process cannot run forever.
const defaultMaxTurns = 40

// WarmSessionConfig configures the persistent claude process.
type WarmSessionConfig struct {
	// Bin overrides the binary name (tests).
	Bin string
	// WorkDir is the subprocess cwd.
	WorkDir string
	// MaxTurns restarts the process after this many turns (0 = default 40).
	MaxTurns int
}

// WarmSession keeps one
// `claude --print --input-format stream-json --output-format stream-json --verbose`
// process alive across turns, writing each user turn as a stream-json user
// message on stdin and reading the streamed reply from stdout. Only one turn
// is in flight at a time. The process is restarted on crash, on a model or
// system-prompt change, after MaxTurns, or when Clear is called. If the
// process dies mid-turn, that turn fails and the next call cold-starts a new
// one. If a warm start itself fails (the CLI lacks the needed flags, or won't
// launch), RunTurn falls back to a single non-warm streamed call.
type WarmSession struct {
	cfg WarmSessionConfig

	mu     sync.Mutex // one turn in flight; also guards everything below
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan string
	werr   chan error
	live   bool
	turns  int
	system string
	model  string
}

func NewWarmSession(cfg WarmSessionConfig) *WarmSession { return &WarmSession{cfg: cfg} }

func (w *WarmSession) bin() string {
	if w.cfg.Bin != "" {
		return w.cfg.Bin
	}
	return "claude"
}

func (w *WarmSession) maxTurns() int {
	if w.cfg.MaxTurns > 0 {
		return w.cfg.MaxTurns
	}
	return defaultMaxTurns
}

// Clear ends the current process, if any; the next RunTurn cold-starts a
// fresh one. Used for the /clear command.
func (w *WarmSession) Clear() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.killLocked()
}

// Close ends the session for good (daemon shutdown).
func (w *WarmSession) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.killLocked()
}

func (w *WarmSession) killLocked() {
	if w.cmd != nil && w.cmd.Process != nil {
		_ = w.cmd.Cancel()
		<-w.werr
	}
	w.cmd, w.stdin, w.lines, w.werr, w.live = nil, nil, nil, nil, false
}

// start launches the subprocess with the given system prompt and model. It
// requires --print, --input-format and --output-format to be present in the
// installed CLI's --help; anything less and it refuses so the caller can fall
// back to per-turn spawning.
func (w *WarmSession) start(ctx context.Context, system, model string) error {
	path, err := exec.LookPath(w.bin())
	if err != nil {
		return err
	}
	fs, err := detectFlags(ctx, path, "--help")
	if err != nil {
		return err
	}
	if !fs["--print"] || !fs["--input-format"] || !fs["--output-format"] {
		return errors.New("claude CLI lacks warm-session flags (--print/--input-format/--output-format)")
	}
	args := []string{"--print", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose"}
	switch {
	case fs["--system-prompt"]:
		args = append(args, "--system-prompt", system)
	case fs["--append-system-prompt"]:
		args = append(args, "--append-system-prompt", system)
	default:
		return errors.New("claude CLI lacks --system-prompt/--append-system-prompt")
	}
	if !fs["--tools"] || !fs["--strict-mcp-config"] {
		return errors.New("claude CLI lacks load-bearing --tools/--strict-mcp-config flags")
	}
	args = append(args, "--tools", "", "--strict-mcp-config")
	if model != "" && fs["--model"] {
		args = append(args, "--model", model)
	}
	// The process outlives any single turn's context, so it is built with a
	// background context; containProcessGroup's Cancel is invoked manually by
	// killLocked, never by an automatic deadline.
	cmd := exec.CommandContext(context.Background(), path, args...)
	cmd.Env = ScrubbedEnv()
	if w.cfg.WorkDir != "" {
		cmd.Dir = w.cfg.WorkDir
	}
	containProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 8<<20)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	werr := make(chan error, 1)
	go func() { werr <- cmd.Wait() }()

	w.cmd, w.stdin, w.lines, w.werr = cmd, stdin, lines, werr
	w.live, w.turns, w.system, w.model = true, 0, system, model
	return nil
}

// RunTurn sends one user turn and streams the reply, reusing the live process
// when possible.
func (w *WarmSession) RunTurn(ctx context.Context, req Request, onDelta func(string)) (Response, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	needRestart := !w.live || w.system != req.System || (req.Model != "" && w.model != req.Model) || w.turns >= w.maxTurns()
	if needRestart {
		if w.live {
			w.killLocked()
		}
		if err := w.start(ctx, req.System, req.Model); err != nil {
			// Warm start failed: fall back to a single non-warm streamed call
			// rather than surfacing a warm-session-specific error.
			cold := &ClaudeSubscription{Bin: w.cfg.Bin, WorkDir: w.cfg.WorkDir, Model: req.Model}
			return cold.RunStream(ctx, req, onDelta)
		}
	}

	msg := map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": req.Prompt}}
	b, err := json.Marshal(msg)
	if err != nil {
		return Response{}, err
	}
	start := time.Now()
	if _, err := w.stdin.Write(append(b, '\n')); err != nil {
		w.live = false
		return Response{}, fmt.Errorf("warm session: write turn: %w", err)
	}

	var raw, text strings.Builder
	for {
		select {
		case line, ok := <-w.lines:
			if !ok {
				// The process is gone (stdout closed): drain its exit status
				// so a later kill of this dead handle never blocks on werr,
				// then clear the dead resources so the next call starts
				// clean rather than trying to kill an already-reaped process.
				werr := <-w.werr
				w.cmd, w.stdin, w.lines, w.werr, w.live = nil, nil, nil, nil, false
				return Response{}, fmt.Errorf("warm session ended mid-turn: %w", werr)
			}
			raw.WriteString(line)
			raw.WriteByte('\n')
			if d, ok := parseTextDelta(line); ok {
				text.WriteString(d)
				if onDelta != nil {
					onDelta(d)
				}
			}
			if res, ok := parseClaudeResult(line); ok {
				w.turns++
				resp := Response{
					Text:     strings.TrimSpace(res.Result),
					Raw:      raw.String(),
					Backend:  ClaudeSubscriptionName,
					Duration: time.Since(start),
					Model:    w.model,
				}
				if resp.Text == "" {
					resp.Text = strings.TrimSpace(text.String())
				}
				resp.InputTokens = res.Usage.InputTokens + res.Usage.CacheReadInput + res.Usage.CacheCreationInput
				resp.OutputTokens = res.Usage.OutputTokens
				resp.RateLimit = parseRateLimit(raw.String())
				if res.IsError {
					if IsRateLimitText(res.Result) {
						return resp, fmt.Errorf("%w: %s", ErrRateLimited, firstLine(res.Result))
					}
					return resp, fmt.Errorf("claude reported an error: %s", firstLine(res.Result))
				}
				return resp, nil
			}
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
	}
}
