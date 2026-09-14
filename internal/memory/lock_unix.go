//go:build !windows

package memory

import (
	"os"
	"path/filepath"
	"syscall"
)

// lockRole takes an exclusive advisory lock for one role's memory file across
// processes, so two sessions of the same role (a chat running /remember and a
// `water memory add` in another tab) cannot interleave read-modify-write and
// lose an entry. The in-process mutex alone only protects one process.
func lockRole(root, role string) (func(), error) {
	dir := filepath.Join(root, filepath.Base(role))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
