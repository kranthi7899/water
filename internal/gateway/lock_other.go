//go:build windows

package gateway

import "errors"

var ErrAlreadyRunning = errors.New("gateway: another water daemon is already running")

// lockFile has no flock-based implementation on Windows yet; the single Unix
// socket transport this package targets is not supported there either.
func lockFile(path string) (func(), error) {
	return nil, errors.New("gateway: daemon locking is not implemented on this platform")
}
