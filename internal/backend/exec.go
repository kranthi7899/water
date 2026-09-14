package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
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
	var ob, eb bytes.Buffer
	cmd.Stdout = &ob
	cmd.Stderr = &eb
	err = cmd.Run()
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("%s timed out after %s", bin, timeout)
	}
	return ob.String(), eb.String(), err
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
