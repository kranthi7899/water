package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHarnessWriteLeafSymlinkCannotEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "untouched.txt")
	if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "output.txt")); err != nil {
		t.Fatal(err)
	}
	p := &Policy{Role: "ceo", Filesystem: FSPolicy{Mode: "read-write", Roots: []string{root}}}
	if dec, _ := p.Authorize(ToolWriteFile, map[string]any{"path": "output.txt", "content": "changed"}); dec.Allowed {
		t.Fatalf("leaf symlink write was authorized: %+v", dec)
	}
}

func TestHarnessApprovalDisconnectReleasesHandler(t *testing.T) {
	b := &ApprovalBroker{reqs: make(chan *PendingApproval, 1), done: make(chan struct{})}
	defer close(b.done)
	server, client := net.Pipe()
	done := make(chan struct{})
	go func() { b.handle(server); close(done) }()
	if err := json.NewEncoder(client).Encode(ApprovalRequest{CallID: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("disconnected approval left its handler waiting for a decision")
	}
	if _, ok := b.Next(); ok {
		t.Fatal("disconnected request still offered for approval")
	}
}

func TestHarnessCancelledWriteDoesNotExecute(t *testing.T) {
	root := t.TempDir()
	svc := NewService(&Policy{Role: "ceo", Filesystem: FSPolicy{Mode: "read-write", Roots: []string{root}}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svc.execute(ctx, ToolWriteFile, map[string]any{"path": filepath.Join(root, "cancelled.txt"), "content": "no"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled call error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "cancelled.txt")); !os.IsNotExist(err) {
		t.Fatalf("cancelled write touched disk: %v", err)
	}
}

func TestHarnessSandboxProtectsStateWithinWorkspace(t *testing.T) {
	root := t.TempDir()
	protected := filepath.Join(root, ".water")
	profile := SandboxProfile(&Policy{Filesystem: FSPolicy{Mode: "read-write", Roots: []string{root}}, Protected: []string{protected}})
	if !strings.Contains(profile, "(deny file-read* file-write*") || !strings.Contains(profile, protected) {
		t.Fatalf("shell sandbox omits protected state: %s", profile)
	}
}
