// Package tokenapi is the shared foundation for water's simple token-
// authenticated REST/GraphQL connectors (github, linear, hubspot): a single
// vault-stored credential, a retrying HTTP client, and secret scrubbing.
//
// It deliberately mirrors internal/connectors/google/gapi's shape — a
// Credential type with the same redaction posture, a Client with the same
// get-with-backoff structure, the same host-allowlist request-URL guard —
// but is simpler where these services are: there is no OAuth flow and
// nothing to refresh. Whatever the owner pastes at `water connect <service>
// --token <TOKEN>` is sent on every call until they replace or revoke it.
package tokenapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"water/internal/vault"
)

// ErrNoToken means there is no stored credential for this service.
var ErrNoToken = errors.New("tokenapi: not connected")

// Credential is one bearer/raw API token, stored compactly as single-line
// JSON in the vault (see internal/vault), exactly as gapi.Credential is.
type Credential struct {
	Token string `json:"token"`
}

type credentialJSON Credential

// ParseCredential decodes the vault value.
func ParseCredential(s string) (Credential, error) {
	var c credentialJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &c); err != nil {
		return Credential{}, errors.New("tokenapi: stored credential is not valid JSON")
	}
	cred := Credential(c)
	if err := cred.Validate(); err != nil {
		return Credential{}, err
	}
	return cred, nil
}

// CredentialFromSecret parses the credential a permit carries.
func CredentialFromSecret(s vault.Secret) (Credential, error) {
	if s.IsZero() {
		return Credential{}, ErrNoToken
	}
	return ParseCredential(s.Reveal())
}

// Marshal encodes the credential as the single-line vault value.
func (c Credential) Marshal() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(credentialJSON(c))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Secret wraps the marshalled credential for the vault.
func (c Credential) Secret() (vault.Secret, error) {
	s, err := c.Marshal()
	if err != nil {
		return vault.Secret{}, err
	}
	return vault.NewSecret(s), nil
}

func (c Credential) Validate() error {
	if strings.TrimSpace(c.Token) == "" {
		return errors.New("tokenapi: credential is missing token")
	}
	return nil
}

func (c Credential) secrets() []string { return []string{c.Token} }

const redacted = "[redacted]"

func (Credential) String() string               { return redacted }
func (Credential) GoString() string             { return redacted }
func (Credential) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }
func (Credential) Format(f fmt.State, _ rune)   { _, _ = f.Write([]byte(redacted)) }
