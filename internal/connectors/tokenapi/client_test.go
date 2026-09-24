package tokenapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func noSleep(context.Context, time.Duration) error { return nil }

func TestGetRetriesOn5xxAndSucceeds(t *testing.T) {
	tries := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tries++
		if tries < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	cl, err := New(Credential{Token: "tok"}, BearerAuth, "example.invalid", &Options{BaseURL: srv.URL, HTTPClient: srv.Client(), Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if _, err := cl.GetJSON(context.Background(), "https://example.invalid/x", nil, nil, &out); err != nil {
		t.Fatal(err)
	}
	if tries != 3 {
		t.Fatalf("tries = %d, want 3", tries)
	}
}

func TestGetDoesNotRetry401(t *testing.T) {
	tries := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tries++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	cl, err := New(Credential{Token: "tok"}, BearerAuth, "example.invalid", &Options{BaseURL: srv.URL, HTTPClient: srv.Client(), Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_, err = cl.GetJSON(context.Background(), "https://example.invalid/x", nil, nil, &out)
	if err == nil {
		t.Fatal("expected an error")
	}
	if tries != 1 {
		t.Fatalf("tries = %d, want 1 (a 401 must never be retried — nothing to refresh)", tries)
	}
	if Status(err) != http.StatusUnauthorized {
		t.Fatalf("Status(err) = %d, want 401", Status(err))
	}
}

func TestRequestURLRefusesWrongHostWithNoBaseURLOverride(t *testing.T) {
	cl, err := New(Credential{Token: "tok"}, BearerAuth, "api.example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = cl.Get(context.Background(), "https://evil.example.com/steal", nil, nil)
	if err == nil {
		t.Fatal("expected the host mismatch to be refused")
	}
}

func TestNewRejectsEmptyToken(t *testing.T) {
	if _, err := New(Credential{}, BearerAuth, "api.example.com", nil); err == nil {
		t.Fatal("expected an error for an empty token")
	}
}

func TestCredentialNeverPrintsToken(t *testing.T) {
	c := Credential{Token: "supersecrettoken"}
	for _, s := range []string{c.String(), c.GoString(), fmt.Sprint(c)} {
		if s != redacted {
			t.Fatalf("credential rendered as %q, want %q", s, redacted)
		}
	}
	b, err := c.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"`+redacted+`"` {
		t.Fatalf("MarshalJSON = %s", b)
	}
}
