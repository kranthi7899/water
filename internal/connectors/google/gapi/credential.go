// Package gapi is the shared foundation of the Google connectors: the stored
// credential, an in-memory access token cache, a retrying HTTP client and the
// installed-app OAuth flow used by `water connect google`.
package gapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"water/internal/vault"
)

// Service and DefaultAccount name the Keychain entry holding the Credential.
const (
	Service        = "water.google"
	DefaultAccount = "ceo"
)

const (
	ScopeCalendarEventsReadonly = "https://www.googleapis.com/auth/calendar.events.readonly"
	ScopeGmailReadonly          = "https://www.googleapis.com/auth/gmail.readonly"
	ScopeDriveReadonly          = "https://www.googleapis.com/auth/drive.readonly"
)

// Scopes returns the scopes water asks for.
func Scopes() []string {
	return []string{ScopeCalendarEventsReadonly, ScopeGmailReadonly, ScopeDriveReadonly}
}

var (
	// ErrReconnect means Google no longer accepts the stored refresh token.
	ErrReconnect = errors.New("google: authorization expired or revoked; reconnect: run `water connect google`")
	// ErrNotConnected means there is no stored credential.
	ErrNotConnected = errors.New("google: not connected; run `water connect google`")
)

// Credential is what the vault stores, as compact single-line JSON.
type Credential struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
}

type credentialJSON Credential

// ParseCredential decodes the vault value.
func ParseCredential(s string) (Credential, error) {
	var c credentialJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &c); err != nil {
		return Credential{}, errors.New("google: stored credential is not valid JSON; reconnect: run `water connect google`")
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
		return Credential{}, ErrNotConnected
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
	var missing []string
	if c.ClientID == "" {
		missing = append(missing, "client_id")
	}
	if c.ClientSecret == "" {
		missing = append(missing, "client_secret")
	}
	if c.RefreshToken == "" {
		missing = append(missing, "refresh_token")
	}
	if len(missing) > 0 {
		return fmt.Errorf("google: credential is missing %s; reconnect: run `water connect google`", strings.Join(missing, ", "))
	}
	return nil
}

func (c Credential) secrets() []string { return []string{c.RefreshToken, c.ClientSecret} }

const redacted = "[redacted]"

func (Credential) String() string               { return redacted }
func (Credential) GoString() string             { return redacted }
func (Credential) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }
func (Credential) Format(f fmt.State, _ rune)   { _, _ = f.Write([]byte(redacted)) }

// ClientConfig is the OAuth client from Google's downloaded client_secret JSON.
type ClientConfig struct {
	ClientID     string
	ClientSecret string
}

func (ClientConfig) String() string             { return redacted }
func (ClientConfig) GoString() string           { return redacted }
func (ClientConfig) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }

// ParseClientFile reads the JSON downloaded for a Desktop OAuth client.
func ParseClientFile(data []byte) (ClientConfig, error) {
	var f struct {
		Installed *struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
		} `json:"installed"`
		Web json.RawMessage `json:"web"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return ClientConfig{}, errors.New("google: client file is not valid JSON")
	}
	if f.Installed == nil {
		if f.Web != nil {
			return ClientConfig{}, errors.New(`google: client file is for a "Web application" client; create a "Desktop app" OAuth client instead`)
		}
		return ClientConfig{}, errors.New(`google: client file has no "installed" client; create a "Desktop app" OAuth client`)
	}
	if f.Installed.ClientID == "" || f.Installed.ClientSecret == "" {
		return ClientConfig{}, errors.New("google: client file is missing client_id or client_secret")
	}
	return ClientConfig{ClientID: f.Installed.ClientID, ClientSecret: f.Installed.ClientSecret}, nil
}
