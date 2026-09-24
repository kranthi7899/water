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
)

// Clients is the daemon's persisted bearer-token table: ~/.water/run/clients.json,
// 0600. The CLI's own token is named "cli" and is created on first start;
// `water daemon token new <name>` adds more for other clients (a macOS
// Shortcut, the Swift app).
type Clients struct {
	path string
	mu   sync.Mutex
	// token -> name
	tokens map[string]string
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
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &c.tokens); err != nil {
		return nil, fmt.Errorf("gateway: %s: %w", path, err)
	}
	return c, nil
}

func (c *Clients) save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c.tokens, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, b, 0o600)
}

// New mints a token for name and persists it.
func (c *Clients) New(name string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
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

// Valid reports the client name for token, if any.
func (c *Clients) Valid(token string) (name string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
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
