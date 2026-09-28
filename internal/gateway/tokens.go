package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Clients is the daemon's persisted bearer-token table: ~/.water/run/clients.json,
// 0600. The CLI's own token is named "cli" and is created on first start;
// `water daemon token new <name>` adds more for other clients (a macOS
// Shortcut, the Swift app).
//
// `water daemon token new` runs in its own process and rewrites the file
// while the daemon is up, so the table is not a startup-only snapshot: a
// lookup that misses re-reads the file when its mtime has changed (see
// Valid), and every save re-reads before writing and replaces the file
// atomically, so neither process drops a token the other minted or reads a
// half-written table.
type Clients struct {
	path string
	mu   sync.Mutex
	// token -> name
	tokens map[string]string
	// mtime is the file's modification time when tokens was last read from
	// or written to it; zero when the file did not exist.
	mtime time.Time
}

func randToken() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// LoadClients reads path, creating an empty table if it does not exist yet.
func LoadClients(path string) (*Clients, error) {
	c := &Clients{path: path, tokens: map[string]string{}}
	tokens, mtime, err := readClients(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	c.tokens, c.mtime = tokens, mtime
	return c, nil
}

// readClients reads and parses the table at path, with its mtime.
func readClients(path string) (map[string]string, time.Time, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	tokens := map[string]string{}
	if err := json.Unmarshal(b, &tokens); err != nil {
		return nil, time.Time{}, fmt.Errorf("gateway: %s: %w", path, err)
	}
	return tokens, fi.ModTime(), nil
}

// reloadLocked re-reads the file if its mtime differs from the last one
// seen. A missing, unreadable or unparsable file leaves the in-memory table
// as it is, so a broken file can never wipe out tokens that were valid.
// Callers hold c.mu.
func (c *Clients) reloadLocked() {
	fi, err := os.Stat(c.path)
	if err != nil || fi.ModTime().Equal(c.mtime) {
		return
	}
	tokens, mtime, err := readClients(c.path)
	if err != nil {
		return
	}
	c.tokens, c.mtime = tokens, mtime
}

// save writes the table atomically (a 0600 temp file renamed over the
// original), so a concurrent reader sees either the old or the new table,
// never a truncated one. Callers hold c.mu.
func (c *Clients) save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c.tokens, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	// WriteFile keeps a pre-existing file's mode; the table is always 0600.
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, c.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if fi, err := os.Stat(c.path); err == nil {
		c.mtime = fi.ModTime()
	}
	return nil
}

// New mints a token for name and persists it. It first picks up any tokens
// another process added to the file, so saving never drops them.
func (c *Clients) New(name string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reloadLocked()
	tok, err := randToken()
	if err != nil {
		return "", err
	}
	c.tokens[tok] = name
	return tok, c.save()
}

// EnsureCLI returns the existing "cli" client's token, minting one if the
// table has none yet (first `water daemon` start).
func (c *Clients) EnsureCLI() (string, error) {
	c.mu.Lock()
	for tok, name := range c.tokens {
		if name == "cli" {
			c.mu.Unlock()
			return tok, nil
		}
	}
	c.mu.Unlock()
	return c.New("cli")
}

// Valid reports the client name for token, if any. A miss re-reads the file
// when it changed since it was last read, so a token minted by
// `water daemon token new` while the daemon is running is accepted at once
// rather than after a restart. The mtime check keeps repeated bad-token
// requests from re-reading an unchanged file.
func (c *Clients) Valid(token string) (name string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if name, ok = c.tokens[token]; ok {
		return name, ok
	}
	c.reloadLocked()
	name, ok = c.tokens[token]
	return name, ok
}

// Names lists every client name, sorted (for `water daemon token list`, if
// ever needed) — currently used only by tests.
func (c *Clients) Names() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.tokens))
	for _, n := range c.tokens {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
