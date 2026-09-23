// Package audit is the append-only, hash-chained record of every gate
// decision, approval and execution. Unlike the per-run trace it is 0600, has
// exactly one writer, and a failed write fails the action that caused it.
package audit

import (
	"bufio"
	"bytes"
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

// Log is the single writer for one audit file.
type Log struct {
	path   string
	mu     sync.Mutex
	unlock func()
	seq    int64
	last   string
	now    func() time.Time
}

// Open takes the writer lock and verifies the existing chain. A log that does
// not verify is refused rather than extended: appending to a broken chain
// would launder the break.
func Open(path string) (*Log, error) {
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
	return &Log{path: path, unlock: unlock, seq: last.Seq, last: last.Hash, now: time.Now}, nil
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
	l.seq, l.last = e.Seq, e.Hash
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
