package gapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func forge(t *testing.T, authURL string) {
	t.Helper()
	pu, _ := url.Parse(authURL)
	resp, err := http.Get(pu.Query().Get("redirect_uri") + "/?code=4/attacker-code-0123456789abcdef&state=forged")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("forged callback status %d", resp.StatusCode)
	}
}

// A callback with the wrong state (any local process or web page can hit the
// loopback port) must be refused without ending the real sign-in.
func TestForgedCallbackDoesNotEndFlow(t *testing.T) {
	g := newFakeGoogle(t)
	var page string
	var status int
	real := browse(t, &page, &status)
	cred, err := Authorize(context.Background(), testClient, func(u string) error {
		forge(t, u)
		return real(u)
	}, g.opts())
	if err != nil {
		t.Fatalf("forged callback ended the flow: %v", err)
	}
	if cred.RefreshToken != testRefresh || status != 200 || g.exchanges.Load() != 1 {
		t.Fatalf("status %d exchanges %d", status, g.exchanges.Load())
	}

	o := g.opts()
	o.Timeout = 200 * time.Millisecond
	_, err = Authorize(context.Background(), testClient, func(u string) error { forge(t, u); return nil }, o)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("forged-only flow: %v", err)
	}
	if g.exchanges.Load() != 1 {
		t.Fatal("forged code was exchanged")
	}
}

// An id of "." or ".." survives url.PathEscape and would climb out of the
// resource collection once the server resolves dot segments.
func TestRefusesDotSegments(t *testing.T) {
	var hits atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer api.Close()
	c := newClient(t, newTokenServer(t), api, nil)
	for _, id := range []string{"..", "."} {
		var out map[string]any
		err := c.GetJSON(context.Background(), GmailBase+"/users/me/messages/"+url.PathEscape(id), nil, &out)
		if err == nil {
			t.Fatalf("id %q: request allowed", id)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests reached the API", hits.Load())
	}
}
