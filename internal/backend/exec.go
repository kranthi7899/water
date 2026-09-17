package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// flagSet is the set of long flags a CLI advertises in --help. Headless flags
// drift between releases, so subscription backends parse --help at runtime
// rather than hardcoding flag names.
type flagSet map[string]bool

var longFlagRe = regexp.MustCompile(`--[a-zA-Z][a-zA-Z0-9-]*`)

func parseFlags(help string) flagSet {
	fs := flagSet{}
	for _, m := range longFlagRe.FindAllString(help, -1) {
		fs[m] = true
	}
	return fs
}

// helpCache memoises --help parsing per binary+args for the process lifetime.
var helpCache sync.Map

func detectFlags(ctx context.Context, bin string, args ...string) (flagSet, error) {
	key := bin + " " + strings.Join(args, " ")
	if v, ok := helpCache.Load(key); ok {
		return v.(flagSet), nil
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, args...)
	cmd.Env = ScrubbedEnv()
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("%s %s: %w", bin, strings.Join(args, " "), err)
	}
	fs := parseFlags(string(out))
	helpCache.Store(key, fs)
	return fs, nil
}

// ErrCallTimeout marks a subprocess killed because its per-call deadline
// passed. Callers name the setting that controls it.
var ErrCallTimeout = errors.New("model call timed out")

// Debug, when non-nil, receives one line when each subprocess starts and one
// when it exits (--debug). Prompt-bearing arguments are replaced by their
// size, so the log shows the real flags (for example --strict-mcp-config)
// without dumping persona or memory text.
var Debug io.Writer

// InFlightCall describes a subprocess that has started and not yet exited.
type InFlightCall struct {
	PID     int       `json:"pid"`
	Bin     string    `json:"bin"`
	Args    string    `json:"args"`
	Started time.Time `json:"started"`
}

var inflight sync.Map // *exec.Cmd -> InFlightCall

// InFlight lists subprocesses currently running, oldest first. Used by the
// live state dump to show which model call a stuck run is waiting on.
func InFlight() []InFlightCall {
	var out []InFlightCall
	inflight.Range(func(_, v any) bool {
		out = append(out, v.(InFlightCall))
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// redactArgs replaces values that carry prompt text with their byte size.
func redactArgs(args []string) string {
	var out []string
	secret := map[string]bool{"--system-prompt": true, "--append-system-prompt": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case secret[a] && i+1 < len(args):
			out = append(out, a, fmt.Sprintf("<%d bytes>", len(args[i+1])))
			i++
		case a == "--" && i+1 < len(args):
			out = append(out, a, fmt.Sprintf("<prompt %d bytes>", len(strings.Join(args[i+1:], " "))))
			i = len(args)
		case a == "":
			out = append(out, `""`)
		default:
			out = append(out, a)
		}
	}
	return strings.Join(out, " ")
}

// runScrubbed executes a subprocess with metered credentials stripped from its
// environment. dir is the working directory ("" = inherit).
func runScrubbed(ctx context.Context, timeout time.Duration, dir string, stdin string, bin string, args ...string) (stdout, stderr string, err error) {
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
	var ob, eb bytes.Buffer
	cmd.Stdout = &ob
	cmd.Stderr = &eb
	shown := redactArgs(args)
	start := time.Now()
	if err = cmd.Start(); err != nil {
		return "", "", err
	}
	inflight.Store(cmd, InFlightCall{PID: cmd.Process.Pid, Bin: bin, Args: shown, Started: start})
	if Debug != nil {
		fmt.Fprintf(Debug, "[debug] exec pid=%d %s %s (cwd=%s, timeout=%s)\n", cmd.Process.Pid, bin, shown, dir, timeout)
	}
	err = cmd.Wait()
	inflight.Delete(cmd)
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("%w: %s killed after %s", ErrCallTimeout, bin, timeout)
	}
	if Debug != nil {
		fmt.Fprintf(Debug, "[debug] exit pid=%d after %s err=%v stdout=%dB stderr=%dB last-stderr=%q\n",
			cmd.Process.Pid, time.Since(start).Round(time.Millisecond), err, ob.Len(), eb.Len(), ErrorSummary(eb.String(), ""))
	}
	return ob.String(), eb.String(), err
}

// errorNoise are CLI chatter lines that never explain a failure.
var errorNoise = []string{"reading additional input from stdin", "openai codex v", "--------"}

// ErrorSummary picks the line of subprocess output most likely to explain a
// failure: the last line that looks like an error (with a JSON error message
// unwrapped), else the last meaningful line. It replaces "first line", which
// for codex is a banner and for stream-json is the init event.
func ErrorSummary(stderr, stdout string) string {
	lines := strings.Split(strings.TrimSpace(stderr+"\n"+stdout), "\n")
	meaningful := func(l string) bool {
		t := strings.ToLower(strings.TrimSpace(l))
		if t == "" {
			return false
		}
		for _, n := range errorNoise {
			if strings.HasPrefix(t, n) {
				return false
			}
		}
		return !strings.HasPrefix(t, `{"type":"system"`)
	}
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		low := strings.ToLower(l)
		if !meaningful(l) || !(strings.HasPrefix(low, "error") || strings.Contains(low, `"error"`) || strings.Contains(low, "error:")) {
			continue
		}
		if j := strings.Index(l, "{"); j >= 0 {
			var v struct {
				Message string `json:"message"`
				Error   struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(l[j:]), &v) == nil {
				if v.Error.Message != "" {
					return v.Error.Message
				}
				if v.Message != "" {
					return v.Message
				}
			}
		}
		return l
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if meaningful(lines[i]) {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
