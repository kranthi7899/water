package gapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"water/internal/vault"
)

// API base URLs. Build request URLs from these; Options.BaseURL swaps their
// scheme and host in tests.
const (
	CalendarBase = "https://www.googleapis.com/calendar/v3"
	GmailBase    = "https://gmail.googleapis.com/gmail/v1"
	DriveBase    = "https://www.googleapis.com/drive/v3"
)

const (
	DefaultMaxBytes = 8 << 20
	maxErrorBytes   = 64 << 10
	maxTries        = 4
	backoffBase     = 500 * time.Millisecond
	backoffCap      = 8 * time.Second
	retryAfterCap   = 30 * time.Second
)

// APIError is a non-2xx answer from a Google API. Message is Google's own
// error message with any secret scrubbed out.
type APIError struct {
	Status  int
	Reason  string
	Message string
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("google: HTTP %d", e.Status)
	if e.Reason != "" {
		s += " (" + e.Reason + ")"
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// Status returns the HTTP status of an APIError in err's chain, or 0.
func Status(err error) int {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

// Client calls Google APIs with a credential. It is cheap to build: the
// access token is cached per credential, so connectors build one per call.
type Client struct {
	cred Credential
	opts Options
	key  string

	mu   sync.Mutex
	seen []string
}

// New builds a client. o may be nil.
func New(cred Credential, o *Options) (*Client, error) {
	if err := cred.Validate(); err != nil {
		return nil, err
	}
	opts := o.or()
	return &Client{cred: cred, opts: opts, key: cacheKey(opts.TokenURL, cred)}, nil
}

// FromSecret builds a client from the secret a permit carries.
func FromSecret(s vault.Secret, o *Options) (*Client, error) {
	cred, err := CredentialFromSecret(s)
	if err != nil {
		return nil, err
	}
	return New(cred, o)
}

// Refresh forces a token refresh; `water connect google --status` uses it.
func (c *Client) Refresh(ctx context.Context) error {
	tok, err := c.token(ctx, true, "")
	c.remember(tok)
	return err
}

// GetJSON GETs rawURL with query added and decodes the JSON body into out.
func (c *Client) GetJSON(ctx context.Context, rawURL string, query url.Values, out any) error {
	body, truncated, err := c.get(ctx, rawURL, query, "application/json", c.opts.MaxBytes)
	if err != nil {
		return err
	}
	if truncated {
		return fmt.Errorf("google: response larger than %d bytes", c.opts.MaxBytes)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("google: decoding response: %s", c.scrub(err.Error()))
	}
	return nil
}

// GetBytes GETs rawURL and returns at most max bytes of the body, reporting
// whether it was cut. It suits exports such as Drive files.export.
func (c *Client) GetBytes(ctx context.Context, rawURL string, query url.Values, max int64) ([]byte, bool, error) {
	if max <= 0 {
		max = c.opts.MaxBytes
	}
	return c.get(ctx, rawURL, query, "*/*", max)
}

// GetText is GetBytes as valid UTF-8 text.
func (c *Client) GetText(ctx context.Context, rawURL string, query url.Values, max int64) (string, bool, error) {
	b, truncated, err := c.GetBytes(ctx, rawURL, query, max)
	if err != nil {
		return "", false, err
	}
	return strings.ToValidUTF8(string(b), ""), truncated, nil
}

func (c *Client) requestURL(rawURL string, query url.Values) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", errors.New("google: invalid request URL")
	}
	if c.opts.BaseURL != "" {
		b, err := url.Parse(c.opts.BaseURL)
		if err != nil {
			return "", errors.New("google: invalid base URL override")
		}
		u.Scheme, u.Host = b.Scheme, b.Host
	} else if u.Scheme != "https" || !(u.Host == "googleapis.com" || strings.HasSuffix(u.Host, ".googleapis.com")) {
		// The bearer token only ever goes to Google's API hosts.
		return "", fmt.Errorf("google: refusing to send credentials to %q", u.Host)
	}
	if len(query) > 0 {
		q := u.Query()
		for k, vs := range query {
			for _, v := range vs {
				q.Add(k, v)
			}
		}
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

func (c *Client) get(ctx context.Context, rawURL string, query url.Values, accept string, max int64) ([]byte, bool, error) {
	target, err := c.requestURL(rawURL, query)
	if err != nil {
		return nil, false, err
	}
	refreshed := false
	var forceStale string
	force := false
	for try := 1; ; try++ {
		tok, err := c.token(ctx, force, forceStale)
		c.remember(tok)
		if err != nil {
			return nil, false, err
		}
		force = false
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, false, fmt.Errorf("google: %s", c.scrub(err.Error()))
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", accept)
		resp, err := c.opts.HTTPClient.Do(req)
		if err != nil {
			if ctx.Err() != nil || try >= maxTries {
				return nil, false, fmt.Errorf("google: request failed: %s", c.scrub(err.Error()))
			}
			if err := c.opts.Sleep(ctx, backoff(try, 0)); err != nil {
				return nil, false, err
			}
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
			resp.Body.Close()
			if err != nil {
				return nil, false, fmt.Errorf("google: reading response: %s", c.scrub(err.Error()))
			}
			if int64(len(body)) > max {
				return body[:max], true, nil
			}
			return body, false, nil
		}
		apiErr := c.readError(resp)
		if resp.StatusCode == http.StatusUnauthorized && !refreshed {
			refreshed, force, forceStale = true, true, tok
			try--
			continue
		}
		if retryable(apiErr) && try < maxTries {
			if err := c.opts.Sleep(ctx, backoff(try, retryAfter(resp))); err != nil {
				return nil, false, err
			}
			continue
		}
		return nil, false, apiErr
	}
}

func (c *Client) readError(resp *http.Response) *APIError {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBytes))
	var g struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	e := &APIError{Status: resp.StatusCode}
	if json.Unmarshal(body, &g) == nil {
		e.Message = g.Error.Message
		for _, r := range g.Error.Errors {
			if r.Reason != "" {
				e.Reason = r.Reason
				break
			}
		}
		if e.Reason == "" {
			for _, d := range g.Error.Details {
				if d.Reason != "" {
					e.Reason = d.Reason
					break
				}
			}
		}
	}
	if e.Message == "" {
		e.Message = http.StatusText(resp.StatusCode)
	}
	if len(e.Message) > 500 {
		e.Message = e.Message[:500]
	}
	e.Message = strings.ToValidUTF8(c.scrub(e.Message), "")
	e.Reason = c.scrub(e.Reason)
	return e
}

func retryable(e *APIError) bool {
	switch {
	case e.Status == http.StatusTooManyRequests, e.Status >= 500:
		return true
	case e.Status == http.StatusForbidden:
		r := strings.ToLower(strings.ReplaceAll(e.Reason, "_", ""))
		return r == "ratelimitexceeded" || r == "userratelimitexceeded"
	}
	return false
}

func retryAfter(resp *http.Response) time.Duration {
	s, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After")))
	if err != nil || s <= 0 {
		return 0
	}
	return min(time.Duration(s)*time.Second, retryAfterCap)
}

// backoff is truncated exponential with jitter in [d/2, d).
func backoff(try int, floor time.Duration) time.Duration {
	d := backoffBase << (try - 1)
	if d > backoffCap || d <= 0 {
		d = backoffCap
	}
	d = d/2 + rand.N(d/2)
	return max(d, floor)
}

func (c *Client) remember(tok string) {
	if tok == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.seen {
		if s == tok {
			return
		}
	}
	c.seen = append(c.seen, tok)
}

func (c *Client) scrub(s string) string {
	c.mu.Lock()
	secrets := append(c.cred.secrets(), c.seen...)
	c.mu.Unlock()
	return scrub(s, secrets...)
}

// Google access tokens, refresh tokens, client secrets and auth codes.
var secretPattern = regexp.MustCompile(`ya29\.[A-Za-z0-9._\-]+|1//[A-Za-z0-9._\-]+|GOCSPX-[A-Za-z0-9._\-]+|4/[0-9A-Za-z_\-]{20,}`)

func scrub(s string, secrets ...string) string {
	for _, sec := range secrets {
		if len(sec) >= 4 {
			s = strings.ReplaceAll(s, sec, redacted)
			if esc := url.QueryEscape(sec); esc != sec {
				s = strings.ReplaceAll(s, esc, redacted)
			}
		}
	}
	return secretPattern.ReplaceAllString(s, redacted)
}
