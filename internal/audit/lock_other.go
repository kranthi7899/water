//go:build windows

package audit

import (
	"errors"
)

const noFollow = 0

// The twin runs on macOS and Linux; without a writer lock the single-writer
// guarantee does not hold, so refuse rather than degrade.
func lockFile(string) (func(), error) {
	return nil, errors.New("audit log is not supported on this platform")
}
