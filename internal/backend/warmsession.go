package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"water/internal/tools"
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
	// SelfExe is the water binary used as the MCP server for tool calls
	// (defaults to os.Executable(), as ClaudeSubscription does).
	SelfExe string
	// ScratchDir holds per-session policy/log files (defaults to WorkDir/tmp).
	ScratchDir string
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

	// sem is a one-slot semaphore: one turn (or Clear/Close) in flight, and
	// it guards everything below. It is a channel rather than a sync.Mutex
	// so a caller waiting behind a hung turn can give up with its context.
	sem chan struct{}

	// turnMu guards only turnCancel, the in-flight turn's cancel func, so
	// Clear and Close can interrupt a hung turn without first taking sem.
	turnMu     sync.Mutex
	turnCancel context.CancelFunc
	closed     bool // set by Close under sem; later turns refuse

	cmd          *exec.Cmd
	stdin        io.WriteCloser
	lines        chan string
	done         chan struct{} // closed on kill, so the stdout reader never blocks on lines
	werr         chan error
	live         bool
	turns        int
	system       string
	model        string
	toolsKey     string
	toolsCleanup func()
}

// toolsKey identifies the tool-proxy scope of a request, so RunTurn can tell
// whether the running process's --mcp-config still matches. A twin's proxy
// token is minted once per daemon lifetime (not per turn — the MCP child
// this spawns is itself long-lived and reads its policy file only once), so
// in practice this almost never changes after the session's first turn.
func toolsKey(p *tools.Policy) string {
	if p == nil {
		return ""
	}
	return p.TwinSocket + "|" + p.TwinToken
}

func NewWarmSession(cfg WarmSessionConfig) *WarmSession {
	return &WarmSession{cfg: cfg, sem: make(chan struct{}, 1)}
}

// defaultWarmTimeout bounds a warm turn whose Request carries no Timeout,
// matching the cold path's runScrubbedStream default.
const defaultWarmTimeout = 5 * time.Minute

// acquire takes the session, or gives up when ctx ends first.
func (w *WarmSession) acquire(ctx context.Context) error {
	select {
	case w.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *WarmSession) release() { <-w.sem }

// interruptTurn cancels the in-flight turn, if any; that turn then kills its
// process and releases the session promptly.
func (w *WarmSession) interruptTurn() {
	w.turnMu.Lock()
	if w.turnCancel != nil {
		w.turnCancel()
	}
	w.turnMu.Unlock()
}

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
// fresh one. Used for the /clear command. An in-flight turn is interrupted
// (it fails with a cancellation error) rather than waited for, so /clear
// also recovers a hung session.
func (w *WarmSession) Clear() {
	w.interruptTurn()
	_ = w.acquire(context.Background())
	defer w.release()
	w.killLocked()
}

// Close ends the session for good (daemon shutdown): it interrupts any
// in-flight turn, kills the process group, and makes later turns refuse
// rather than start a new process.
func (w *WarmSession) Close() {
	w.interruptTurn()
	_ = w.acquire(context.Background())
	defer w.release()
	w.closed = true
	w.killLocked()
}

// killLocked tears the current process down completely: it unblocks the
// stdout reader, kills the process group (a no-op if it already exited, but
// still reaching any surviving children), reaps it if that has not happened
// yet, and removes its tool policy files. Every teardown path goes through
// here, so none can leak a process, a goroutine or a file holding the
// session's proxy token.
func (w *WarmSession) killLocked() {
	if w.done != nil {
		close(w.done)
	}
	if w.cmd != nil && w.cmd.Process != nil {
		_ = w.cmd.Cancel()
	}
	if w.werr != nil {
		<-w.werr
	}
	if w.toolsCleanup != nil {
		w.toolsCleanup()
		w.toolsCleanup = nil
	}
	w.cmd, w.stdin, w.lines, w.done, w.werr, w.live, w.toolsKey = nil, nil, nil, nil, nil, false, ""
}

func (w *WarmSession) scratch() string {
	if w.cfg.ScratchDir != "" {
		return w.cfg.ScratchDir
	}
	if w.cfg.WorkDir != "" {
		return filepath.Join(w.cfg.WorkDir, "tmp")
	}
	return os.TempDir()
}

// start launches the subprocess for req's system prompt, model and tool
// policy. It requires --print, --input-format and --output-format to be
// present in the installed CLI's --help; anything less and it refuses so the
// caller can fall back to per-turn spawning.
func (w *WarmSession) start(ctx context.Context, req Request) error {
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

	self := w.cfg.SelfExe
	if self == "" {
		self, _ = os.Executable()
	}
	mcpCfg, _, toolsCleanup, err := setupToolsFor(self, w.scratch(), req)
	if err != nil {
		return fmt.Errorf("warm session: %w", err)
	}

	args, err := buildWarmArgs(fs, req, mcpCfg)
	if err != nil {
		toolsCleanup()
		return err
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
		toolsCleanup()
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		toolsCleanup()
		return err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		toolsCleanup()
		return err
	}
	w.toolsCleanup = toolsCleanup
	w.toolsKey = toolsKey(req.Tools)
	lines := make(chan string, 64)
	done := make(chan struct{})
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 8<<20)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-done:
				return
			}
		}
	}()
	werr := make(chan error, 1)
	go func() { werr <- cmd.Wait() }()

	w.cmd, w.stdin, w.lines, w.done, w.werr = cmd, stdin, lines, done, werr
	w.live, w.turns, w.system, w.model = true, 0, req.System, req.Model
	return nil
}

// buildWarmArgs is the warm process's pure argument builder (the analogue of
// ClaudeSubscription.BuildArgs), exposed so a guard test can assert its
// isolation flags without spawning anything.
func buildWarmArgs(fs flagSet, req Request, mcpCfg string) ([]string, error) {
	args := []string{"--print", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose"}
	if fs["--include-partial-messages"] {
		args = append(args, "--include-partial-messages")
	}
	switch {
	case fs["--system-prompt"]:
		args = append(args, "--system-prompt", req.System)
	case fs["--append-system-prompt"]:
		args = append(args, "--append-system-prompt", req.System)
	default:
		return nil, errors.New("claude CLI lacks --system-prompt/--append-system-prompt")
	}
	if !fs["--tools"] || !fs["--strict-mcp-config"] {
		return nil, errors.New("claude CLI lacks load-bearing --tools/--strict-mcp-config flags")
	}
	args = append(args, "--tools", "", "--strict-mcp-config")
	args = append(args, hardeningArgs(fs)...)
	if mcpCfg != "" {
		if !fs["--mcp-config"] {
			return nil, errors.New("claude CLI lacks --mcp-config; tools unavailable")
		}
		args = append(args, "--mcp-config", mcpCfg)
		if fs["--allowedTools"] || fs["--allowed-tools"] {
			args = append(args, "--allowedTools", strings.Join(tools.AllowedToolFlags(req.Tools), ","))
		}
	}
	if req.Model != "" && fs["--model"] {
		args = append(args, "--model", req.Model)
	}
	return args, nil
}

// busyForTest reports whether a turn currently holds the session.
func (w *WarmSession) busyForTest() bool { return len(w.sem) == 1 }

// RunTurn sends one user turn and streams the reply, reusing the live process
// when possible. It honors req.Timeout (defaultWarmTimeout when zero) and
// ctx both while waiting for the session and while the turn runs. A turn
// that ends early for any reason — cancelled, timed out, write failed, or
// the process died — kills the process, because the rest of that turn's
// output would otherwise be read as the next turn's reply, and the model
// would keep working (and calling tools) on a turn nobody is waiting for.
func (w *WarmSession) RunTurn(ctx context.Context, req Request, onDelta func(string)) (Response, error) {
	if err := w.acquire(ctx); err != nil {
		return Response{}, err
	}
	defer w.release()
	if w.closed {
		return Response{}, errors.New("warm session: closed")
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultWarmTimeout
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	w.turnMu.Lock()
	w.turnCancel = cancel
	w.turnMu.Unlock()
	defer func() {
		w.turnMu.Lock()
		w.turnCancel = nil
		w.turnMu.Unlock()
	}()

	needRestart := !w.live || w.system != req.System || (req.Model != "" && w.model != req.Model) ||
		w.turns >= w.maxTurns() || w.toolsKey != toolsKey(req.Tools)
	if needRestart {
		// Unconditional: a dead process can still hold policy files and an
		// unreaped handle, and killLocked is a no-op when nothing is there.
		w.killLocked()
		// start runs on ctx, not the turn's timeout: its --help probe has its
		// own bound, and a start that failed only because a short turn
		// timeout expired would wrongly fall back to a cold call.
		if err := w.start(ctx, req); err != nil {
			// Warm start failed: fall back to a single non-warm streamed call
			// rather than surfacing a warm-session-specific error.
			cold := &ClaudeSubscription{Bin: w.cfg.Bin, WorkDir: w.cfg.WorkDir, Model: req.Model, SelfExe: w.cfg.SelfExe, ScratchDir: w.cfg.ScratchDir}
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
		w.killLocked()
		return Response{}, fmt.Errorf("warm session: write turn: %w", err)
	}

	var raw, text strings.Builder
	for {
		select {
		case line, ok := <-w.lines:
			if !ok {
				// The process is gone (stdout closed): collect its exit
				// status, then tear down the rest (policy files, any
				// surviving children) so the next call starts clean.
				werr := <-w.werr
				w.werr = nil
				w.killLocked()
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
		case <-tctx.Done():
			w.killLocked()
			if ctx.Err() == nil && errors.Is(tctx.Err(), context.DeadlineExceeded) {
				return Response{}, fmt.Errorf("%w: warm claude turn killed after %s", ErrCallTimeout, timeout)
			}
			if ctx.Err() != nil {
				return Response{}, ctx.Err()
			}
			return Response{}, fmt.Errorf("warm session: turn interrupted: %w", context.Canceled)
		}
	}
}
