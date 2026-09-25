package sidecar

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// State is the Supervisor's observable lifecycle state. Nothing wires this
// into a live circuit breaker yet (that lands with the Tier 1 adapter in
// R-18); this just makes the state queryable.
type State int

const (
	// StateIdle: New'd but never Start'ed.
	StateIdle State = iota
	// StateStarting: a subprocess has been spawned and has not yet exited.
	StateStarting
	// StateBackoff: the subprocess exited and a restart is pending.
	StateBackoff
	// StateUnhealthy: 5 or more consecutive failures. Restarts keep being
	// attempted (with backoff capped at 60s); this is only a reported
	// status for the daemon/health endpoint to surface, per
	// docs/slices/R.md's "sidecar_down" breaker reason.
	StateUnhealthy
	// StateStopped: Stop was called; the restart loop has exited for good.
	StateStopped
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateStarting:
		return "starting"
	case StateBackoff:
		return "backoff"
	case StateUnhealthy:
		return "unhealthy"
	case StateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

// unhealthyAfter is the number of consecutive subprocess failures after
// which the Supervisor reports StateUnhealthy (docs/slices/R.md §10: "After
// 5 consecutive failures the T1 breaker opens with reason sidecar_down").
const unhealthyAfter = 5

// initialBackoff and maxBackoff bound the restart backoff schedule: 1s, 2s,
// 4s, ... capped at 60s.
const (
	initialBackoff = time.Second
	maxBackoff     = 60 * time.Second
)

// healthTimeout bounds a single GET /health probe.
const healthTimeout = 2 * time.Second

// Config configures the llama-server sidecar Supervisor.
type Config struct {
	// Bin is the llama-server executable. In tests this points at a stub
	// binary (typically the test binary itself via the TestMain re-exec
	// trick) instead of a real llama-server.
	Bin string
	// ModelPath is the GGUF file passed as --model.
	ModelPath string
	// CtxSize is passed as --ctx-size. Zero means 2048 (New's default).
	CtxSize int
	// Home is the subprocess's working directory (e.g. so llama-server's
	// own logs/cache land under $WATER_HOME rather than wherever the
	// daemon happens to be running from).
	Home string
}

// Supervisor starts, health-checks, restarts and stops a single llama-server
// subprocess bound to 127.0.0.1. Config carries no host field at all, so
// there is no way to point it at a non-loopback address.
type Supervisor struct {
	cfg Config

	// sleepFn performs the restart backoff wait. It defaults to an
	// interruptible real-time wait (see newDefaultSleeper) and is
	// overridden directly by this package's own tests (same package, so
	// the unexported field is reachable) with a non-blocking recorder, so
	// backoff tests don't wait out real exponential delays.
	sleepFn func(time.Duration)

	mu                  sync.Mutex
	started             bool
	stopped             bool
	port                int
	state               State
	consecutiveFailures int
	cmd                 *exec.Cmd

	httpClient *http.Client

	stopCh chan struct{}
	done   chan struct{}
}

// New builds a Supervisor. It does not start anything.
func New(cfg Config) *Supervisor {
	if cfg.CtxSize == 0 {
		cfg.CtxSize = 2048
	}
	s := &Supervisor{
		cfg:        cfg,
		state:      StateIdle,
		httpClient: &http.Client{Timeout: healthTimeout},
		stopCh:     make(chan struct{}),
		done:       make(chan struct{}),
	}
	s.sleepFn = s.defaultSleep
	return s
}

// defaultSleep waits for d, or returns early the moment Stop is called, so a
// real Supervisor's Stop never has to wait out an in-progress backoff delay
// (up to 60s) before returning.
func (s *Supervisor) defaultSleep(d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-s.stopCh:
	}
}

// Start picks a free 127.0.0.1 port, spawns llama-server against it, and
// launches a background loop that restarts the process with exponential
// backoff (1s, 2s, 4s, ... capped at 60s) whenever it exits, until Stop is
// called. It returns once the first spawn attempt has been scheduled; it
// does not wait for the process to become healthy (use Healthy for that).
func (s *Supervisor) Start(ctx context.Context) error {
	port, addr, err := freePort()
	if err != nil {
		return fmt.Errorf("pick free port: %w", err)
	}
	if addr != "127.0.0.1" {
		// Unreachable in practice: freePort always binds 127.0.0.1. Kept as
		// a hard invariant check rather than a silent trust.
		return fmt.Errorf("internal error: picked non-loopback address %q", addr)
	}

	s.mu.Lock()
	s.port = port
	s.state = StateStarting
	s.started = true
	s.mu.Unlock()

	go s.runLoop(ctx, port)
	return nil
}

// runLoop owns the spawn/wait/backoff cycle for one Supervisor. Every check
// of the stop flag and every mutation of shared state happens under s.mu, so
// there is no window in which Stop can miss a just-spawned process: either
// Stop observes the process (and kills it) or runLoop observes stopped and
// never spawns it.
func (s *Supervisor) runLoop(ctx context.Context, port int) {
	defer close(s.done)
	backoff := initialBackoff

	for {
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()

		// cmd.Start() runs without s.mu held (spawning a process is not
		// instantaneous, and cmd.Process must not be published to s.cmd
		// until Start() has returned — publishing it earlier would let
		// Stop, on another goroutine, read cmd.Process while os/exec is
		// still concurrently initializing it).
		cmd := exec.CommandContext(ctx, s.cfg.Bin, buildArgs(s.cfg, port)...)
		cmd.Dir = s.cfg.Home
		startErr := cmd.Start()

		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			// Stop ran while we were spawning and never saw this process:
			// clean it up ourselves instead of leaking it.
			if startErr == nil && cmd.Process != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
			return
		}
		s.cmd = cmd
		s.state = StateStarting
		s.mu.Unlock()

		if startErr == nil {
			_ = cmd.Wait() // returns when the process exits, for any reason
		}

		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return
		}
		s.consecutiveFailures++
		failures := s.consecutiveFailures
		if failures >= unhealthyAfter {
			s.state = StateUnhealthy
		} else {
			s.state = StateBackoff
		}
		s.mu.Unlock()

		s.sleepFn(backoff)
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// Endpoint returns "http://127.0.0.1:<port>", or "" before Start has been
// called.
func (s *Supervisor) Endpoint() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.port == 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d", s.port)
}

// Healthy performs a live GET /health against the sidecar and reports
// whether it answered 200 OK. It does not consult or reset the restart
// backoff state; those are tracked independently by the restart loop.
func (s *Supervisor) Healthy(ctx context.Context) bool {
	endpoint := s.Endpoint()
	if endpoint == "" {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// State reports the Supervisor's current lifecycle state.
func (s *Supervisor) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// ConsecutiveFailures reports how many restart attempts in a row have ended
// in the subprocess exiting. There is no success-resets-the-counter wiring
// yet; that is part of the live circuit-breaker work in a later task.
func (s *Supervisor) ConsecutiveFailures() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.consecutiveFailures
}

// Stop ends the restart loop and kills any subprocess currently running,
// then waits for the restart loop to exit. It is safe to call more than
// once (concurrently or not) and safe to call before Start.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	if s.stopped {
		started := s.started
		s.mu.Unlock()
		if started {
			<-s.done
		}
		return
	}
	s.stopped = true
	cmd := s.cmd
	started := s.started
	s.mu.Unlock()

	close(s.stopCh) // unblocks defaultSleep immediately if a backoff wait is in progress
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	if started {
		<-s.done
	}

	s.mu.Lock()
	s.state = StateStopped
	s.mu.Unlock()
}

// buildArgs builds the llama-server argument list. --host is always the
// literal "127.0.0.1": Config has no field that could route this anywhere
// else.
func buildArgs(cfg Config, port int) []string {
	return []string{
		"--model", cfg.ModelPath,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--ctx-size", strconv.Itoa(cfg.CtxSize),
		"--jinja",
		"--no-webui",
		"--parallel", "2",
	}
}

// freePort asks the OS for a free port on 127.0.0.1 and returns it along
// with the address it was picked on (always "127.0.0.1"). There is an
// inherent, accepted TOCTOU gap between closing this listener and the
// subprocess binding the same port; both stay on loopback throughout.
func freePort() (port int, addr string, err error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, "", err
	}
	defer l.Close()
	tcpAddr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, "", fmt.Errorf("unexpected listener address type %T", l.Addr())
	}
	return tcpAddr.Port, tcpAddr.IP.String(), nil
}
