//go:build darwin

package vault

import (
	"errors"
	"os"
	"testing"
)

// Touches the real login keychain, so it runs only when asked:
// WATER_KEYCHAIN_TEST=1 go test ./internal/vault
func TestKeychainRoundTrip(t *testing.T) {
	if os.Getenv("WATER_KEYCHAIN_TEST") != "1" {
		t.Skip("set WATER_KEYCHAIN_TEST=1 to exercise the login keychain")
	}
	v := NewKeychain()
	const svc, acct = "water.vault.selftest", "probe"
	_ = v.Delete(svc, acct)
	if _, err := v.Get(svc, acct); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing item: %v", err)
	}
	for _, val := range []string{"first secret", "second 'secret' $x"} {
		if err := v.Set(svc, acct, NewSecret(val)); err != nil {
			t.Fatal(err)
		}
		got, err := v.Get(svc, acct)
		if err != nil || got.Reveal() != val {
			t.Fatalf("got %q %v", got.Reveal(), err)
		}
	}
	if err := v.Delete(svc, acct); err != nil {
		t.Fatal(err)
	}
	if err := v.Delete(svc, acct); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if err := v.Set(svc, acct, NewSecret("two\nlines")); err == nil {
		t.Fatal("multi-line secret accepted")
	}
}
