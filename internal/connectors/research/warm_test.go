package research

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeProc is a warmable with no process behind it.
type fakeProc struct {
	k      string
	t      time.Time
	dead   atomic.Bool
	killed atomic.Int32
}

func (f *fakeProc) key() string     { return f.k }
func (f *fakeProc) born() time.Time { return f.t }
func (f *fakeProc) alive() bool     { return !f.dead.Load() }
func (f *fakeProc) kill()           { f.killed.Add(1) }

// fakeStarts is a pool start func that records every process it starts.
type fakeStarts struct {
	mu    sync.Mutex
	procs []*fakeProc
	now   func() time.Time
	gate  chan struct{} // when non-nil, each start waits on it
	fail  bool
}

func (fs *fakeStarts) start(key string) (warmable, error) {
	if fs.gate != nil {
		<-fs.gate
	}
	if fs.fail {
		return nil, errors.New("spawn failed")
	}
	p := &fakeProc{k: key, t: fs.now()}
	fs.mu.Lock()
	fs.procs = append(fs.procs, p)
	fs.mu.Unlock()
	return p, nil
}

func (fs *fakeStarts) count() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return len(fs.procs)
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func newTestPool() (*pool, *fakeStarts, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	fs := &fakeStarts{now: clk.now}
	pl := newPool(fs.start)
	pl.now = clk.now
	return pl, fs, clk
}

// TestPoolEmptyTakeIsColdThenRefills: the first call finds no spare (runs
// cold), and refill leaves exactly one spare, which the next call gets.
func TestPoolEmptyTakeIsColdThenRefills(t *testing.T) {
	pl, fs, _ := newTestPool()
	if pl.take("k") != nil {
		t.Fatal("empty pool handed out a spare")
	}
	pl.refill("k")
	waitFor(t, "a spare", func() bool { return pl.spareCount() == 1 })
	got := pl.take("k")
	if got == nil || got != warmable(fs.procs[0]) {
		t.Fatalf("take = %v, want the spare", got)
	}
	if pl.spareCount() != 0 {
		t.Fatal("spare still held after take")
	}
}

// TestPoolConcurrentTakesShareOneSpare: many concurrent calls, one spare:
// exactly one gets it, the rest run cold (nil), and it is never handed out
// twice.
func TestPoolConcurrentTakesShareOneSpare(t *testing.T) {
	pl, _, _ := newTestPool()
	pl.refill("k")
	waitFor(t, "a spare", func() bool { return pl.spareCount() == 1 })
	var got atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if pl.take("k") != nil {
				got.Add(1)
			}
		}()
	}
	wg.Wait()
	if got.Load() != 1 {
		t.Fatalf("%d callers got the spare, want exactly 1", got.Load())
	}
}

// TestPoolNeverMoreThanOneSpare: concurrent refills start one process, not
// one each, and a refill while a spare is held starts nothing.
func TestPoolNeverMoreThanOneSpare(t *testing.T) {
	pl, fs, _ := newTestPool()
	fs.gate = make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); pl.refill("k") }()
	}
	wg.Wait()
	close(fs.gate)
	waitFor(t, "a spare", func() bool { return pl.spareCount() == 1 })
	pl.refill("k")
	time.Sleep(20 * time.Millisecond)
	if n := fs.count(); n != 1 {
		t.Fatalf("started %d processes, want 1", n)
	}
}

// TestPoolDiscardsUnusableSpare: a spare started with other arguments (a
// model change), one that has exited, or one older than maxIdle is killed
// and the caller runs cold.
func TestPoolDiscardsUnusableSpare(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  func(p *fakeProc, clk *fakeClock)
		key  string
	}{
		{"other key", func(*fakeProc, *fakeClock) {}, "other"},
		{"exited", func(p *fakeProc, _ *fakeClock) { p.dead.Store(true) }, "k"},
		{"too old", func(_ *fakeProc, clk *fakeClock) { clk.add(MaxSpareIdle) }, "k"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pl, fs, clk := newTestPool()
			pl.refill("k")
			waitFor(t, "a spare", func() bool { return pl.spareCount() == 1 })
			p := fs.procs[0]
			tc.mod(p, clk)
			if pl.take(tc.key) != nil {
				t.Fatal("unusable spare handed out")
			}
			waitFor(t, "the spare killed", func() bool { return p.killed.Load() == 1 })
		})
	}
}

// TestPoolExpireRecycles: an idle spare is killed at maxIdle and replaced
// while research was used recently, and not replaced once it was not.
func TestPoolExpireRecycles(t *testing.T) {
	pl, fs, clk := newTestPool()
	pl.maxIdle = 10 * time.Millisecond
	pl.take("k") // a use
	pl.refill("k")
	waitFor(t, "the recycled replacement", func() bool { return fs.count() >= 2 && pl.spareCount() == 1 })
	if fs.procs[0].killed.Load() != 1 {
		t.Fatal("expired spare not killed")
	}
	// No use for longer than keepWarm: the next expiry leaves the pool empty.
	clk.add(KeepWarmFor + time.Minute)
	waitFor(t, "the pool to go quiet", func() bool { return pl.spareCount() == 0 })
	n := fs.count()
	time.Sleep(50 * time.Millisecond)
	if fs.count() != n || pl.spareCount() != 0 {
		t.Fatal("pool kept re-spawning with no recent use")
	}
}

// TestPoolCloseKillsAndStops: close kills the spare, and neither a later
// refill nor a start already in flight leaves one behind.
func TestPoolCloseKillsAndStops(t *testing.T) {
	pl, fs, _ := newTestPool()
	pl.refill("k")
	waitFor(t, "a spare", func() bool { return pl.spareCount() == 1 })
	p := fs.procs[0]
	pl.take("k") // taken by a call...
	fs.gate = make(chan struct{})
	pl.refill("k") // ...whose replacement is starting when the daemon stops
	pl.close()
	close(fs.gate)
	waitFor(t, "the in-flight start killed", func() bool { return fs.count() == 2 && fs.procs[1].killed.Load() == 1 })
	if pl.spareCount() != 0 {
		t.Fatal("spare left after close")
	}
	pl.refill("k")
	time.Sleep(20 * time.Millisecond)
	if fs.count() != 2 || pl.spareCount() != 0 {
		t.Fatal("refill after close started a process")
	}
	_ = p
	// close is idempotent.
	pl.close()
}

// TestPoolSpawnFailureLeavesNoSpare: a failed start leaves the pool empty
// and able to try again.
func TestPoolSpawnFailureLeavesNoSpare(t *testing.T) {
	pl, fs, _ := newTestPool()
	fs.fail = true
	pl.refill("k")
	time.Sleep(20 * time.Millisecond)
	if pl.spareCount() != 0 {
		t.Fatal("spare after failed start")
	}
	fs.fail = false
	pl.refill("k")
	waitFor(t, "a spare", func() bool { return pl.spareCount() == 1 })
}

// TestTrackerPhases: the tracker times the first output, the first tool
// call, each WebSearch/WebFetch (by tool_use id), and the answer turn.
func TestTrackerPhases(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	tr := newTracker(t0)
	tr.ready = at(100)
	lines := []struct {
		ms   int
		line string
	}{
		{300, `{"type":"system","subtype":"init"}`},
		{400, `{"type":"user","message":{"role":"user","content":"Question: x"}}`},
		{1500, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"s1","name":"WebSearch","input":{}}]}}`},
		{4500, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"s1","content":"links"}]}}`},
		{5000, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"f1","name":"WebFetch","input":{}}]}}`},
		{7000, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"f1","content":"page"}]}}`},
		{7100, `not json`},
		{7200, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"unknown"}]}}`},
	}
	for _, l := range lines {
		if tr.line(at(l.ms), []byte(l.line)) {
			t.Fatalf("done early at %q", l.line)
		}
	}
	if !tr.line(at(9000), []byte(`{"type":"result","subtype":"success","is_error":false,"num_turns":3,"result":"ok"}`)) {
		t.Fatal("result not recognised")
	}
	got := tr.timing(true, at(9050))
	want := Timing{Warm: true, SetupMS: 100, FirstOutputMS: 300, FirstToolMS: 1500, SearchMS: 3000, Searches: 1,
		FetchMS: 2000, Fetches: 1, AnswerMS: 2050, TotalMS: 9050, Turns: 3}
	if got != want {
		t.Fatalf("timing = %+v\nwant     %+v", got, want)
	}
	ph := got.Phases()
	if ph["research_warm"] != 1 || ph["research_search"] != 3000 || ph["research_total"] != 9050 || ph["research_fetches"] != 1 {
		t.Fatalf("phases = %v", ph)
	}
}

// fakeCLI is a /bin/sh script standing in for `claude --print
// --input-format stream-json`: it waits for one stdin line, then prints a
// stream with one WebSearch and a result, then waits for EOF.
const fakeCLI = `read line
case "$line" in *'"type":"user"'*) ;; *) echo "bad input" >&2; exit 3;; esac
echo '{"type":"system","subtype":"init"}'
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"s1","name":"WebSearch","input":{}}]}}'
sleep 0.05
echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"s1","content":"x"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"num_turns":2,"result":"Dublin: 14C.\n{\"sources\":[{\"title\":\"Met\",\"url\":\"https://www.met.ie/\"}]}"}'
cat >/dev/null
`

func requireSh(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
}

// TestRunReadsAnswerAndTiming: a started process is given the question on
// stdin and read to its result; the answer and its timing come back, and
// the process and its scratch directory are gone after kill.
func TestRunReadsAnswerAndTiming(t *testing.T) {
	requireSh(t)
	p, err := startProc(procKey("/bin/sh", []string{"-c", fakeCLI}))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond) // a warm spare sits idle first
	if !p.alive() {
		t.Fatal("spare exited while waiting for its question")
	}
	ans, err := run(context.Background(), p, true, time.Now(), "weather in Dublin", 3)
	p.kill()
	if err != nil {
		t.Fatal(err)
	}
	if ans.Summary != "Dublin: 14C." || len(ans.Sources) != 1 || ans.Timing == nil {
		t.Fatalf("answer = %+v", ans)
	}
	if !ans.Timing.Warm || ans.Timing.Searches != 1 || ans.Timing.SearchMS < 40 || ans.Timing.Turns != 2 || ans.Timing.TotalMS <= 0 {
		t.Fatalf("timing = %+v", *ans.Timing)
	}
	if p.alive() {
		t.Fatal("process alive after kill")
	}
	if _, err := os.Stat(p.dir); !os.IsNotExist(err) {
		t.Fatalf("scratch dir left behind: %v", err)
	}
}

// TestSpareExitsWhenStdinCloses stands in for the daemon dying: the spare's
// only stdin writer goes away, it reads EOF and exits on its own.
func TestSpareExitsWhenStdinCloses(t *testing.T) {
	requireSh(t)
	p, err := startProc(procKey("/bin/sh", []string{"-c", "cat >/dev/null"}))
	if err != nil {
		t.Fatal(err)
	}
	defer p.kill()
	_ = p.stdin.Close()
	select {
	case <-p.exited:
	case <-time.After(3 * time.Second):
		t.Fatal("spare did not exit on stdin EOF")
	}
}

// TestRunTimeoutKillsProcessGroup: when the call's context ends, the whole
// process group is killed (a child that ignores EOF included) and the call
// returns the context's error.
func TestRunTimeoutKillsProcessGroup(t *testing.T) {
	requireSh(t)
	p, err := startProc(procKey("/bin/sh", []string{"-c", "read line; sleep 30 & wait"}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = run(ctx, p, false, start, "q q q", 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second || p.alive() {
		t.Fatal("process group not killed promptly")
	}
}

// TestRunProcessExitIsAnError: a process that dies without a result is an
// error carrying its stderr, not a hang.
func TestRunProcessExitIsAnError(t *testing.T) {
	requireSh(t)
	p, err := startProc(procKey("/bin/sh", []string{"-c", "read line; echo 'Error: not logged in' >&2; exit 1"}))
	if err != nil {
		t.Fatal(err)
	}
	defer p.kill()
	_, err = run(context.Background(), p, false, time.Now(), "q q q", 1)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("err = %v", err)
	}
}

// TestProcKeyRoundTrip: the key carries the exact arguments, empty ones
// (--setting-sources "") included.
func TestProcKeyRoundTrip(t *testing.T) {
	args := []string{"--print", "--setting-sources", "", "--model", "haiku"}
	k := procKey("/x/claude", args)
	parts := strings.Split(k, "\x00")
	if parts[0] != "/x/claude" || strings.Join(parts[1:], "|") != strings.Join(args, "|") {
		t.Fatalf("round trip = %q", parts)
	}
}
