//go:build !windows

package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// armStateDump installs a SIGUSR1 handler that writes a live state dump
// without stopping the run. It returns a function that removes the handler.
func armStateDump(dump func() (string, error)) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				if path, err := dump(); err != nil {
					fmt.Fprintln(os.Stderr, "state dump failed:", err)
				} else {
					fmt.Fprintln(os.Stderr, "state dump written:", path)
				}
			case <-done:
				return
			}
		}
	}()
	return func() { signal.Stop(ch); close(done) }
}

func signalDump(pid int) error { return syscall.Kill(pid, syscall.SIGUSR1) }
