// Package audit is the append-only, hash-chained record of every gate
// decision, approval and execution. Unlike the per-run trace it is 0600, has
// exactly one writer, and a failed write fails the action that caused it.
package audit

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"water/internal/canon"
	"water/internal/config"
)

type Kind string

const (
	KindCall     Kind = "call"
	KindDecision Kind = "decision"
	KindApproval Kind = "approval"
	KindDenial   Kind = "denial"
	KindEdit     Kind = "edit"
	KindExecute  Kind = "execute"
	// KindPropose records a new envelope entering the approval queue.
	KindPropose Kind = "propose"
)

// Record is what a caller logs. Args are never logged, only ArgsHash.
type Record struct {
	Kind       Kind
	Function   string
	EnvelopeID string
	Origin     string
	Allowed    bool
	Reason     string
	ArgsHash   string
}

// Entry is one line of the log.
type Entry struct {
	Seq        int64  `json:"seq"`
	Time       string `json:"time"`
	Kind       Kind   `json:"kind"`
	Function   string `json:"function,omitempty"`
	EnvelopeID string `json:"envelope_id,omitempty"`
	Origin     string `json:"origin,omitempty"`
	Allowed    bool   `json:"allowed"`
	Reason     string `json:"reason,omitempty"`
	ArgsHash   string `json:"args_hash,omitempty"`
	PrevHash   string `json:"prev_hash"`
	Hash       string `json:"hash"`
}

var (
	ErrLocked = errors.New("audit log is held by another writer")
	ErrClosed = errors.New("audit log is closed")
)

// BreakError names the first entry at which the chain fails to verify.
type BreakError struct {
	Line   int
	Reason string
}

func (e *BreakError) Error() string {
	return fmt.Sprintf("audit chain broken at line %d: %s", e.Line, e.Reason)
}

// DefaultPath is ~/.water/audit/audit.jsonl.
func DefaultPath() string { return filepath.Join(config.Home(), "audit", "audit.jsonl") }

// Anchor persists the audit log's last (seq, hash) somewhere other than the
// log file itself, so a truncated tail can be detected on Open even though
// the file has no external witness of its own. The store implements this.
type Anchor interface {
	LoadAuditAnchor(ctx context.Context) (seq int64, hash string, ok bool, err error)
	SaveAuditAnchor(ctx context.Context, seq int64, hash string) error
}

// Log is the single writer for one audit file.
type Log struct {
	path   string
	mu     sync.Mutex
	unlock func()
	seq    int64
	last   string
	now    func() time.Time
	anchor Anchor
}

// Option configures Open.
type Option func(*Log)

// WithAnchor anchors the chain's tail in a is persisted store: Open compares
// the file's actual tail against the anchor and refuses a mismatch (the file
// was truncated or replaced out from under the anchor), and every Append
// updates the anchor before returning.
func WithAnchor(a Anchor) Option { return func(l *Log) { l.anchor = a } }

// Open takes the writer lock and verifies the existing chain. A log that does
// not verify is refused rather than extended: appending to a broken chain
// would launder the break. With WithAnchor, a chain that verifies internally
// but whose tail does not match the anchor (a truncated or replaced file) is
// refused too.
func Open(path string, opts ...Option) (*Log, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	unlock, err := lockFile(path + ".lock")
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY|noFollow, 0o600)
	if err != nil {
		unlock()
		return nil, err
	}
	err = f.Chmod(0o600)
	f.Close()
	if err != nil {
		unlock()
		return nil, err
	}
	last, err := verify(path)
	if err != nil {
		unlock()
		return nil, err
	}
	l := &Log{path: path, unlock: unlock, seq: last.Seq, last: last.Hash, now: time.Now}
	for _, o := range opts {
		o(l)
	}
	if l.anchor != nil {
		aseq, ahash, ok, err := l.anchor.LoadAuditAnchor(context.Background())
		if err != nil {
			unlock()
			return nil, fmt.Errorf("audit: loading anchor: %w", err)
		}
		if ok && (aseq != last.Seq || ahash != last.Hash) {
			unlock()
			return nil, &BreakError{int(last.Seq) + 1, fmt.Sprintf("tail does not match the anchored seq=%d hash=%s (truncated or replaced log?)", aseq, ahash)}
		}
		if !ok {
			if err := l.anchor.SaveAuditAnchor(context.Background(), last.Seq, last.Hash); err != nil {
				unlock()
				return nil, fmt.Errorf("audit: saving anchor: %w", err)
			}
		}
	}
	return l, nil
}

// Path returns the file this log writes.
func (l *Log) Path() string { return l.path }

// Close releases the writer lock. Later appends fail.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.unlock != nil {
		l.unlock()
		l.unlock = nil
	}
	return nil
}

// Append writes one entry and syncs it. The chain state advances only after
// the write is durable, so a failed append leaves nothing to build on.
func (l *Log) Append(r Record) (Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.unlock == nil {
		return Entry{}, ErrClosed
	}
	e := Entry{
		Seq:        l.seq + 1,
		Time:       l.now().UTC().Format(time.RFC3339Nano),
		Kind:       r.Kind,
		Function:   r.Function,
		EnvelopeID: r.EnvelopeID,
		Origin:     r.Origin,
		Allowed:    r.Allowed,
		Reason:     r.Reason,
		ArgsHash:   r.ArgsHash,
		PrevHash:   l.last,
	}
	h, err := entryHash(e)
	if err != nil {
		return Entry{}, err
	}
	e.Hash = h
	line, err := json.Marshal(e)
	if err != nil {
		return Entry{}, err
	}
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND|noFollow, 0o600)
	if err != nil {
		return Entry{}, fmt.Errorf("audit write: %w", err)
	}
	_, err = f.Write(append(line, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Entry{}, fmt.Errorf("audit write: %w", err)
	}
	// The line is now durable on disk; advance the in-memory chain state
	// before anything else can go wrong, so a later anchor failure never
	// leaves this Log computing its next entry against a stale prev_hash.
	l.seq, l.last = e.Seq, e.Hash
	if l.anchor != nil {
		if aerr := l.anchor.SaveAuditAnchor(context.Background(), e.Seq, e.Hash); aerr != nil {
			// The anchor is what detects tampering with the file later, so a
			// failure to update it is reported to the caller (the action
			// this record was for should not be treated as clean) even
			// though the entry itself is already durable and cannot be
			// unwritten.
			return e, fmt.Errorf("audit: anchor write failed after a durable append: %w", aerr)
		}
	}
	return e, nil
}

// HashArgs is the digest logged in place of raw arguments.
func HashArgs(args any) (string, error) { return canon.Hash(args) }

// Verify walks the chain and returns the number of entries, or a
// *BreakError for the first entry that does not verify.
func Verify(path string) (int64, error) {
	last, err := verify(path)
	return last.Seq, err
}

func verify(path string) (Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return Entry{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	var prev Entry
	line := 0
	for sc.Scan() {
		line++
		var e Entry
		dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&e); err != nil {
			return prev, &BreakError{line, "malformed entry: " + err.Error()}
		}
		if e.Seq != prev.Seq+1 {
			return prev, &BreakError{line, fmt.Sprintf("seq %d follows %d", e.Seq, prev.Seq)}
		}
		if e.PrevHash != prev.Hash {
			return prev, &BreakError{line, "prev_hash does not match the previous entry"}
		}
		want, err := entryHash(e)
		if err != nil {
			return prev, &BreakError{line, err.Error()}
		}
		if e.Hash != want {
			return prev, &BreakError{line, "hash does not match entry contents"}
		}
		prev = e
	}
	if err := sc.Err(); err != nil {
		return prev, &BreakError{line + 1, err.Error()}
	}
	return prev, nil
}

func entryHash(e Entry) (string, error) {
	e.Hash = ""
	b, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
