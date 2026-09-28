package gapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

type callback struct {
	code, state, err string
}

// Authorize runs the installed-app loopback flow with PKCE. It listens on
// 127.0.0.1, hands the consent URL to open (which should print it as well as
// try a browser; an error from open aborts), waits for Google's redirect and
// exchanges the code. o may be nil.
func Authorize(ctx context.Context, cfg ClientConfig, open func(authURL string) error, o *Options) (Credential, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return Credential{}, errors.New("google: client id and secret are required")
	}
	if open == nil {
		return Credential{}, errors.New("google: no way to open the consent page")
	}
	opts := o.or()
	verifier, err := randomString(32)
	if err != nil {
		return Credential{}, err
	}
	state, err := randomString(32)
	if err != nil {
		return Credential{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Credential{}, fmt.Errorf("google: loopback listener: %w", err)
	}
	redirect := "http://127.0.0.1:" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)

	results := make(chan callback, 1)
	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" || r.Method != http.MethodGet {
				http.NotFound(w, r)
				return
			}
			q := r.URL.Query()
			cb := callback{code: q.Get("code"), state: q.Get("state"), err: q.Get("error")}
			if cb.code == "" && cb.err == "" {
				http.NotFound(w, r)
				return
			}
			msg := "Water is connected to Google. You can close this tab."
			status := http.StatusOK
			switch {
			case subtle.ConstantTimeCompare([]byte(cb.state), []byte(state)) != 1:
				// Anything local can reach this port; a forged callback is
				// refused without ending the real sign-in.
				msg, status = "This sign-in did not come from water. You can close this tab.", http.StatusBadRequest
			case cb.err != "":
				msg = "Google did not grant access (" + cb.err + "). You can close this tab."
			}
			if status == http.StatusOK {
				select {
				case results <- cb:
				default:
					msg, status = "This sign-in was already handled. You can close this tab.", http.StatusConflict
				}
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(status)
			fmt.Fprintf(w, "<!doctype html><title>water</title><p style=\"font-family:sans-serif\">%s</p>", html.EscapeString(msg))
		}),
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	q := url.Values{
		"client_id":              {cfg.ClientID},
		"redirect_uri":           {redirect},
		"response_type":          {"code"},
		"scope":                  {strings.Join(opts.Scopes, " ")},
		"code_challenge":         {challenge},
		"code_challenge_method":  {"S256"},
		"state":                  {state},
		"access_type":            {"offline"},
		"prompt":                 {"consent"},
		"include_granted_scopes": {"true"},
	}
	if err := open(opts.AuthURL + "?" + q.Encode()); err != nil {
		return Credential{}, fmt.Errorf("google: opening consent page: %w", err)
	}

	timer := time.NewTimer(opts.Timeout)
	defer timer.Stop()
	var cb callback
	select {
	case <-ctx.Done():
		return Credential{}, ctx.Err()
	case <-timer.C:
		return Credential{}, errors.New("google: timed out waiting for the browser sign-in")
	case cb = <-results:
	}
	if subtle.ConstantTimeCompare([]byte(cb.state), []byte(state)) != 1 {
		return Credential{}, errors.New("google: sign-in state did not match; try again")
	}
	if cb.err != "" {
		return Credential{}, fmt.Errorf("google: access not granted: %s", scrub(cb.err, cfg.ClientSecret))
	}

	form := url.Values{
		"code":          {cb.code},
		"code_verifier": {verifier},
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirect},
	}
	tr, err := postToken(ctx, opts.HTTPClient, opts.TokenURL, form, func(s string) string {
		return scrub(s, cfg.ClientSecret, cb.code, verifier)
	})
	if err != nil {
		return Credential{}, err
	}
	if tr.RefreshToken == "" {
		return Credential{}, errors.New("google: no refresh token returned; remove water at https://myaccount.google.com/permissions and connect again")
	}
	if tr.Scope != "" {
		granted := map[string]bool{}
		for _, s := range strings.Fields(tr.Scope) {
			granted[s] = true
		}
		var missing []string
		for _, s := range opts.Scopes {
			if !granted[s] {
				missing = append(missing, s)
			}
		}
		if len(missing) > 0 {
			return Credential{}, fmt.Errorf("google: access not granted for %s; connect again and tick every box", strings.Join(missing, ", "))
		}
	}
	return Credential{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RefreshToken: tr.RefreshToken}, nil
}

// Revoke revokes a refresh or access token. o may be nil.
func Revoke(ctx context.Context, token string, o *Options) error {
	opts := o.or()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, opts.RevokeURL, strings.NewReader(url.Values{"token": {token}}.Encode()))
	if err != nil {
		return fmt.Errorf("google: revoke: %s", scrub(err.Error(), token))
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := opts.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("google: revoke: %s", scrub(err.Error(), token))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	msg := strings.TrimSpace(string(body))
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return fmt.Errorf("google: revoke returned %d: %s", resp.StatusCode, scrub(strings.ToValidUTF8(msg, ""), token))
}
