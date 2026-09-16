package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMCPHungCallDoesNotBlockServer — a tool call that never finishes must not
// block ping or other calls, must end with a timeout error, and closing stdin
// must stop the server even while that call is still stuck.
func TestMCPHungCallDoesNotBlockServer(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "ok.txt"), []byte("fine"), 0o644)
	svc := NewService(&Policy{Role: "cto", Filesystem: FSPolicy{Mode: "read-only", Roots: []string{root}}}, nil)
	svc.CallTimeout = 400 * time.Millisecond
	svc.testDelay = 10 * time.Second

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeStdio(context.Background(), inR, outW, svc) }()
	replies := make(chan map[string]any, 8)
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				replies <- m
			}
		}
	}()
	send := func(s string) { _, _ = inW.Write([]byte(s + "\n")) }
	send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"ok.txt"}}}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)

	start := time.Now()
	got := map[float64]map[string]any{}
	for len(got) < 2 && time.Since(start) < 3*time.Second {
		select {
		case m := <-replies:
			got[m["id"].(float64)] = m
			if m["id"].(float64) == 2 && time.Since(start) > 300*time.Millisecond {
				t.Fatalf("ping answered only after %s: blocked behind the slow call", time.Since(start))
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	if _, ok := got[2]; !ok {
		t.Fatal("ping never answered")
	}
	call, ok := got[1]
	if !ok {
		t.Fatal("slow call never returned a timeout result")
	}
	res := call["result"].(map[string]any)
	if res["isError"] != true || !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), "deadline") {
		t.Fatalf("slow call result: %v", res)
	}
	inW.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("server kept running after stdin closed")
	}
}

func TestMCPCancelNotificationStopsMatchingCall(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("fine"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := NewService(&Policy{Role: "cto", Filesystem: FSPolicy{Mode: "read-only", Roots: []string{root}}}, nil)
	svc.CallTimeout = 5 * time.Second
	svc.testDelay = 10 * time.Second

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeStdio(context.Background(), inR, outW, svc) }()
	replies := make(chan map[string]any, 8)
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				replies <- m
			}
		}
	}()
	send := func(s string) { _, _ = inW.Write([]byte(s + "\n")) }
	send(`{"jsonrpc":"2.0","id":"slow","method":"tools/call","params":{"name":"read_file","arguments":{"path":"ok.txt"}}}`)
	send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"slow","reason":"user cancelled"}}`)

	select {
	case msg := <-replies:
		if msg["id"] != "slow" {
			t.Fatalf("unexpected reply: %v", msg)
		}
		res := msg["result"].(map[string]any)
		text := res["content"].([]any)[0].(map[string]any)["text"].(string)
		if res["isError"] != true || !strings.Contains(text, "context canceled") {
			t.Fatalf("cancelled call result: %v", res)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel notification did not stop the call")
	}
	inW.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("server kept running after stdin closed")
	}
}

// TestReadNonRegularFileRefused — a named pipe under a root is refused
// immediately instead of blocking open() forever.
func TestReadNonRegularFileRefused(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skip("mkfifo unavailable:", err)
	}
	svc := NewService(&Policy{Role: "cto", Filesystem: FSPolicy{Mode: "read-only", Roots: []string{root}}}, nil)
	start := time.Now()
	_, err := svc.Call(context.Background(), ToolReadFile, map[string]any{"path": "pipe"})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("fifo read: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("fifo refusal was not immediate")
	}
}

// TestShellAllowlistRefusesCompound — "ls" in the allowlist must not authorise
// chaining, pipes or substitution; confirm-each is refused (no approval
// channel); and no shell runs at all where no OS sandbox exists.
func TestShellAllowlistRefusesCompound(t *testing.T) {
	orig := SandboxAvailable
	defer func() { SandboxAvailable = orig }()
	SandboxAvailable = func() bool { return true }
	p := &Policy{Role: "cto", Filesystem: FSPolicy{Mode: "read-only", Roots: []string{t.TempDir()}}, Shell: ShellPolicy{Mode: "allowlist", Allowlist: []string{"ls"}}}
	for _, c := range []string{"ls && touch x", "ls; rm -rf /", "ls $(touch x)", "ls `touch x`", "ls | sh", "ls > out", "ls\ntouch x"} {
		if d, _ := p.Authorize(ToolRun, map[string]any{"command": c}); d.Allowed {
			t.Fatalf("%q was allowed: %s", c, d.Basis)
		}
	}
	d, out := p.Authorize(ToolRun, map[string]any{"command": "ls -la"})
	if !d.Allowed || len(out["argv"].([]string)) != 2 {
		t.Fatalf("simple allowlisted command: %+v %v", d, out)
	}
	p.Shell.Mode = "confirm-each"
	if d, _ := p.Authorize(ToolRun, map[string]any{"command": "ls"}); d.Allowed {
		t.Fatal("confirm-each allowed without an approval channel")
	}
	SandboxAvailable = func() bool { return false }
	p.Shell.Mode = "unrestricted"
	if d, _ := p.Authorize(ToolRun, map[string]any{"command": "ls"}); d.Allowed || !strings.Contains(d.Basis, "no OS sandbox") {
		t.Fatalf("shell allowed without a sandbox: %+v", d)
	}
}
