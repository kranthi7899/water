package research

import (
	"sync"
	"time"
)

// Warm spare (docs/slices/W.md §15). A cold `claude --print` pays for the
// CLI's own start-up (Node, settings, auth) before the model sees the
// question. The research process's arguments do not depend on the query
// (the question goes in on stdin as one stream-json message), so the next
// process can be started ahead of time and left blocked on stdin: with
// --input-format stream-json it waits for its first message indefinitely,
// does nothing (no model call) until it gets one, and exits by itself on
// EOF. The daemon holds the only write end of that stdin, so if the daemon
// dies for any reason the spare reads EOF and exits too.
//
// The pool holds at most ONE spare. Every call takes it (atomically, under
// the pool's lock) or, when there is none or it is unusable, starts a cold
// process of its own; a used process is never reused. A second call that
// arrives while the first holds the spare simply runs cold. After each take
// the pool starts one replacement in the background (never while another is
// already starting, never once closed). A spare idle for MaxSpareIdle is
// recycled (killed, and replaced only while research was used within
// KeepWarmFor), and Shutdown kills it for good.

const (
	// MaxSpareIdle is how long a spare may sit unused before it is recycled,
	// so a long-idle process never answers with a stale login or an
	// upgraded CLI underneath it.
	MaxSpareIdle = 5 * time.Minute
	// KeepWarmFor is how long after the last research call recycled spares
	// are still replaced. After that the pool goes quiet (no process at all)
	// until the next call, which runs cold and warms the pool again.
	KeepWarmFor = 30 * time.Minute
)

// warmable is what the pool holds: a started research process waiting on
// stdin. *proc is the real one; tests use a fake.
type warmable interface {
	key() string     // the exact binary+arguments it was started with
	born() time.Time // when it was started
	alive() bool     // false once the process has exited
	kill()           // idempotent: kill the group, reap, clean up
}

// pool keeps at most one warm spare. The zero value is not usable; see
// newPool.
type pool struct {
	mu       sync.Mutex
	spare    warmable
	timer    *time.Timer
	spawning bool
	closed   bool
	lastUse  time.Time

	start    func(key string) (warmable, error)
	now      func() time.Time
	maxIdle  time.Duration
	keepWarm time.Duration
}

func newPool(start func(key string) (warmable, error)) *pool {
	return &pool{start: start, now: time.Now, maxIdle: MaxSpareIdle, keepWarm: KeepWarmFor}
}

// take hands the spare to exactly one caller when it was started with key,
// is still running and is younger than maxIdle; otherwise it returns nil
// (the caller runs cold) and disposes of an unusable spare. It also counts
// as a use for KeepWarmFor.
func (pl *pool) take(key string) warmable {
	pl.mu.Lock()
	pl.lastUse = pl.now()
	s := pl.spare
	pl.spare = nil
	if pl.timer != nil {
		pl.timer.Stop()
		pl.timer = nil
	}
	now := pl.now()
	pl.mu.Unlock()
	if s == nil {
		return nil
	}
	if s.key() != key || !s.alive() || now.Sub(s.born()) >= pl.maxIdle {
		go s.kill()
		return nil
	}
	return s
}

// refill starts one replacement spare for key in the background, unless the
// pool is closed, already holds a spare, or is already starting one. It
// never blocks the caller.
func (pl *pool) refill(key string) {
	pl.mu.Lock()
	if pl.closed || pl.spare != nil || pl.spawning {
		pl.mu.Unlock()
		return
	}
	pl.spawning = true
	pl.mu.Unlock()
	go func() {
		p, err := pl.start(key)
		pl.mu.Lock()
		pl.spawning = false
		if err != nil {
			pl.mu.Unlock()
			return
		}
		if pl.closed || pl.spare != nil {
			pl.mu.Unlock()
			p.kill()
			return
		}
		pl.spare = p
		pl.timer = time.AfterFunc(pl.maxIdle, func() { pl.expire(p) })
		pl.mu.Unlock()
	}()
}

// expire recycles p if it is still the idle spare: it is killed, and a
// fresh one is started only while research was used within keepWarm.
func (pl *pool) expire(p warmable) {
	pl.mu.Lock()
	if pl.spare != p {
		pl.mu.Unlock()
		return
	}
	pl.spare = nil
	pl.timer = nil
	recent := pl.now().Sub(pl.lastUse) < pl.keepWarm
	pl.mu.Unlock()
	p.kill()
	if recent {
		pl.refill(p.key())
	}
}

// close kills the spare and stops the pool for good (daemon shutdown).
// Calls after it still work, cold, but never leave a spare behind.
func (pl *pool) close() {
	pl.mu.Lock()
	pl.closed = true
	s := pl.spare
	pl.spare = nil
	if pl.timer != nil {
		pl.timer.Stop()
		pl.timer = nil
	}
	pl.mu.Unlock()
	if s != nil {
		s.kill()
	}
}

// spareCount reports whether a spare is currently held (tests only).
func (pl *pool) spareCount() int {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if pl.spare != nil {
		return 1
	}
	return 0
}

// warm is the process-wide research pool: one spare for the whole daemon.
var warm = newPool(startSpare)

// Shutdown kills the warm spare and stops keeping one (call it on daemon
// shutdown). It is safe to call more than once. Even without it the spare
// exits by itself when the daemon's process ends (its stdin reaches EOF).
func Shutdown() { warm.close() }
