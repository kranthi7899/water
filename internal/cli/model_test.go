package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"water/internal/nervous/sidecar"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// --- pullModel (the download+verify mechanics), tested directly ---

func TestPullModelSuccessVerifiesAndInstalls(t *testing.T) {
	content := []byte("pretend-gguf-bytes-for-testing-only")
	want := sha256Hex(content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	home := t.TempDir()
	var out bytes.Buffer
	if err := pullModel(context.Background(), srv.URL, want, home, "", &out); err != nil {
		t.Fatalf("pullModel: %v", err)
	}

	finalPath := filepath.Join(home, "models", sidecar.ModelFileName)
	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("reading installed file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("installed content mismatch: got %q want %q", got, content)
	}

	partPath := filepath.Join(home, "models", sidecar.ModelPartFileName)
	if _, err := os.Stat(partPath); !os.IsNotExist(err) {
		t.Fatalf("expected .part file to be gone after success, stat err = %v", err)
	}
}

func TestPullModelSHA256MismatchCleansUpPartFile(t *testing.T) {
	content := []byte("some bytes that will not match")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	home := t.TempDir()
	var out bytes.Buffer
	err := pullModel(context.Background(), srv.URL, "0000000000000000000000000000000000000000000000000000000000000000", home, "", &out)
	if err == nil {
		t.Fatal("expected a sha256 mismatch error")
	}

	finalPath := filepath.Join(home, "models", sidecar.ModelFileName)
	if _, statErr := os.Stat(finalPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected no installed file after mismatch, stat err = %v", statErr)
	}
	partPath := filepath.Join(home, "models", sidecar.ModelPartFileName)
	if _, statErr := os.Stat(partPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected .part file to be cleaned up after mismatch, stat err = %v", statErr)
	}
}

func TestPullModelNoTokenSendsNoAuthHeader(t *testing.T) {
	content := []byte("ok")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("expected no Authorization header, got %q", got)
		}
		w.Write(content)
	}))
	defer srv.Close()

	home := t.TempDir()
	var out bytes.Buffer
	if err := pullModel(context.Background(), srv.URL, sha256Hex(content), home, "", &out); err != nil {
		t.Fatalf("pullModel: %v", err)
	}
}

func TestPullModelSendsBearerToken(t *testing.T) {
	content := []byte("gated-ok")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("Authorization header = %q, want %q", got, "Bearer secret-token")
		}
		w.Write(content)
	}))
	defer srv.Close()

	home := t.TempDir()
	var out bytes.Buffer
	if err := pullModel(context.Background(), srv.URL, sha256Hex(content), home, "secret-token", &out); err != nil {
		t.Fatalf("pullModel: %v", err)
	}
}

func TestPullModel401WithoutTokenMentionsHFToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	home := t.TempDir()
	var out bytes.Buffer
	err := pullModel(context.Background(), srv.URL, "irrelevant", home, "", &out)
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("HF_TOKEN")) {
		t.Fatalf("error %q should mention HF_TOKEN", err.Error())
	}
	if _, statErr := os.Stat(filepath.Join(home, "models", sidecar.ModelPartFileName)); !os.IsNotExist(statErr) {
		t.Fatal("expected no .part file to be created on a 401 before any bytes were written")
	}
}

func TestPullModel401WithTokenReportsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	home := t.TempDir()
	var out bytes.Buffer
	err := pullModel(context.Background(), srv.URL, "irrelevant", home, "bad-token", &out)
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("401")) {
		t.Fatalf("error %q should mention the 401 status", err.Error())
	}
}

func TestPullModel403MentionsLicenseAndToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	home := t.TempDir()
	var out bytes.Buffer
	err := pullModel(context.Background(), srv.URL, "irrelevant", home, "", &out)
	if err == nil {
		t.Fatal("expected an error for a 403 response")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("403")) || !bytes.Contains([]byte(err.Error()), []byte("HF_TOKEN")) {
		t.Fatalf("error %q should mention 403 and HF_TOKEN", err.Error())
	}
}

// --- `water model pull functiongemma` cobra command, end to end ---

func withOverriddenModelVars(t *testing.T, url string, sha256 string) {
	t.Helper()
	prevURL, prevSHA := modelDownloadURL, modelExpectedSHA256
	modelDownloadURL = func() string { return url }
	modelExpectedSHA256 = sha256
	t.Cleanup(func() {
		modelDownloadURL = prevURL
		modelExpectedSHA256 = prevSHA
	})
}

func TestModelPullRefusesWithoutAcceptFlagBeforeAnyNetworkCall(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte("should never be requested"))
	}))
	defer srv.Close()
	withOverriddenModelVars(t, srv.URL, "irrelevant")
	t.Setenv("WATER_HOME", t.TempDir())

	a := NewApp()
	cmd := a.modelPullCmd()
	cmd.SetArgs([]string{"functiongemma"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error refusing to pull without --accept-gemma-terms")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("accept-gemma-terms")) {
		t.Fatalf("error %q should mention --accept-gemma-terms", err.Error())
	}
	if hits != 0 {
		t.Fatalf("expected no network calls before accepting terms, got %d", hits)
	}
}

func TestModelPullWithAcceptFlagDownloadsAndReadsHFToken(t *testing.T) {
	content := []byte("full-end-to-end-fixture-bytes")
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write(content)
	}))
	defer srv.Close()
	withOverriddenModelVars(t, srv.URL, sha256Hex(content))

	home := t.TempDir()
	t.Setenv("WATER_HOME", home)
	t.Setenv("HF_TOKEN", "end-to-end-token")

	a := NewApp()
	cmd := a.modelPullCmd()
	cmd.SetArgs([]string{"functiongemma", "--accept-gemma-terms"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotAuth != "Bearer end-to-end-token" {
		t.Fatalf("Authorization header = %q, want Bearer end-to-end-token", gotAuth)
	}
	installed := filepath.Join(home, "models", sidecar.ModelFileName)
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("expected installed model file: %v", err)
	}
}

func TestModelPullUnknownModelName(t *testing.T) {
	t.Setenv("WATER_HOME", t.TempDir())
	a := NewApp()
	cmd := a.modelPullCmd()
	cmd.SetArgs([]string{"not-a-real-model", "--accept-gemma-terms"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for an unknown model name")
	}
}
