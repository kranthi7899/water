package nervous

import "time"

// Timer is the subset of *time.Timer nervous needs, so tests can substitute
// a fake one that fires deterministically instead of waiting on the wall
// clock.
type Timer interface {
	// Stop cancels the timer. It has no effect once the timer has already
	// fired; callers don't need its bool return (unlike time.Timer.Stop,
	// nothing here ever drains a channel).
	Stop()
}

// Clock supplies the current time and a way to schedule a one-shot callback,
// so Handle's handoff-acknowledgement timer (Design §11.4 step 1/6) is
// testable without a real 250ms sleep per test.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Timer
}

// realClock is the production Clock: time.Now and time.AfterFunc.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) Timer {
	return realTimer{time.AfterFunc(d, f)}
}

type realTimer struct{ t *time.Timer }

func (r realTimer) Stop() { r.t.Stop() }

// DefaultAckAfter is how long Handle waits, from the final transcript,
// before emitting a handoff acknowledgement if the turn is still unrouted
// (Design §17: router.ack_ms default 250, must stay under 300ms).
const DefaultAckAfter = 250 * time.Millisecond
