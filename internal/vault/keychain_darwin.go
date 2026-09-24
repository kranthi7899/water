//go:build darwin

package vault

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const securityBin = "/usr/bin/security"

// KeychainVault stores credentials as generic passwords in the user's login
// keychain through /usr/bin/security, so the binary needs no cgo. Items are
// created by that tool, so later reads by it do not prompt.
type KeychainVault struct{}

func NewKeychain() KeychainVault { return KeychainVault{} }

// notFound is security's exit status for errSecItemNotFound.
const notFound = 44

func (KeychainVault) Get(service, account string) (Secret, error) {
	if err := validKey(service, account); err != nil {
		return Secret{}, err
	}
	var out, stderr bytes.Buffer
	cmd := exec.Command(securityBin, "find-generic-password", "-s", service, "-a", account, "-w")
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return Secret{}, keychainErr("read", err)
	}
	return Secret{strings.TrimRight(out.String(), "\n")}, nil
}

// Set adds or updates an item. The password goes to security on stdin (it
// prompts twice when -w is last), never on the command line where other
// processes could read it.
func (KeychainVault) Set(service, account string, s Secret) error {
	if err := validKey(service, account); err != nil {
		return err
	}
	if s.IsZero() || strings.ContainsAny(s.v, "\r\n") {
		return errors.New("vault: credential must be non-empty and single-line")
	}
	cmd := exec.Command(securityBin, "add-generic-password", "-U", "-s", service, "-a", account, "-w")
	cmd.Stdin = strings.NewReader(s.v + "\n" + s.v + "\n")
	if err := cmd.Run(); err != nil {
		return keychainErr("write", err)
	}
	return nil
}

func (KeychainVault) Delete(service, account string) error {
	if err := validKey(service, account); err != nil {
		return err
	}
	if err := exec.Command(securityBin, "delete-generic-password", "-s", service, "-a", account).Run(); err != nil {
		return keychainErr("delete", err)
	}
	return nil
}

func validKey(service, account string) error {
	if service == "" || account == "" || strings.HasPrefix(service, "-") || strings.HasPrefix(account, "-") {
		return errors.New("vault: invalid service or account")
	}
	return nil
}

// keychainErr never includes security's output, which can echo item data.
func keychainErr(op string, err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == notFound {
		return ErrNotFound
	}
	if errors.As(err, &ee) {
		return fmt.Errorf("vault: keychain %s failed (exit %d)", op, ee.ExitCode())
	}
	return fmt.Errorf("vault: keychain %s failed: %w", op, err)
}
