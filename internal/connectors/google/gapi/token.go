package gapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	TokenURL  = "https://oauth2.googleapis.com/token"
	AuthURL   = "https://accounts.google.com/o/oauth2/v2/auth"
	RevokeURL = "https://oauth2.googleapis.com/revoke"
)

const refreshSkew = 60 * time.Second

var now = time.Now

// Options overrides endpoints and behaviour, mostly for tests. The zero
// value (or nil) talks to Google.
type Options struct {
	// BaseURL, if set, replaces the scheme and host of every API request URL,
	// keeping its path and query.
	BaseURL   string
	TokenURL  string
	AuthURL   string
	RevokeURL string
	// HTTPClient defaults to a client with a 60s timeout.
	HTTPClient *http.Client
	// Sleep waits between retries; it defaults to a context-aware timer.
	Sleep func(ctx context.Context, d time.Duration) error
	// MaxBytes caps a GetJSON response; default DefaultMaxBytes.
	MaxBytes int64
	// Timeout bounds how long Authorize waits for the browser; default 5m.
	Timeout time.Duration
	// Scopes requested by Authorize; default Scopes().
	Scopes []string
}

func (o *Options) or() Options {
	var v Options
	if o != nil {
		v = *o
	}
	if v.TokenURL == "" {
		v.TokenURL = TokenURL
	}
	if v.AuthURL == "" {
		v.AuthURL = AuthURL
	}
	if v.RevokeURL == "" {
		v.RevokeURL = RevokeURL
	}
	if v.HTTPClient == nil {
		v.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	if v.Sleep == nil {
		v.Sleep = sleepCtx
	}
	if v.MaxBytes <= 0 {
		v.MaxBytes = DefaultMaxBytes
	}
	if v.Timeout <= 0 {
		v.Timeout = 5 * time.Minute
	}
	if len(v.Scopes) == 0 {
		v.Scopes = Scopes()
	}
	return v
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// The access token lives in memory only, shared by every Client built from
// the same credential and token endpoint.
type tokenEntry struct {
	mu  sync.Mutex
	tok string
	exp time.Time
}

var tokens = struct {
	mu sync.Mutex
	m  map[string]*tokenEntry
}{m: map[string]*tokenEntry{}}

func cacheKey(tokenURL string, c Credential) string {
	h := sha256.Sum256([]byte(tokenURL + "\x00" + c.ClientID + "\x00" + c.ClientSecret + "\x00" + c.RefreshToken))
	return hex.EncodeToString(h[:])
}

func entryFor(key string) *tokenEntry {
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	e, ok := tokens.m[key]
	if !ok {
		e = &tokenEntry{}
		tokens.m[key] = e
	}
	return e
}

// token returns a valid access token. With force it refreshes unless the
// cached token already differs from stale, i.e. another caller refreshed.
func (c *Client) token(ctx context.Context, force bool, stale string) (string, error) {
	e := entryFor(c.key)
	e.mu.Lock()
	defer e.mu.Unlock()
	valid := e.tok != "" && now().Before(e.exp)
	if valid && (!force || e.tok != stale) {
		return e.tok, nil
	}
	tok, ttl, err := c.refresh(ctx)
	if err != nil {
		e.tok, e.exp = "", time.Time{}
		return "", err
	}
	skew := refreshSkew
	if ttl/2 < skew {
		skew = ttl / 2
	}
	e.tok, e.exp = tok, now().Add(ttl-skew)
	return tok, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func (c *Client) refresh(ctx context.Context) (string, time.Duration, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {c.cred.RefreshToken},
		"client_id":     {c.cred.ClientID},
		"client_secret": {c.cred.ClientSecret},
	}
	tr, err := postToken(ctx, c.opts.HTTPClient, c.opts.TokenURL, form, c.scrub)
	if err != nil {
		return "", 0, err
	}
	ttl := time.Duration(tr.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	return tr.AccessToken, ttl, nil
}

// postToken posts a form to the token endpoint. Every error text passes
// through scrub.
func postToken(ctx context.Context, hc *http.Client, endpoint string, form url.Values, scrub func(string) string) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("google: token request: %s", scrub(err.Error()))
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google: token request: %s", scrub(err.Error()))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	var tr tokenResponse
	jsonErr := json.Unmarshal(body, &tr)
	if tr.Error == "invalid_grant" {
		return nil, ErrReconnect
	}
	if resp.StatusCode != http.StatusOK || jsonErr != nil || tr.Error != "" || tr.AccessToken == "" {
		msg := tr.Error
		if tr.Description != "" {
			msg += ": " + tr.Description
		}
		if msg == "" {
			msg = "no access token in response"
		}
		return nil, fmt.Errorf("google: token endpoint returned %d: %s", resp.StatusCode, scrub(msg))
	}
	return &tr, nil
}
