//go:build unix

package tools

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// A cancelled shell can otherwise leave its children running (and keep its
// output pipe open). The group is private to this one approved action.
func containProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
