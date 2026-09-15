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

func TestInteractivePlanGetsOneApprovalAndExecutesExactActions(t *testing.T) {
	if !SandboxAvailable() {
		// The plan includes a run action, and shell.mode=confirm-each is
		// refused outright wherever no OS sandbox exists (macOS only, see
		// docs/decisions.md) — before any approval is ever requested. On such
		// a host the test's own wait for that prompt would time out, not
		// because approval is broken but because the run action is correctly
		// refused pre-prompt. Skip rather than fake a capability this host
		// doesn't have.
		t.Skip("no OS sandbox on this platform; run actions are refused pre-prompt by design")
	}
	b, err := NewApprovalBroker()
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("the surrounding test sandbox forbids Unix-domain sockets")
		}
		t.Fatal(err)
	}
	defer b.Close()
	root := t.TempDir()
	svc := NewService(InteractiveWorkspacePolicy("ceo", "id", root, t.TempDir(), b.Socket()), nil)
	done := make(chan error, 1)
	go func() {
		_, err := svc.Call(context.Background(), ToolApplyActions, map[string]any{
			"summary": "Create a short brief and verify it",
			"actions": []any{
				map[string]any{"tool": ToolWriteFile, "args": map[string]any{"path": "brief.txt", "content": "approved"}},
				map[string]any{"tool": ToolRun, "args": map[string]any{"command": "test -f brief.txt"}},
			},
		})
		done <- err
	}()
	pending := waitApproval(t, b)
	if pending.Request.Tool != ToolApplyActions || len(pending.Request.Actions) != 2 || pending.Request.Summary != "Create a short brief and verify it" {
		t.Fatalf("approval plan = %+v", pending.Request)
	}
	// A single decision releases the validated plan. There must not be a
	// second prompt for the run action.
	pending.Decide(true)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Next(); ok {
		t.Fatal("plan prompted more than once")
	}
	if got, err := os.ReadFile(filepath.Join(root, "brief.txt")); err != nil || string(got) != "approved" {
		t.Fatalf("planned write = %q, %v", got, err)
	}
}

func TestInteractivePlanRejectsInvalidActionBeforePrompt(t *testing.T) {
	b, err := NewApprovalBroker()
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("the surrounding test sandbox forbids Unix-domain sockets")
		}
		t.Fatal(err)
	}
	defer b.Close()
	root := t.TempDir()
	svc := NewService(InteractiveWorkspacePolicy("ceo", "id", root, t.TempDir(), b.Socket()), nil)
	_, err = svc.Call(context.Background(), ToolApplyActions, map[string]any{
		"summary": "Attempt an escape",
		"actions": []any{map[string]any{"tool": ToolWriteFile, "args": map[string]any{"path": "../outside.txt", "content": "no"}}},
	})
	if err == nil || !errors.Is(err, ErrDenied) {
		t.Fatalf("invalid plan err = %v", err)
	}
	if _, ok := b.Next(); ok {
		t.Fatal("invalid plan reached approval UI")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "outside.txt")); !os.IsNotExist(err) {
		t.Fatalf("invalid plan touched disk: %v", err)
	}
}

func TestInteractivePolicyRejectsDirectConsequentialCalls(t *testing.T) {
	b, err := NewApprovalBroker()
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("the surrounding test sandbox forbids Unix-domain sockets")
		}
		t.Fatal(err)
	}
	defer b.Close()
	svc := NewService(InteractiveWorkspacePolicy("ceo", "id", t.TempDir(), t.TempDir(), b.Socket()), nil)
	if _, err := svc.Call(context.Background(), ToolWriteFile, map[string]any{"path": "bypass.txt", "content": "no"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("direct write err = %v", err)
	}
	if _, err := svc.Call(context.Background(), ToolRun, map[string]any{"command": "true"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("direct run err = %v", err)
	}
	if _, ok := b.Next(); ok {
		t.Fatal("direct action reached approval UI")
	}
}

func TestInteractivePlanDenialDoesNotPartiallyExecute(t *testing.T) {
	b, err := NewApprovalBroker()
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("the surrounding test sandbox forbids Unix-domain sockets")
		}
		t.Fatal(err)
	}
	defer b.Close()
	root := t.TempDir()
	svc := NewService(InteractiveWorkspacePolicy("ceo", "id", root, t.TempDir(), b.Socket()), nil)
	done := make(chan error, 1)
	go func() {
		_, err := svc.Call(context.Background(), ToolApplyActions, map[string]any{
			"summary": "Write two files",
			"actions": []any{
				map[string]any{"tool": ToolWriteFile, "args": map[string]any{"path": "one.txt", "content": "one"}},
				map[string]any{"tool": ToolWriteFile, "args": map[string]any{"path": "two.txt", "content": "two"}},
			},
		})
		done <- err
	}()
	waitApproval(t, b).Decide(false)
	if err := <-done; err == nil {
		t.Fatal("denied plan succeeded")
	}
	for _, n := range []string{"one.txt", "two.txt"} {
		if _, err := os.Stat(filepath.Join(root, n)); !os.IsNotExist(err) {
			t.Fatalf("denied plan touched %s: %v", n, err)
		}
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
