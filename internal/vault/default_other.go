//go:build !darwin

package vault

// Default returns an in-memory vault: Water's only supported client platform
// is macOS (the menu-bar app, the Keychain), so other platforms get a vault
// that works for development but does not persist secrets across restarts.
func Default() Vault { return NewMemory() }
