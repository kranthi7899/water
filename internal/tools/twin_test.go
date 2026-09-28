package tools

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// fakeDaemon serves POST /v1/tools/invoke over a Unix socket, standing in for
// the real daemon's gate endpoint so this package's twin-proxy code can be
// tested without ever building the gateway or gate packages here (tools must
// not import them — the dependency runs the other way).
func fakeDaemon(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (socketPath string) {
	t.Helper()
	// A short-lived dir outside t.TempDir(): the unix socket path length
	// limit (~104 bytes on macOS) is easily blown by TempDir's long,
	// test-name-qualified paths.
	dir, err := os.MkdirTemp("", "water-twin-sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socketPath = filepath.Join(dir, "s.sock")
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/tools/invoke", handler)
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	t.Cleanup(func() {
		srv.Close()
		os.Remove(socketPath)
	})
	return socketPath
}

func twinPolicy(socket, token string) *Policy {
	schema, _ := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{}})
	return &Policy{
		Role:       "ceo",
		Twin:       []TwinFunction{{ID: "fake_mail.send_email", Tool: "fake_mail__send_email", Description: "Send an email.", Schema: schema}},
		TwinSocket: socket,
		TwinToken:  token,
	}
}

func TestTwinToolListedAndAuthorized(t *testing.T) {
	pol := twinPolicy("/nonexistent.sock", "tok")
	if !containsStr(pol.ToolNames(), "fake_mail__send_email") {
		t.Fatalf("ToolNames() = %v, missing the twin tool", pol.ToolNames())
	}
	dec, _ := pol.Authorize("fake_mail__send_email", map[string]any{"to": []any{"a@b.com"}})
	if !dec.Allowed {
		t.Fatalf("twin tool call denied: %s", dec.Basis)
	}
	svc := NewService(pol, nil)
	defs := svc.Definitions()
	if len(defs) != 1 || defs[0].Name != "fake_mail__send_email" || defs[0].Description != "Send an email." {
		t.Fatalf("Definitions() = %+v", defs)
	}
}

func TestTwinCallOKIsReturnedVerbatim(t *testing.T) {
	socket := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var body struct {
			Function string         `json:"function"`
			Args     map[string]any `json:"args"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Function != "fake_mail.send_email" {
			t.Errorf("daemon saw function %q", body.Function)
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "output": json.RawMessage(`{"id":"sent1"}`)})
	})
	svc := NewService(twinPolicy(socket, "secret-tok"), nil)
	out, err := svc.Call(context.Background(), "fake_mail__send_email", map[string]any{"to": []any{"a@b.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubstr(out, `"id":"sent1"`) {
		t.Fatalf("out = %q", out)
	}
}

func TestTwinCallQueuedForApprovalDoesNotError(t *testing.T) {
	socket := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": "queued", "approval_id": "env_abc123"})
	})
	svc := NewService(twinPolicy(socket, "tok"), nil)
	out, err := svc.Call(context.Background(), "fake_mail__send_email", map[string]any{})
	if err != nil {
		t.Fatalf("a queued call must not be an error: %v", err)
	}
	if !containsSubstr(out, "env_abc123") {
		t.Fatalf("out = %q, want it to name the approval id", out)
	}
}

func TestTwinCallDeniedIsAnError(t *testing.T) {
	socket := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": "denied", "reason": "rate cap reached"})
	})
	svc := NewService(twinPolicy(socket, "tok"), nil)
	_, err := svc.Call(context.Background(), "fake_mail__send_email", map[string]any{})
	if err == nil {
		t.Fatal("expected a denial error")
	}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func containsSubstr(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
