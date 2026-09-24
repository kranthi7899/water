package gateway

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// bumpMtime moves path's mtime forward so a reload sees a change even on a
// filesystem with coarse timestamps.
func bumpMtime(t *testing.T, path string) {
	t.Helper()
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
}

// A token minted by a second process (`water daemon token new`) while the
// daemon is running is accepted without restarting the daemon.
func TestClientsValidPicksUpTokenMintedElsewhere(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clients.json")
	daemon, err := LoadClients(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.EnsureCLI(); err != nil {
		t.Fatal(err)
	}

	cli, err := LoadClients(path)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := cli.New("ui")
	if err != nil {
		t.Fatal(err)
	}
	bumpMtime(t, path)

	if name, ok := daemon.Valid(tok); !ok || name != "ui" {
		t.Fatalf("Valid(minted elsewhere) = %q, %v; want ui, true", name, ok)
	}
	if _, ok := daemon.Valid("not-a-token"); ok {
		t.Fatal("an unknown token was accepted")
	}
}

// A save from one instance never drops a token another instance minted.
func TestClientsNewKeepsTokensMintedElsewhere(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clients.json")
	a, err := LoadClients(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.EnsureCLI(); err != nil {
		t.Fatal(err)
	}
	b, err := LoadClients(path)
	if err != nil {
		t.Fatal(err)
	}
	tokB, err := b.New("ui")
	if err != nil {
		t.Fatal(err)
	}
	bumpMtime(t, path)
	tokA, err := a.New("shortcut")
	if err != nil {
		t.Fatal(err)
	}

	fresh, err := LoadClients(path)
	if err != nil {
		t.Fatal(err)
	}
	for tok, want := range map[string]string{tokA: "shortcut", tokB: "ui"} {
		if name, ok := fresh.Valid(tok); !ok || name != want {
			t.Fatalf("Valid(%s) = %q, %v; want %s", want, name, ok, want)
		}
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("clients.json mode = %v, want 0600", fi.Mode().Perm())
	}
}

// A corrupt file never wipes the tokens already loaded.
func TestClientsCorruptFileKeepsLoadedTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clients.json")
	c, err := LoadClients(path)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := c.EnsureCLI()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	bumpMtime(t, path)
	if _, ok := c.Valid("miss-forces-a-reload"); ok {
		t.Fatal("unknown token accepted")
	}
	if _, ok := c.Valid(tok); !ok {
		t.Fatal("a corrupt clients.json wiped a valid token")
	}
}
