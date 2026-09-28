package tokenapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"water/internal/vault"
)

const (
	DefaultMaxBytes = 4 << 20
	maxErrorBytes   = 64 << 10
	maxTries        = 4
	backoffBase     = 500 * time.Millisecond
	backoffCap      = 8 * time.Second
	retryAfterCap   = 30 * time.Second
)

// APIError is a non-2xx answer. Message has any secret already scrubbed.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("tokenapi: HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("tokenapi: HTTP %d", e.Status)
}

// Status returns the HTTP status of an APIError in err's chain, or 0.
func Status(err error) int {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

// AuthHeader returns the header name/value a Client sends the token as.
// Services differ: GitHub and HubSpot want "Authorization: Bearer <token>";
// Linear wants the raw key with no scheme.
type AuthHeader func(token string) (name, value string)

func BearerAuth(token string) (string, string) { return "Authorization", "Bearer " + token }
func RawAuth(token string) (string, string)    { return "Authorization", token }

// Options overrides endpoints and behaviour, mostly for tests. The zero
// value (or nil) talks to the real host.
type Options struct {
	// BaseURL, if set, replaces the scheme and host of every request URL,
	// keeping its path and query (an httptest server in tests).
	BaseURL string
	// HTTPClient defaults to a client with a 30s timeout.
	HTTPClient *http.Client
	// Sleep waits between retries; defaults to a context-aware timer.
	Sleep func(ctx context.Context, d time.Duration) error
	// MaxBytes caps a response body; default DefaultMaxBytes.
	MaxBytes int64
}

func (o *Options) or() Options {
	var v Options
	if o != nil {
		v = *o
	}
	if v.HTTPClient == nil {
		v.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if v.Sleep == nil {
		v.Sleep = sleepCtx
	}
	if v.MaxBytes <= 0 {
		v.MaxBytes = DefaultMaxBytes
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

// Client calls a token-authenticated REST or GraphQL API. Host restricts
// which host the token is ever sent to — the same guard gapi.Client applies
// for Google — so a malformed request URL can never leak the token
// elsewhere.
type Client struct {
	cred Credential
	auth AuthHeader
	host string
	opts Options

	mu sync.Mutex
}

// New builds a client. o may be nil.
func New(cred Credential, auth AuthHeader, host string, o *Options) (*Client, error) {
	if err := cred.Validate(); err != nil {
		return nil, err
	}
	if host == "" {
		return nil, errors.New("tokenapi: host is required")
	}
	return &Client{cred: cred, auth: auth, host: host, opts: o.or()}, nil
}

// FromSecret builds a client from the secret a permit carries.
func FromSecret(s vault.Secret, auth AuthHeader, host string, o *Options) (*Client, error) {
	cred, err := CredentialFromSecret(s)
	if err != nil {
		return nil, err
	}
	return New(cred, auth, host, o)
}

func (c *Client) requestURL(rawURL string, query url.Values) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", errors.New("tokenapi: invalid request URL")
	}
	if c.opts.BaseURL != "" {
		b, err := url.Parse(c.opts.BaseURL)
		if err != nil {
			return "", errors.New("tokenapi: invalid base URL override")
		}
		u.Scheme, u.Host = b.Scheme, b.Host
	} else if u.Scheme != "https" || u.Host != c.host {
		// The token only ever goes to this connector's own API host.
		return "", fmt.Errorf("tokenapi: refusing to send credentials to %q", u.Host)
	}
	for _, seg := range strings.Split(u.EscapedPath(), "/") {
		if seg == "." || seg == ".." {
			return "", errors.New("tokenapi: invalid id in request path")
		}
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

func (c *Client) applyHeaders(req *http.Request, extra map[string]string) {
	name, value := c.auth(c.cred.Token)
	req.Header.Set(name, value)
	for k, v := range extra {
		req.Header.Set(k, v)
	}
}

// Get GETs rawURL with query added, returning the raw body and the
// response's headers (GitHub's pagination/rate-limit headers live there).
func (c *Client) Get(ctx context.Context, rawURL string, query url.Values, extraHeaders map[string]string) ([]byte, http.Header, error) {
	target, err := c.requestURL(rawURL, query)
	if err != nil {
		return nil, nil, err
	}
	for try := 1; ; try++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("tokenapi: %s", c.scrub(err.Error()))
		}
		c.applyHeaders(req, extraHeaders)
		resp, err := c.opts.HTTPClient.Do(req)
		if err != nil {
			if ctx.Err() != nil || try >= maxTries {
				return nil, nil, fmt.Errorf("tokenapi: request failed: %s", c.scrub(err.Error()))
			}
			if err := c.opts.Sleep(ctx, backoff(try, 0)); err != nil {
				return nil, nil, err
			}
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			body, err := io.ReadAll(io.LimitReader(resp.Body, c.opts.MaxBytes+1))
			hdr := resp.Header.Clone()
			resp.Body.Close()
			if err != nil {
				return nil, nil, fmt.Errorf("tokenapi: reading response: %s", c.scrub(err.Error()))
			}
			if int64(len(body)) > c.opts.MaxBytes {
				return nil, nil, fmt.Errorf("tokenapi: response larger than %d bytes", c.opts.MaxBytes)
			}
			return body, hdr, nil
		}
		apiErr := c.readError(resp)
		hdr := resp.Header.Clone()
		if retryable(apiErr) && try < maxTries {
			if err := c.opts.Sleep(ctx, backoff(try, retryAfter(resp))); err != nil {
				return nil, nil, err
			}
			continue
		}
		return nil, hdr, apiErr
	}
}

// GetJSON is Get, decoding the body as JSON into out.
func (c *Client) GetJSON(ctx context.Context, rawURL string, query url.Values, extraHeaders map[string]string, out any) (http.Header, error) {
	body, hdr, err := c.Get(ctx, rawURL, query, extraHeaders)
	if err != nil {
		return hdr, err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return hdr, fmt.Errorf("tokenapi: decoding response: %s", c.scrub(err.Error()))
	}
	return hdr, nil
}

// PostJSON POSTs body as JSON to rawURL and decodes the response into out.
// Every call this package makes so far is a read (a GraphQL query has no
// side effect on the server), so — unlike gapi.PostJSON's single-attempt
// ErrSendOutcomeUnknown contract, needed once a write exists — retrying a
// failed attempt here is safe. A future GraphQL mutation must not reuse this
// method as-is; it would need gapi's single-attempt write discipline.
func (c *Client) PostJSON(ctx context.Context, rawURL string, extraHeaders map[string]string, body, out any) (http.Header, error) {
	target, err := c.requestURL(rawURL, nil)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("tokenapi: encoding request: %s", c.scrub(err.Error()))
	}
	for try := 1; ; try++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(string(payload)))
		if err != nil {
			return nil, fmt.Errorf("tokenapi: %s", c.scrub(err.Error()))
		}
		req.Header.Set("Content-Type", "application/json")
		c.applyHeaders(req, extraHeaders)
		resp, err := c.opts.HTTPClient.Do(req)
		if err != nil {
			if ctx.Err() != nil || try >= maxTries {
				return nil, fmt.Errorf("tokenapi: request failed: %s", c.scrub(err.Error()))
			}
			if err := c.opts.Sleep(ctx, backoff(try, 0)); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			b, err := io.ReadAll(io.LimitReader(resp.Body, c.opts.MaxBytes+1))
			hdr := resp.Header.Clone()
			resp.Body.Close()
			if err != nil {
				return nil, fmt.Errorf("tokenapi: reading response: %s", c.scrub(err.Error()))
			}
			if int64(len(b)) > c.opts.MaxBytes {
				return nil, fmt.Errorf("tokenapi: response larger than %d bytes", c.opts.MaxBytes)
			}
			if out != nil {
				if err := json.Unmarshal(b, out); err != nil {
					return hdr, fmt.Errorf("tokenapi: decoding response: %s", c.scrub(err.Error()))
				}
			}
			return hdr, nil
		}
		apiErr := c.readError(resp)
		hdr := resp.Header.Clone()
		if retryable(apiErr) && try < maxTries {
			if err := c.opts.Sleep(ctx, backoff(try, retryAfter(resp))); err != nil {
				return nil, err
			}
			continue
		}
		return hdr, apiErr
	}
}

func (c *Client) readError(resp *http.Response) *APIError {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	var g struct {
		Message string `json:"message"`
	}
	msg := ""
	if json.Unmarshal(body, &g) == nil && g.Message != "" {
		msg = g.Message
	} else {
		msg = strings.TrimSpace(string(body))
	}
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return &APIError{Status: resp.StatusCode, Message: c.scrub(strings.ToValidUTF8(msg, ""))}
}

// retryable reports whether a status is worth a backoff retry: rate limits
// and server errors. A 401 (bad/expired token — nothing to refresh) and
// every other 4xx are terminal.
func retryable(e *APIError) bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
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

func (c *Client) scrub(s string) string {
	return scrub(s, c.cred.secrets()...)
}

func scrub(s string, secrets ...string) string {
	for _, sec := range secrets {
		if len(sec) >= 4 {
			s = strings.ReplaceAll(s, sec, redacted)
		}
	}
	return s
}
