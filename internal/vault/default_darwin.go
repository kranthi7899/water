//go:build darwin

package vault

// Default returns the production vault for this platform: the macOS
// Keychain, reached through /usr/bin/security so no cgo is needed.
func Default() Vault { return NewKeychain() }
