package gapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"water/internal/vault"
)

const (
	testSecret  = "GOCSPX-test-client-secret-xyz"
	testRefresh = "1//test-refresh-token-abc"
)

func testCred(t *testing.T) Credential {
	return Credential{ClientID: "cid-" + t.Name() + ".apps.googleusercontent.com", ClientSecret: testSecret, RefreshToken: testRefresh}
}

// tokenServer issues ya29.tokN access tokens and counts refreshes.
type tokenServer struct {
	*httptest.Server
	n       atomic.Int32
	expires int
	fail    string
}

func newTokenServer(t *testing.T) *tokenServer {
	ts := &tokenServer{expires: 3600}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != testRefresh || r.Form.Get("client_secret") != testSecret || r.Form.Get("client_id") == "" {
			t.Errorf("bad refresh form: %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		if ts.fail != "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":%q,"error_description":"Bad client_secret %s for token %s"}`, ts.fail, testSecret, testRefresh)
			return
		}
		n := ts.n.Add(1)
		fmt.Fprintf(w, `{"access_token":"ya29.tok%d","expires_in":%d,"token_type":"Bearer","scope":"x"}`, n, ts.expires)
	}))
	t.Cleanup(ts.Close)
	return ts
}

type sleeps struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *sleeps) sleep(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d = append(s.d, d)
	return nil
}

func (s *sleeps) all() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.d...)
}

func newClient(t *testing.T, ts *tokenServer, api *httptest.Server, sl *sleeps) *Client {
	t.Helper()
	if sl == nil {
		sl = &sleeps{}
	}
	c, err := New(testCred(t), &Options{BaseURL: api.URL, TokenURL: ts.URL, Sleep: sl.sleep})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func assertClean(t *testing.T, err error, extra ...string) {
	t.Helper()
	if err == nil {
		return
	}
	for _, s := range append([]string{testSecret, testRefresh, "ya29.", "1//", "GOCSPX-"}, extra...) {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error leaks %q: %v", s, err)
		}
	}
}

func TestCredential(t *testing.T) {
	c := testCred(t)
	s, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(s, "\n\r") {
		t.Fatalf("not single-line: %q", s)
	}
	back, err := ParseCredential(s)
	if err != nil || back != c {
		t.Fatalf("round trip: %v %v", err, back == c)
	}
	sec, _ := c.Secret()
	if fromSec, err := CredentialFromSecret(sec); err != nil || fromSec != c {
		t.Fatalf("from secret: %v", err)
	}
	if _, err := CredentialFromSecret(vault.Secret{}); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("zero secret: %v", err)
	}
	if _, err := ParseCredential(`{"client_id":"x"}`); err == nil {
		t.Fatal("incomplete credential accepted")
	}
	j, _ := json.Marshal(struct{ C Credential }{c})
	for _, out := range []string{fmt.Sprint(c), fmt.Sprintf("%+v %#v %s", c, c, c), string(j), fmt.Sprintf("%v", ClientConfig{ClientSecret: testSecret})} {
		if strings.Contains(out, testSecret) || strings.Contains(out, testRefresh) {
			t.Fatalf("credential rendered: %s", out)
		}
	}
	if len(Scopes()) != 3 || Service != "water.google" || DefaultAccount != "ceo" {
		t.Fatal("constants")
	}
}

func TestParseClientFile(t *testing.T) {
	cfg, err := ParseClientFile([]byte(`{"installed":{"client_id":"abc.apps.googleusercontent.com","project_id":"p","auth_uri":"https://accounts.google.com/o/oauth2/auth","token_uri":"https://oauth2.googleapis.com/token","client_secret":"GOCSPX-zzz","redirect_uris":["http://localhost"]}}`))
	if err != nil || cfg.ClientID != "abc.apps.googleusercontent.com" || cfg.ClientSecret != "GOCSPX-zzz" {
		t.Fatalf("%v %+v", err, cfg.ClientID)
	}
	if _, err := ParseClientFile([]byte(`{"web":{"client_id":"a","client_secret":"b"}}`)); err == nil || !strings.Contains(err.Error(), "Desktop") {
		t.Fatalf("web client: %v", err)
	}
	if _, err := ParseClientFile([]byte(`nope`)); err == nil {
		t.Fatal("bad json accepted")
	}
}

func TestRefreshCacheAndExpiry(t *testing.T) {
	ts := newTokenServer(t)
	var auths []string
	var mu sync.Mutex
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.URL.Path != "/calendar/v3/calendars/primary/events" || r.URL.Query().Get("singleEvents") != "true" || r.URL.Query().Get("a") != "1" {
			t.Errorf("unexpected request %s", r.URL)
		}
		fmt.Fprint(w, `{"items":[{"id":"e1"}]}`)
	}))
	defer api.Close()
	c := newClient(t, ts, api, nil)
	ctx := context.Background()
	var out struct{ Items []struct{ ID string } }
	for range 3 {
		if err := c.GetJSON(ctx, CalendarBase+"/calendars/primary/events?a=1", url.Values{"singleEvents": {"true"}}, &out); err != nil {
			t.Fatal(err)
		}
	}
	if out.Items[0].ID != "e1" || ts.n.Load() != 1 {
		t.Fatalf("items %v refreshes %d", out.Items, ts.n.Load())
	}
	// Another client for the same credential shares the cached token.
	c2 := newClient(t, ts, api, nil)
	if err := c2.GetJSON(ctx, CalendarBase+"/calendars/primary/events?a=1", url.Values{"singleEvents": {"true"}}, &out); err != nil || ts.n.Load() != 1 {
		t.Fatalf("shared cache: %v %d", err, ts.n.Load())
	}
	// 59 minutes later the token is inside the 60s skew and is refreshed.
	defer func(f func() time.Time) { now = f }(now)
	start := time.Now()
	now = func() time.Time { return start.Add(59*time.Minute + 1*time.Second) }
	if err := c.GetJSON(ctx, CalendarBase+"/calendars/primary/events?a=1", url.Values{"singleEvents": {"true"}}, &out); err != nil {
		t.Fatal(err)
	}
	if ts.n.Load() != 2 || auths[len(auths)-1] != "Bearer ya29.tok2" || auths[0] != "Bearer ya29.tok1" {
		t.Fatalf("refreshes %d auths %v", ts.n.Load(), auths)
	}
}

func TestConcurrentRefreshOnce(t *testing.T) {
	ts := newTokenServer(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) }))
	defer api.Close()
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := newClient(t, ts, api, nil)
			var out map[string]any
			if err := c.GetJSON(context.Background(), GmailBase+"/users/me/messages", nil, &out); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ts.n.Load() != 1 {
		t.Fatalf("refreshes %d", ts.n.Load())
	}
}

func TestUnauthorizedRefreshesOnceAndRetries(t *testing.T) {
	ts := newTokenServer(t)
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") == "Bearer ya29.tok1" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"code":401,"message":"Invalid Credentials"}}`)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer api.Close()
	sl := &sleeps{}
	c := newClient(t, ts, api, sl)
	var out struct{ OK bool }
	if err := c.GetJSON(context.Background(), DriveBase+"/files", nil, &out); err != nil || !out.OK {
		t.Fatalf("%v %v", err, out)
	}
	if ts.n.Load() != 2 || calls.Load() != 2 || len(sl.all()) != 0 {
		t.Fatalf("refreshes %d calls %d sleeps %v", ts.n.Load(), calls.Load(), sl.all())
	}

	// A 401 that persists after the refresh is returned, not looped on.
	ts2 := newTokenServer(t)
	calls.Store(0)
	always := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":{"code":401,"message":"bad token %s"}}`, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	}))
	defer always.Close()
	c = newClient(t, ts2, always, nil)
	err := c.GetJSON(context.Background(), DriveBase+"/files", nil, &out)
	if Status(err) != 401 || ts2.n.Load() != 2 || calls.Load() != 2 {
		t.Fatalf("err %v refreshes %d calls %d", err, ts2.n.Load(), calls.Load())
	}
	assertClean(t, err, "tok1", "tok2")
}

func TestBackoff(t *testing.T) {
	ts := newTokenServer(t)
	responses := []func(w http.ResponseWriter){
		func(w http.ResponseWriter) {
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"code":429,"message":"slow down"}}`)
		},
		func(w http.ResponseWriter) {
			w.WriteHeader(403)
			fmt.Fprint(w, `{"error":{"code":403,"message":"Rate Limit Exceeded","errors":[{"reason":"userRateLimitExceeded"}]}}`)
		},
		func(w http.ResponseWriter) { w.WriteHeader(503); fmt.Fprint(w, `oops`) },
		func(w http.ResponseWriter) { fmt.Fprint(w, `{"ok":true}`) },
	}
	var i atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		responses[i.Add(1)-1](w)
	}))
	defer api.Close()
	sl := &sleeps{}
	c := newClient(t, ts, api, sl)
	var out struct{ OK bool }
	if err := c.GetJSON(context.Background(), GmailBase+"/users/me/messages", nil, &out); err != nil || !out.OK {
		t.Fatalf("%v %v", err, out)
	}
	d := sl.all()
	if len(d) != 3 {
		t.Fatalf("sleeps %v", d)
	}
	for k, got := range d {
		lo, hi := backoffBase<<k/2, backoffBase<<k
		if got < lo || got >= hi {
			t.Fatalf("sleep %d = %v, want [%v,%v)", k, got, lo, hi)
		}
	}

	// Always 500: gives up after maxTries.
	var n atomic.Int32
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(500)
		fmt.Fprint(w, `{"error":{"code":500,"message":"Backend Error"}}`)
	}))
	defer down.Close()
	sl = &sleeps{}
	c = newClient(t, ts, down, sl)
	err := c.GetJSON(context.Background(), GmailBase+"/users/me/messages", nil, &out)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 500 || ae.Message != "Backend Error" || n.Load() != maxTries || len(sl.all()) != maxTries-1 {
		t.Fatalf("err %v calls %d sleeps %v", err, n.Load(), sl.all())
	}
	if sl.all()[0] < 2*time.Second {
		t.Fatalf("Retry-After ignored: %v", sl.all())
	}

	// A plain 403 is not retried.
	n.Store(0)
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(403)
		fmt.Fprint(w, `{"error":{"code":403,"message":"Insufficient Permission","errors":[{"reason":"insufficientPermissions"}]}}`)
	}))
	defer forbidden.Close()
	sl = &sleeps{}
	c = newClient(t, ts, forbidden, sl)
	err = c.GetJSON(context.Background(), DriveBase+"/files", nil, &out)
	if !errors.As(err, &ae) || ae.Reason != "insufficientPermissions" || n.Load() != 1 || len(sl.all()) != 0 {
		t.Fatalf("err %v calls %d", err, n.Load())
	}
}

func TestInvalidGrant(t *testing.T) {
	ts := newTokenServer(t)
	ts.fail = "invalid_grant"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("API reached without a token") }))
	defer api.Close()
	c := newClient(t, ts, api, nil)
	var out any
	err := c.GetJSON(context.Background(), DriveBase+"/files", nil, &out)
	if !errors.Is(err, ErrReconnect) || !strings.Contains(err.Error(), "water connect google") {
		t.Fatalf("got %v", err)
	}
	assertClean(t, err)
	if err := c.Refresh(context.Background()); !errors.Is(err, ErrReconnect) {
		t.Fatalf("refresh: %v", err)
	}

	ts.fail = "invalid_client"
	err = c.Refresh(context.Background())
	if err == nil || errors.Is(err, ErrReconnect) || !strings.Contains(err.Error(), "invalid_client") {
		t.Fatalf("invalid_client: %v", err)
	}
	assertClean(t, err)
}

func TestErrorsNeverCarrySecrets(t *testing.T) {
	ts := newTokenServer(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		fmt.Fprintf(w, `{"error":{"code":400,"message":"bad %s / %s / %s / %s","errors":[{"reason":"%s"}]}}`, tok, testRefresh, testSecret, url.QueryEscape(testRefresh), tok)
	}))
	defer api.Close()
	c := newClient(t, ts, api, nil)
	var out any
	err := c.GetJSON(context.Background(), GmailBase+"/users/me/messages", nil, &out)
	if Status(err) != 400 || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("got %v", err)
	}
	assertClean(t, err, "tok1", url.QueryEscape(testRefresh))

	// Non-JSON success bodies fail to decode without echoing content.
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.Header.Get("Authorization"))
	}))
	defer junk.Close()
	c = newClient(t, ts, junk, nil)
	err = c.GetJSON(context.Background(), GmailBase+"/users/me/messages", nil, &out)
	if err == nil {
		t.Fatal("junk decoded")
	}
	assertClean(t, err, "tok1")

	// Transport failures.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	c = newClient(t, ts, dead, nil)
	err = c.GetJSON(context.Background(), GmailBase+"/users/me/messages?q="+url.QueryEscape(testSecret), nil, &out)
	if err == nil {
		t.Fatal("dead server answered")
	}
	assertClean(t, err, "tok1")
}

func TestRefusesNonGoogleHosts(t *testing.T) {
	ts := newTokenServer(t)
	c, err := New(testCred(t), &Options{TokenURL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	var out any
	for _, u := range []string{"http://www.googleapis.com/drive/v3/files", "https://evil.example/x", "https://googleapis.com.evil.example/x"} {
		if err := c.GetJSON(context.Background(), u, nil, &out); err == nil || !strings.Contains(err.Error(), "refusing") {
			t.Fatalf("%s: %v", u, err)
		}
	}
	if ts.n.Load() != 0 {
		t.Fatal("token fetched for a refused host")
	}
}

func TestSizeCaps(t *testing.T) {
	ts := newTokenServer(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"text":"`+strings.Repeat("é", 100)+`"}`)
	}))
	defer api.Close()
	c, err := New(testCred(t), &Options{BaseURL: api.URL, TokenURL: ts.URL, MaxBytes: 50})
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := c.GetJSON(context.Background(), DriveBase+"/files/x", nil, &out); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("cap: %v", err)
	}
	s, truncated, err := c.GetText(context.Background(), DriveBase+"/files/x/export", url.Values{"mimeType": {"text/plain"}}, 20)
	if err != nil || !truncated || len(s) > 20 || !strings.HasPrefix(s, `{"text":"é`) || strings.ContainsRune(s, '�') {
		t.Fatalf("text %q %v %v", s, truncated, err)
	}
	b, truncated, err := c.GetBytes(context.Background(), DriveBase+"/files/x/export", nil, 1000)
	if err != nil || truncated || len(b) != 211 {
		t.Fatalf("bytes %d %v %v", len(b), truncated, err)
	}
}

// fakeGoogle serves the consent endpoint (which redirects straight back with
// a code) and the code exchange.
type fakeGoogle struct {
	auth, token *httptest.Server
	challenge   string
	exchanges   atomic.Int32
	noRefresh   bool
	scope       string
	redirectQ   func(q url.Values) url.Values
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	g := &fakeGoogle{scope: strings.Join(Scopes(), " ")}
	g.auth = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		want := map[string]string{"client_id": "cid", "response_type": "code", "code_challenge_method": "S256", "access_type": "offline", "prompt": "consent", "include_granted_scopes": "true", "scope": strings.Join(Scopes(), " ")}
		for k, v := range want {
			if q.Get(k) != v {
				t.Errorf("auth %s = %q, want %q", k, q.Get(k), v)
			}
		}
		if !strings.HasPrefix(q.Get("redirect_uri"), "http://127.0.0.1:") || len(q.Get("state")) < 32 || len(q.Get("code_challenge")) != 43 {
			t.Errorf("auth params %v", q)
		}
		g.challenge = q.Get("code_challenge")
		back := url.Values{"code": {"4/0AanRRrt-the-auth-code-value-123456"}, "state": {q.Get("state")}, "scope": {q.Get("scope")}}
		if g.redirectQ != nil {
			back = g.redirectQ(back)
		}
		http.Redirect(w, r, q.Get("redirect_uri")+"/?"+back.Encode(), http.StatusFound)
	}))
	g.token = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.exchanges.Add(1)
		_ = r.ParseForm()
		f := r.Form
		sum := sha256.Sum256([]byte(f.Get("code_verifier")))
		if f.Get("grant_type") != "authorization_code" || f.Get("code") != "4/0AanRRrt-the-auth-code-value-123456" || f.Get("client_id") != "cid" ||
			f.Get("client_secret") != testSecret || !strings.HasPrefix(f.Get("redirect_uri"), "http://127.0.0.1:") ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge || len(f.Get("code_verifier")) < 43 {
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error":"invalid_request","error_description":"bad exchange for %s"}`, f.Get("client_secret"))
			return
		}
		rt := `,"refresh_token":"` + testRefresh + `"`
		if g.noRefresh {
			rt = ""
		}
		fmt.Fprintf(w, `{"access_token":"ya29.first","expires_in":3599,"scope":%q,"token_type":"Bearer"%s}`, g.scope, rt)
	}))
	t.Cleanup(g.auth.Close)
	t.Cleanup(g.token.Close)
	return g
}

func (g *fakeGoogle) opts() *Options {
	return &Options{AuthURL: g.auth.URL, TokenURL: g.token.URL, Timeout: 5 * time.Second}
}

var testClient = ClientConfig{ClientID: "cid", ClientSecret: testSecret}

// browse follows the consent URL like a browser, landing on the loopback page.
func browse(_ *testing.T, page *string, status *int) func(string) error {
	return func(authURL string) error {
		resp, err := http.Get(authURL)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		*page, *status = string(b), resp.StatusCode
		return nil
	}
}

func TestAuthorizeEndToEnd(t *testing.T) {
	g := newFakeGoogle(t)
	var page string
	var status int
	cred, err := Authorize(context.Background(), testClient, browse(t, &page, &status), g.opts())
	if err != nil {
		t.Fatal(err)
	}
	if cred.RefreshToken != testRefresh || cred.ClientID != "cid" || cred.ClientSecret != testSecret {
		t.Fatal("wrong credential")
	}
	if status != 200 || !strings.Contains(page, "close this tab") {
		t.Fatalf("page %d %q", status, page)
	}
	if g.exchanges.Load() != 1 {
		t.Fatal("no exchange")
	}
}

func TestAuthorizeRejects(t *testing.T) {
	cases := []struct {
		name  string
		setup func(g *fakeGoogle)
		want  string
		code  int
	}{
		{"bad state", func(g *fakeGoogle) {
			g.redirectQ = func(q url.Values) url.Values { q.Set("state", "forged"); return q }
		}, "state", 400},
		{"denied", func(g *fakeGoogle) {
			g.redirectQ = func(q url.Values) url.Values {
				return url.Values{"error": {"access_denied"}, "state": q["state"]}
			}
		}, "access_denied", 200},
		{"no refresh token", func(g *fakeGoogle) { g.noRefresh = true }, "no refresh token", 200},
		{"scope unticked", func(g *fakeGoogle) { g.scope = ScopeCalendarEventsReadonly }, ScopeGmailReadonly, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newFakeGoogle(t)
			tc.setup(g)
			var page string
			var status int
			_, err := Authorize(context.Background(), testClient, browse(t, &page, &status), g.opts())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v", err)
			}
			if status != tc.code {
				t.Fatalf("status %d", status)
			}
			if tc.name == "bad state" || tc.name == "denied" {
				if g.exchanges.Load() != 0 {
					t.Fatal("code exchanged")
				}
			}
			assertClean(t, err, "4/0AanRRrt")
		})
	}
}

func TestAuthorizeExchangeErrorAndTimeout(t *testing.T) {
	g := newFakeGoogle(t)
	var page string
	var status int
	_, err := Authorize(context.Background(), ClientConfig{ClientID: "cid", ClientSecret: "GOCSPX-other-secret"}, browse(t, &page, &status), g.opts())
	if err == nil || !strings.Contains(err.Error(), "invalid_request") {
		t.Fatalf("got %v", err)
	}
	assertClean(t, err, "GOCSPX-other-secret", "4/0AanRRrt")

	o := g.opts()
	o.Timeout = 50 * time.Millisecond
	_, err = Authorize(context.Background(), testClient, func(string) error { return nil }, o)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Authorize(ctx, testClient, func(string) error { return nil }, g.opts()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestLoopbackOnly(t *testing.T) {
	g := newFakeGoogle(t)
	_, _ = Authorize(context.Background(), testClient, func(u string) error {
		pu, _ := url.Parse(u)
		redirect, _ := url.Parse(pu.Query().Get("redirect_uri"))
		if redirect.Hostname() != "127.0.0.1" || redirect.Port() == "" || redirect.Path != "" {
			t.Errorf("redirect %s", redirect)
		}
		// Stray requests do not end the flow.
		resp, err := http.Get(redirect.String() + "/favicon.ico")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != 404 {
				t.Errorf("favicon %d", resp.StatusCode)
			}
		}
		return errors.New("stop")
	}, g.opts())
}

func TestRevoke(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.Form.Get("token")
		if got == "bad" {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_token","error_description":"Token bad expired"}`)
		}
	}))
	defer srv.Close()
	if err := Revoke(context.Background(), testRefresh, &Options{RevokeURL: srv.URL}); err != nil || got != testRefresh {
		t.Fatalf("%v %q", err, got)
	}
	err := Revoke(context.Background(), "bad", &Options{RevokeURL: srv.URL})
	if err == nil || !strings.Contains(err.Error(), "invalid_token") {
		t.Fatalf("%v", err)
	}
}

func TestArgs(t *testing.T) {
	args := map[string]any{"max": json.Number("250"), "f": 3.0, "bad": "x", "frac": json.Number("1.5"), "q": "  from:dana  "}
	if n, err := ArgInt(args, "max", 10, 1, 100); err != nil || n != 100 {
		t.Fatal(n, err)
	}
	if n, err := ArgInt(args, "f", 10, 1, 100); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	if n, err := ArgInt(args, "missing", 10, 1, 100); err != nil || n != 10 {
		t.Fatal(n, err)
	}
	if _, err := ArgInt(args, "bad", 10, 1, 100); err == nil {
		t.Fatal("string accepted")
	}
	if _, err := ArgInt(args, "frac", 10, 1, 100); err == nil {
		t.Fatal("fraction accepted")
	}
	if ArgString(args, "q") != "from:dana" || ArgString(args, "max") != "" {
		t.Fatal("ArgString")
	}
}
