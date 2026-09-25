package sidecar

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// durationRecorder is a thread-safe recorder for the durations a stubbed
// sleepFn is called with. It exists because sleepFn runs on the
// Supervisor's own restart-loop goroutine while the test goroutine reads
// the recorded values concurrently (e.g. inside waitFor).
type durationRecorder struct {
	mu   sync.Mutex
	vals []time.Duration
}

func (r *durationRecorder) add(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.vals = append(r.vals, d)
}

func (r *durationRecorder) snapshot() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]time.Duration, len(r.vals))
	copy(out, r.vals)
	return out
}

func (r *durationRecorder) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.vals)
}

// TestMain implements the re-exec trick: when invoked with
// WATER_SIDECAR_FAKE=1 set, this same test binary behaves as a fake
// llama-server instead of running the test suite. Config.Bin is pointed at
// os.Args[0] in tests below, so Supervisor spawns *this binary* rather than
// a real llama-server.
func TestMain(m *testing.M) {
	if os.Getenv("WATER_SIDECAR_FAKE") == "1" {
		runFakeServer()
		return
	}
	os.Exit(m.Run())
}

// runFakeServer stands in for llama-server. It parses --host/--port the same
// way Supervisor invokes the real binary, then either serves /health or
// exits immediately, depending on WATER_SIDECAR_FAKE_MODE:
//   - "healthy" (default): serves 200 on /health until killed.
//   - "unhealthy": listens, but /health always returns 500.
//   - "crash": exits immediately with a non-zero status.
func runFakeServer() {
	fs := flag.NewFlagSet("fake-llama-server", flag.ExitOnError)
	host := fs.String("host", "", "")
	port := fs.String("port", "", "")
	_ = fs.String("model", "", "")
	_ = fs.Int("ctx-size", 0, "")
	_ = fs.Bool("jinja", false, "")
	_ = fs.Bool("no-webui", false, "")
	_ = fs.Int("parallel", 0, "")
	_ = fs.Parse(os.Args[1:])

	mode := os.Getenv("WATER_SIDECAR_FAKE_MODE")
	if mode == "crash" {
		os.Exit(1)
	}
	if *host != "127.0.0.1" {
		fmt.Fprintln(os.Stderr, "fake server: refusing non-loopback host", *host)
		os.Exit(2)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if mode == "unhealthy" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Addr: net.JoinHostPort(*host, *port), Handler: mux}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "fake server:", err)
		os.Exit(3)
	}
}

func fakeConfig(t *testing.T, mode string) Config {
	t.Helper()
	t.Setenv("WATER_SIDECAR_FAKE", "1")
	t.Setenv("WATER_SIDECAR_FAKE_MODE", mode)
	return Config{
		Bin:       os.Args[0],
		ModelPath: "/dev/null",
		CtxSize:   2048,
		Home:      t.TempDir(),
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func TestSupervisorStartBecomesHealthy(t *testing.T) {
	cfg := fakeConfig(t, "healthy")
	s := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	if !waitFor(t, 3*time.Second, func() bool { return s.Healthy(context.Background()) }) {
		t.Fatal("supervisor never became healthy")
	}

	if got := s.Endpoint(); got == "" {
		t.Fatal("Endpoint returned empty string once running")
	}
}

func TestSupervisorEndpointIsLoopbackOnly(t *testing.T) {
	cfg := fakeConfig(t, "healthy")
	s := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	waitFor(t, 3*time.Second, func() bool { return s.Healthy(context.Background()) })

	endpoint := s.Endpoint()
	if endpoint[:len("http://127.0.0.1:")] != "http://127.0.0.1:" {
		t.Fatalf("Endpoint() = %q, want a http://127.0.0.1:<port> endpoint", endpoint)
	}
}

func TestSupervisorHealthyFalseBeforeStart(t *testing.T) {
	cfg := fakeConfig(t, "healthy")
	s := New(cfg)
	if s.Healthy(context.Background()) {
		t.Fatal("expected Healthy() to be false before Start")
	}
	if s.Endpoint() != "" {
		t.Fatalf("expected empty Endpoint() before Start, got %q", s.Endpoint())
	}
}

func TestSupervisorUnhealthyServerReportsUnhealthy(t *testing.T) {
	cfg := fakeConfig(t, "unhealthy")
	s := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	// Give the process a moment to bind, then confirm Healthy stays false.
	time.Sleep(200 * time.Millisecond)
	if s.Healthy(context.Background()) {
		t.Fatal("expected Healthy() to be false against a server returning 500")
	}
}

func TestSupervisorRestartBackoffOnCrash(t *testing.T) {
	cfg := fakeConfig(t, "crash")

	s := New(cfg)
	// Replace the real (interruptible) sleep with a recording stub so the
	// test doesn't wait out real exponential backoff (up to 1+2+4+8+16 =
	// 31s). sleepFn is an unexported field on the same-package Supervisor,
	// not part of the public Config surface; set before Start so runLoop
	// (launched by Start via `go`) always observes it.
	recorded := &durationRecorder{}
	s.sleepFn = recorded.add

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	if !waitFor(t, 5*time.Second, func() bool { return s.State() == StateUnhealthy }) {
		t.Fatalf("expected state to become unhealthy after repeated crashes, last state=%v recorded=%v", s.State(), recorded.snapshot())
	}

	got := recorded.snapshot()
	if len(got) < 5 {
		t.Fatalf("expected at least 5 backoff waits before going unhealthy, got %d: %v", len(got), got)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("backoff[%d] = %v, want %v (full sequence: %v)", i, got[i], w, got)
		}
	}

	if failures := s.ConsecutiveFailures(); failures < 5 {
		t.Fatalf("ConsecutiveFailures() = %d, want >= 5", failures)
	}
}

func TestSupervisorBackoffCapsAtSixtySeconds(t *testing.T) {
	cfg := fakeConfig(t, "crash")
	s := New(cfg)
	recorded := &durationRecorder{}
	s.sleepFn = recorded.add

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	if !waitFor(t, 5*time.Second, func() bool { return recorded.len() >= 8 }) {
		got := recorded.snapshot()
		t.Fatalf("expected at least 8 backoff waits, got %d: %v", len(got), got)
	}
	got := recorded.snapshot()
	for i := 6; i < 8 && i < len(got); i++ {
		if got[i] != 60*time.Second {
			t.Fatalf("backoff[%d] = %v, want capped at 60s (full sequence: %v)", i, got[i], got)
		}
	}
}

func TestSupervisorStopEndsRestartLoop(t *testing.T) {
	cfg := fakeConfig(t, "crash")
	s := New(cfg)
	recorded := &durationRecorder{}
	s.sleepFn = recorded.add

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool { return recorded.len() >= 2 })
	s.Stop()
	countAtStop := recorded.len()

	time.Sleep(200 * time.Millisecond)
	if got := recorded.len(); got != countAtStop {
		t.Fatalf("restart loop kept running after Stop: count went from %d to %d", countAtStop, got)
	}
	if s.Healthy(context.Background()) {
		t.Fatal("expected Healthy() to be false after Stop")
	}
}

// TestSupervisorNeverBindsNonLoopback checks, structurally, that the
// argument list Supervisor builds for llama-server always pins --host to
// 127.0.0.1 regardless of Config's contents (Config has no Host field at
// all, so there is no way to plumb an arbitrary host through it), and that
// the free-port picker itself only ever listens on 127.0.0.1.
func TestSupervisorNeverBindsNonLoopback(t *testing.T) {
	configs := []Config{
		{Bin: "whatever", ModelPath: "/m1", CtxSize: 1024, Home: "/tmp/a"},
		{Bin: "other", ModelPath: "/m2", CtxSize: 4096, Home: "/tmp/b"},
	}
	for _, cfg := range configs {
		args := buildArgs(cfg, 12345)
		found := false
		for i, a := range args {
			if a == "--host" {
				found = true
				if i+1 >= len(args) || args[i+1] != "127.0.0.1" {
					t.Fatalf("--host arg = %v, want 127.0.0.1 (args=%v)", args, args)
				}
			}
		}
		if !found {
			t.Fatalf("no --host flag in args: %v", args)
		}
	}

	port, addr, err := freePort()
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	if addr != "127.0.0.1" {
		t.Fatalf("freePort address = %q, want 127.0.0.1", addr)
	}
	if port <= 0 {
		t.Fatalf("freePort port = %d, want > 0", port)
	}
}

func TestBuildArgsIncludesFixedFlags(t *testing.T) {
	cfg := Config{Bin: "x", ModelPath: "/models/functiongemma.gguf", CtxSize: 2048, Home: "/home"}
	args := buildArgs(cfg, 55123)
	want := []string{
		"--model", "/models/functiongemma.gguf",
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(55123),
		"--ctx-size", "2048",
		"--jinja",
		"--no-webui",
		"--parallel", "2",
	}
	if len(args) != len(want) {
		t.Fatalf("buildArgs = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("buildArgs[%d] = %q, want %q (full: %v)", i, args[i], want[i], args)
		}
	}
}
