//go:build windows

package cli

import "errors"

func armStateDump(func() (string, error)) func() { return func() {} }

func signalDump(int) error {
	return errors.New("live state dumps need SIGUSR1, which Windows does not have")
}
