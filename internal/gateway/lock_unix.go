//go:build !windows

package gateway

import (
	"errors"
	"os"
	"syscall"
)

// ErrAlreadyRunning marks a second daemon instance refusing to start because
// another one already holds the lock.
var ErrAlreadyRunning = errors.New("gateway: another water daemon is already running")

// lockFile takes an exclusive, non-blocking flock on path, creating it if
// needed. The returned func releases it. Only one daemon process may hold it
// at a time, which is what makes "only one instance runs" true even across
// separate process launches.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
