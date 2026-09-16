package tools

// The interactive approval bridge connects Water's chat process to its MCP
// child. The child must never prompt on /dev/tty: that would corrupt the TUI
// and let an untrusted tool result impersonate a prompt. Instead it sends one
// local, correlated request over a 0600 Unix socket and waits for the parent
// UI to return an explicit decision.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// ApprovalRequest is the exact action the user sees before it can run.
type ApprovalRequest struct {
	CallID    string          `json:"call_id"`
	Role      string          `json:"role"`
	Tool      string          `json:"tool"`
	Args      map[string]any  `json:"args"`
	Summary   string          `json:"summary,omitempty"`
	Workspace string          `json:"workspace,omitempty"`
	Actions   []PlannedAction `json:"actions,omitempty"`
}

// PlannedAction is one exact, already-authorised effect in an approval plan.
// The broker never receives model-supplied actions directly: Service resolves
// them first, then sends this immutable description to the TUI.
type PlannedAction struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}

type approvalReply struct {
	Allowed bool `json:"allowed"`
}

// PendingApproval is delivered to the TUI. Decide is idempotent so cleanup
// and a late keypress cannot turn one tool call into two decisions.
type PendingApproval struct {
	Request ApprovalRequest
	once    sync.Once
	done    chan bool
	expired atomic.Bool
}

// NewPendingApproval constructs one request holder. It is public so a UI can
// render and test approval state without being coupled to the Unix transport.
func NewPendingApproval(req ApprovalRequest) *PendingApproval {
	return &PendingApproval{Request: req, done: make(chan bool, 1)}
}

func (p *PendingApproval) Decide(allowed bool) { p.once.Do(func() { p.done <- allowed }) }

// Expired reports whether the requester disconnected or timed out. Such a
// prompt is not a reusable permission and must be removed from the UI.
func (p *PendingApproval) Expired() bool { return p.expired.Load() }
func (p *PendingApproval) expire() {
	p.expired.Store(true)
	p.Decide(false)
}

// ApprovalBroker owns one private socket and queues requests for an
// interactive UI. It is intentionally session-local and never persisted.
type ApprovalBroker struct {
	socket string
	dir    string
	ln     net.Listener
	reqs   chan *PendingApproval
	done   chan struct{}
	once   sync.Once
}

// NewApprovalBroker starts a local Unix-socket broker. The socket directory
// is 0700 and the socket is 0600; only this macOS/Linux user can connect.
func NewApprovalBroker() (*ApprovalBroker, error) {
	dir, err := os.MkdirTemp("", "water-approval-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	socket := filepath.Join(dir, "approval.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	_ = os.Chmod(socket, 0o600)
	b := &ApprovalBroker{socket: socket, dir: dir, ln: ln, reqs: make(chan *PendingApproval, 8), done: make(chan struct{})}
	go b.serve()
	return b, nil
}

func (b *ApprovalBroker) Socket() string { return b.socket }

// Next returns the next tool action needing a user decision without blocking
// the Bubble Tea update loop.
func (b *ApprovalBroker) Next() (*PendingApproval, bool) {
	for {
		select {
		case p := <-b.reqs:
			if !p.Expired() {
				return p, true
			}
		default:
			return nil, false
		}
	}
}

func (b *ApprovalBroker) Close() error {
	var err error
	b.once.Do(func() {
		close(b.done)
		err = b.ln.Close()
		_ = os.RemoveAll(b.dir)
	})
	return err
}

func (b *ApprovalBroker) serve() {
	for {
		conn, err := b.ln.Accept()
		if err != nil {
			select {
			case <-b.done:
				return
			default:
				continue
			}
		}
		go b.handle(conn)
	}
}

func (b *ApprovalBroker) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))
	// Closing the broker must also release connections still decoding input.
	closed := make(chan struct{})
	defer close(closed)
	go func() {
		select {
		case <-b.done:
			_ = conn.Close()
		case <-closed:
		}
	}()
	var req ApprovalRequest
	if err := json.NewDecoder(io.LimitReader(conn, 2<<20)).Decode(&req); err != nil {
		return
	}
	p := NewPendingApproval(req)
	defer p.expire()
	// There is exactly one request per connection. EOF, timeout or further
	// client data invalidates it; waiting for a key must not outlive the caller.
	disconnected := make(chan struct{})
	go func() {
		var extra [1]byte
		_, _ = conn.Read(extra[:])
		p.expire()
		close(disconnected)
	}()
	select {
	case b.reqs <- p:
	case <-disconnected:
		return
	case <-b.done:
		return
	}
	select {
	case allowed := <-p.done:
		_ = json.NewEncoder(conn).Encode(approvalReply{Allowed: allowed && !p.Expired()})
	case <-disconnected:
	case <-b.done:
	}
}

// RequestApproval asks the parent UI for one decision. If no approval bridge
// exists, the safe answer is an error — callers must never fall open.
func RequestApproval(ctx context.Context, socket string, req ApprovalRequest) (bool, error) {
	if socket == "" {
		return false, errors.New("interactive approval is unavailable")
	}
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return false, fmt.Errorf("connect approval prompt: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline := time.Now().Add(5 * time.Minute)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return false, fmt.Errorf("send approval request: %w", err)
	}
	var reply approvalReply
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		if ctx.Err() != nil {
			return false, fmt.Errorf("wait for approval: %w", ctx.Err())
		}
		return false, fmt.Errorf("wait for approval: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return reply.Allowed, nil
}
