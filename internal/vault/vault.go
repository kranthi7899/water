// Package vault holds connector credentials. A credential reaches exactly one
// place, the connector invoke that needs it; it is never returned to a
// caller of the gate, logged, audited, stored or put into a prompt.
package vault

import (
	"errors"
	"fmt"
	"sync"
)

var ErrNotFound = errors.New("vault: credential not found")

// Secret wraps a credential so that every accidental rendering (fmt, %v,
// %#v, JSON, logs) prints a placeholder. Reveal is the only way out.
type Secret struct{ v string }

func NewSecret(s string) Secret { return Secret{s} }

const redacted = "[redacted]"

func (s Secret) Reveal() string             { return s.v }
func (s Secret) IsZero() bool               { return s.v == "" }
func (Secret) String() string               { return redacted }
func (Secret) GoString() string             { return redacted }
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
func (Secret) Format(f fmt.State, _ rune)   { _, _ = f.Write([]byte(redacted)) }

// Vault stores credentials by service and account.
type Vault interface {
	Get(service, account string) (Secret, error)
	Set(service, account string, s Secret) error
	Delete(service, account string) error
}

// MemoryVault is for Linux and tests.
type MemoryVault struct {
	mu sync.Mutex
	m  map[[2]string]Secret
}

func NewMemory() *MemoryVault { return &MemoryVault{m: map[[2]string]Secret{}} }

func (v *MemoryVault) Get(service, account string) (Secret, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	s, ok := v.m[[2]string{service, account}]
	if !ok {
		return Secret{}, ErrNotFound
	}
	return s, nil
}

func (v *MemoryVault) Set(service, account string, s Secret) error {
	if service == "" || account == "" {
		return errors.New("vault: service and account are required")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.m[[2]string{service, account}] = s
	return nil
}

func (v *MemoryVault) Delete(service, account string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.m[[2]string{service, account}]; !ok {
		return ErrNotFound
	}
	delete(v.m, [2]string{service, account})
	return nil
}
