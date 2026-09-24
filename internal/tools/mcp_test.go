package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// slowFakeDaemon serves POST /v1/tools/invoke on a Unix socket, replying only
// after delay (or never, if delay is negative) — a stand-in for a daemon call
// that hangs or takes too long, so the MCP server's own deadline and cancel
// handling can be tested without a network dependency.
func slowFakeDaemon(t *testing.T, delay time.Duration) (socketPath string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "water-mcp-sock")
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
	mux.HandleFunc("/v1/tools/invoke", func(w http.ResponseWriter, r *http.Request) {
		if delay < 0 {
			<-r.Context().Done() // never reply; wait for the client to give up
			return
		}
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "output": json.RawMessage(`"fine"`)})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return socketPath
}

func slowTwinPolicy(socket string) *Policy {
	schema, _ := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{}})
	return &Policy{
		Role:       "ceo",
		Twin:       []TwinFunction{{ID: "fake_docs.read_doc", Tool: "fake_docs__read_doc", Description: "Read a document.", Schema: schema}},
		TwinSocket: socket,
		TwinToken:  "tok",
	}
}

// TestMCPHungCallDoesNotBlockServer — a tool call that never finishes must not
// block ping or other calls, must end with a timeout error, and closing stdin
// must stop the server even while that call is still stuck.
func TestMCPHungCallDoesNotBlockServer(t *testing.T) {
	socket := slowFakeDaemon(t, -1)
	svc := NewService(slowTwinPolicy(socket), nil)
	svc.CallTimeout = 400 * time.Millisecond

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
	send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fake_docs__read_doc","arguments":{"id":"d1"}}}`)
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
	if res["isError"] != true {
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
	socket := slowFakeDaemon(t, -1)
	svc := NewService(slowTwinPolicy(socket), nil)
	svc.CallTimeout = 5 * time.Second

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
	send(`{"jsonrpc":"2.0","id":"slow","method":"tools/call","params":{"name":"fake_docs__read_doc","arguments":{"id":"d1"}}}`)
	send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"slow","reason":"user cancelled"}}`)

	select {
	case msg := <-replies:
		if msg["id"] != "slow" {
			t.Fatalf("unexpected reply: %v", msg)
		}
		res := msg["result"].(map[string]any)
		if res["isError"] != true {
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

// TestTwinCallSucceedsThroughMCP is the happy path end to end: tools/call →
// Service.Call → the twin proxy → a real (fast) fake daemon.
func TestTwinCallSucceedsThroughMCP(t *testing.T) {
	socket := slowFakeDaemon(t, 0)
	svc := NewService(slowTwinPolicy(socket), nil)

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
	_, _ = inW.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fake_docs__read_doc","arguments":{"id":"d1"}}}` + "\n"))

	select {
	case msg := <-replies:
		res := msg["result"].(map[string]any)
		if res["isError"] == true {
			t.Fatalf("unexpected error: %v", res)
		}
		text := res["content"].([]any)[0].(map[string]any)["text"].(string)
		if !strings.Contains(text, "fine") {
			t.Fatalf("text = %q", text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no reply")
	}
	inW.Close()
	<-done
}
