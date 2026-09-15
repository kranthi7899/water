package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestApprovalBrokerCorrelatesOneRequest(t *testing.T) {
	b, err := NewApprovalBroker()
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("the surrounding test sandbox forbids Unix-domain sockets")
		}
		t.Fatal(err)
	}
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan struct {
		allowed bool
		err     error
	}, 1)
	go func() {
		allowed, err := RequestApproval(ctx, b.Socket(), ApprovalRequest{CallID: "call-1", Role: "ceo", Tool: ToolWriteFile, Args: map[string]any{"path": "/tmp/out.txt"}})
		result <- struct {
			allowed bool
			err     error
		}{allowed, err}
	}()
	deadline := time.Now().Add(time.Second)
	var p *PendingApproval
	for p == nil && time.Now().Before(deadline) {
		p, _ = b.Next()
		time.Sleep(time.Millisecond)
	}
	if p == nil || p.Request.CallID != "call-1" || p.Request.Tool != ToolWriteFile {
		t.Fatalf("approval request = %+v", p)
	}
	p.Decide(true)
	if got := <-result; got.err != nil || !got.allowed {
		t.Fatalf("approval result = %+v", got)
	}
}

func TestWriteRequiresInteractiveApproval(t *testing.T) {
	b, err := NewApprovalBroker()
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("the surrounding test sandbox forbids Unix-domain sockets")
		}
		t.Fatal(err)
	}
	defer b.Close()
	root := t.TempDir()
	p := &Policy{Role: "ceo", Filesystem: FSPolicy{Mode: "read-write", Roots: []string{root}}, ApprovalSocket: b.Socket()}
	svc := NewService(p, nil)
	call := func(content string) <-chan error {
		out := make(chan error, 1)
		go func() {
			_, err := svc.Call(context.Background(), ToolWriteFile, map[string]any{"path": "brief.txt", "content": content})
			out <- err
		}()
		return out
	}

	denied := call("no")
	pending := waitApproval(t, b)
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Request.Args["path"] != filepath.Join(canonicalRoot, "brief.txt") {
		t.Fatalf("approval path = %#v", pending.Request.Args)
	}
	pending.Decide(false)
	if err := <-denied; err == nil {
		t.Fatal("denied write succeeded")
	}
	if _, err := os.Stat(filepath.Join(root, "brief.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied write touched disk: %v", err)
	}

	allowed := call("yes")
	waitApproval(t, b).Decide(true)
	if err := <-allowed; err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "brief.txt")); err != nil || string(got) != "yes" {
		t.Fatalf("approved write = %q, %v", got, err)
	}
	evs := svc.Events()
	if len(evs) != 2 || evs[0].Allowed || !evs[1].Allowed || evs[1].Basis == "" {
		t.Fatalf("approval audit events = %+v", evs)
	}
}

func waitApproval(t *testing.T, b *ApprovalBroker) *PendingApproval {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if p, ok := b.Next(); ok {
			return p
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for approval")
	return nil
}
