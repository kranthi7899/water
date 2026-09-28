//go:build unix

package research

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// containProcessGroup puts the research subprocess in its own process group
// and makes cancellation (the timeout) kill the whole group, so a stuck
// fetch never outlives its call.
func containProcessGroup(cmd *exec.Cmd) {
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
