// Package identity binds persona files to their role (Part 3A).
//
// Each role has a stable random role_id (a UUID minted at scaffold time — not
// the human name, which is guessable and typeable into any file) in role.yaml
// as the source of truth. Every persona file carries that role_id in its
// frontmatter plus file_type and content_hash. The loader verifies the
// embedded role_id against the folder's role.yaml and fails loudly on
// mismatch, catching a file copied into the wrong folder, a bad merge, or a
// backup restored to the wrong place.
//
// An HMAC over role_id + content_hash, keyed from ~/.water/keyring (outside
// agents/, so a copied folder does not carry its key), raises this from
// tamper-detecting to a real barrier against casual forgery.
//
// Honest limit: this defeats accidents and casual tampering, not a determined
// local attacker with root, who can read the keyring. That is not the threat
// model for a single-user CLI.
package identity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Frontmatter keys stamped into persona files.
const (
	KeyRoleID      = "role_id"
	KeyFileType    = "file_type"
	KeyContentHash = "content_hash"
	KeySignature   = "signature"
)

// FileTypes that are identity-bound. Skills are bound per SKILL.md too.
var FileTypes = map[string]string{
	"soul.md":       "soul",
	"experience.md": "experience",
	"SKILL.md":      "skill",
}

// ErrMismatch is the base error for every identity failure.
var ErrMismatch = errors.New("persona identity check failed")

// NewRoleID mints a random RFC 4122 v4 UUID (lowercase).
func NewRoleID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// ContentHash is sha256 over the body (everything after the frontmatter),
// with line endings normalised so a CRLF checkout does not read as tamper.
func ContentHash(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.TrimRight(body, "\n") + "\n"
	sum := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Sign computes the HMAC over role_id + content_hash with key.
func Sign(key []byte, roleID, contentHash string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(roleID + "\n" + contentHash + "\n"))
	return "hmac-sha256:" + hex.EncodeToString(m.Sum(nil))
}

// Verify checks a persona file's frontmatter against the folder's role_id.
//
//   - role_id present and != manifest role_id → ErrMismatch
//   - content_hash present and != hash(body) → ErrMismatch
//   - key != nil and requireSig: signature must be present and valid
//   - key != nil and signature present: must be valid
//
// Files with none of the identity keys pass (unstamped scaffold) unless
// requireSig is set, which the loader sets for a real local source directory
// once a keyring exists — that is what makes "edited but unsigned" fail.
func Verify(front map[string]any, body, manifestRoleID, name string, key []byte, requireSig bool) error {
	get := func(k string) string {
		v, _ := front[k].(string)
		return strings.TrimSpace(v)
	}
	rid, ch, sig := get(KeyRoleID), get(KeyContentHash), get(KeySignature)
	if rid != "" && manifestRoleID != "" && rid != manifestRoleID {
		return fmt.Errorf("%w: %s carries role_id %s but its folder's role.yaml declares %s — was this file copied from another role?", ErrMismatch, name, short(rid), short(manifestRoleID))
	}
	if rid != "" && manifestRoleID == "" {
		return fmt.Errorf("%w: %s carries role_id %s but role.yaml declares none", ErrMismatch, name, short(rid))
	}
	if ch != "" && ch != ContentHash(body) {
		return fmt.Errorf("%w: %s content_hash does not match its body — edited outside `water persona edit`? re-stamp with `water persona sign`", ErrMismatch, name)
	}
	if key != nil {
		if sig == "" && requireSig {
			return fmt.Errorf("%w: %s is unsigned but a keyring exists — run `water persona sign` after an authorised edit", ErrMismatch, name)
		}
		if sig != "" {
			if rid == "" || ch == "" {
				return fmt.Errorf("%w: %s has a signature but no role_id/content_hash", ErrMismatch, name)
			}
			if !hmac.Equal([]byte(sig), []byte(Sign(key, rid, ch))) {
				return fmt.Errorf("%w: %s signature is invalid for this machine's keyring", ErrMismatch, name)
			}
		}
	}
	return nil
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8] + "…"
	}
	return id
}

// Keyring holds the machine-local HMAC key.
type Keyring struct {
	Path string
	Key  []byte
}

// KeyringPath is <water home>/keyring.
func KeyringPath(home string) string { return filepath.Join(home, "keyring") }

// LoadKeyring reads the key if present. A missing keyring yields (nil, nil).
func LoadKeyring(home string) (*Keyring, error) {
	p := KeyringPath(home)
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(key) < 16 {
		return nil, fmt.Errorf("keyring %s is corrupt", p)
	}
	return &Keyring{Path: p, Key: key}, nil
}

// EnsureKeyring loads or creates the keyring (0600).
func EnsureKeyring(home string) (*Keyring, bool, error) {
	if k, err := LoadKeyring(home); err != nil || k != nil {
		return k, false, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, false, err
	}
	p := KeyringPath(home)
	if err := os.WriteFile(p, []byte(hex.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, false, err
	}
	return &Keyring{Path: p, Key: key}, true, nil
}

// Split separates a markdown file into raw frontmatter map and body without
// reformatting anything the caller does not touch.
func Split(raw []byte) (front map[string]any, body string, hadFront bool, err error) {
	s := strings.TrimPrefix(string(raw), "\ufeff")
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return map[string]any{}, s, false, nil
	}
	rest := s[strings.IndexByte(s, '\n')+1:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return map[string]any{}, s, false, nil
	}
	fm := rest[:idx]
	body = rest[idx+4:]
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[i+1:]
	} else {
		body = ""
	}
	front = map[string]any{}
	if strings.TrimSpace(fm) != "" {
		if err := yaml.Unmarshal([]byte(fm), &front); err != nil {
			return nil, "", true, err
		}
	}
	return front, body, true, nil
}

// Stamp rewrites raw with role_id, file_type, content_hash (and signature when
// key != nil) set in the frontmatter. Other frontmatter keys are preserved in
// their existing order; identity keys are appended at the end in a fixed order
// so diffs stay readable.
func Stamp(raw []byte, roleID, fileType string, key []byte) ([]byte, error) {
	front, body, _, err := Split(raw)
	if err != nil {
		return nil, err
	}
	ch := ContentHash(body)
	front[KeyRoleID] = roleID
	front[KeyFileType] = fileType
	front[KeyContentHash] = ch
	if key != nil {
		front[KeySignature] = Sign(key, roleID, ch)
	} else {
		delete(front, KeySignature)
	}
	return Render(front, body, raw), nil
}

// Render writes frontmatter + body. Existing (non-identity) keys keep the
// order they had in orig's frontmatter; identity keys come last.
func Render(front map[string]any, body string, orig []byte) []byte {
	identity := []string{KeyRoleID, KeyFileType, KeyContentHash, KeySignature}
	isIdentity := map[string]bool{}
	for _, k := range identity {
		isIdentity[k] = true
	}
	var order []string
	seen := map[string]bool{}
	if origFront, _, had, err := Split(orig); had && err == nil {
		// Recover key order from the raw text.
		s := string(orig)
		rest := s[strings.IndexByte(s, '\n')+1:]
		idx := strings.Index(rest, "\n---")
		for _, ln := range strings.Split(rest[:idx], "\n") {
			k, _, ok := strings.Cut(ln, ":")
			k = strings.TrimSpace(k)
			if ok && !strings.HasPrefix(ln, " ") && k != "" && !isIdentity[k] {
				if _, present := origFront[k]; present && !seen[k] {
					order = append(order, k)
					seen[k] = true
				}
			}
		}
	}
	var extra []string
	for k := range front {
		if !seen[k] && !isIdentity[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	order = append(order, extra...)
	var sb strings.Builder
	sb.WriteString("---\n")
	write := func(k string) {
		v, ok := front[k]
		if !ok {
			return
		}
		b, _ := yaml.Marshal(map[string]any{k: v})
		sb.Write(b)
	}
	for _, k := range order {
		write(k)
	}
	for _, k := range identity {
		write(k)
	}
	sb.WriteString("---\n")
	sb.WriteString(body)
	return []byte(sb.String())
}

// FileStatus is what `persona show` and the dashboard report per file.
type FileStatus struct {
	Name     string `json:"name"`
	RoleID   string `json:"role_id,omitempty"`
	FileType string `json:"file_type,omitempty"`
	Hash     string `json:"content_hash,omitempty"`
	Signed   bool   `json:"signed"`
	Stamped  bool   `json:"stamped"`
	Verified string `json:"verified"` // ok | unstamped | error text
}

// Status inspects one file without failing.
func Status(name string, raw []byte, manifestRoleID string, key []byte, requireSig bool) FileStatus {
	st := FileStatus{Name: name}
	front, body, _, err := Split(raw)
	if err != nil {
		st.Verified = "frontmatter: " + err.Error()
		return st
	}
	st.RoleID, _ = front[KeyRoleID].(string)
	st.FileType, _ = front[KeyFileType].(string)
	st.Hash, _ = front[KeyContentHash].(string)
	sig, _ := front[KeySignature].(string)
	st.Signed = sig != ""
	st.Stamped = st.RoleID != "" && st.Hash != ""
	if verr := Verify(front, body, manifestRoleID, name, key, requireSig); verr != nil {
		st.Verified = verr.Error()
	} else if !st.Stamped {
		st.Verified = "unstamped"
	} else {
		st.Verified = "ok"
	}
	return st
}
