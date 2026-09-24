package audit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// memAnchor is a minimal in-memory Anchor for tests.
type memAnchor struct {
	seq  int64
	hash string
	ok   bool
}

func (a *memAnchor) LoadAuditAnchor(context.Context) (int64, string, bool, error) {
	return a.seq, a.hash, a.ok, nil
}
func (a *memAnchor) SaveAuditAnchor(_ context.Context, seq int64, hash string) error {
	a.seq, a.hash, a.ok = seq, hash, true
	return nil
}

func TestAnchorDetectsTruncatedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	anchor := &memAnchor{}
	l, err := Open(path, WithAnchor(anchor))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := l.Append(Record{Kind: KindCall, Function: "f", Allowed: true}); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	if !anchor.ok || anchor.seq != 3 {
		t.Fatalf("anchor after 3 appends: %+v", anchor)
	}

	// Truncate the file's last line out from under the anchor: a legitimate
	// chain (it still verifies on its own) but no longer what was anchored.
	lines, err := readLines(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := truncateToLines(path, lines[:len(lines)-1]); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(path, WithAnchor(anchor)); err == nil {
		t.Fatal("expected a truncated tail to be detected against the anchor")
	}
}

func TestAnchorSurvivesReopenAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	anchor := &memAnchor{}
	l, err := Open(path, WithAnchor(anchor))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(Record{Kind: KindCall, Function: "f", Allowed: true}); err != nil {
		t.Fatal(err)
	}
	l.Close()

	// A fresh Log ("daemon restart") with the same (persisted) anchor state
	// must open cleanly and keep extending the same chain.
	l2, err := Open(path, WithAnchor(anchor))
	if err != nil {
		t.Fatalf("reopen with matching anchor: %v", err)
	}
	if _, err := l2.Append(Record{Kind: KindCall, Function: "g", Allowed: true}); err != nil {
		t.Fatal(err)
	}
	l2.Close()
	if n, err := Verify(path); err != nil || n != 2 {
		t.Fatalf("verify: %d %v", n, err)
	}
}

func TestRepairDropsOnlyATornFinalLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := l.Append(Record{Kind: KindCall, Function: "f", Allowed: true}); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()

	// Simulate a crash mid-write: append a torn (incomplete JSON) final line.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"seq":3,"kind":"call","prev_hash":"x"`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if _, err := Verify(path); err == nil {
		t.Fatal("expected the torn file to fail verification before repair")
	}
	if err := Repair(path); err != nil {
		t.Fatalf("repair: %v", err)
	}
	n, err := Verify(path)
	if err != nil || n != 3 {
		t.Fatalf("post-repair verify: %d %v", n, err)
	}
	lines, err := readLines(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 {
		t.Fatalf("expected 2 original + 1 repair entry, got %d lines", len(lines))
	}
}

func TestRepairRefusesAnEarlierBreak(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := l.Append(Record{Kind: KindCall, Function: "f", Allowed: true}); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()

	lines, err := readLines(path)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the MIDDLE line, not the last one.
	lines[1] = `{"seq":2,"kind":"call","prev_hash":"not-right","hash":"also-wrong"}`
	if err := truncateToLines(path, lines); err != nil {
		t.Fatal(err)
	}
	if err := Repair(path); err == nil {
		t.Fatal("expected repair to refuse a break earlier than the final line")
	}
}

// TestAnchorRollsForwardAfterACrashBeforeTheAnchorUpdate: Append makes the
// line durable before it updates the anchor, so a crash between the two
// leaves the file exactly one validly-chained entry ahead. That is not a
// tamper signal; Open re-anchors (and records that it did) instead of
// refusing forever. Anything else still refuses.
func TestAnchorRollsForwardAfterACrashBeforeTheAnchorUpdate(t *testing.T) {
	setup := func(t *testing.T) (string, *memAnchor, []Entry) {
		path := filepath.Join(t.TempDir(), "audit.jsonl")
		anchor := &memAnchor{}
		l, err := Open(path, WithAnchor(anchor))
		if err != nil {
			t.Fatal(err)
		}
		var es []Entry
		for i := 0; i < 3; i++ {
			e, err := l.Append(Record{Kind: KindCall, Function: "f", Allowed: true})
			if err != nil {
				t.Fatal(err)
			}
			es = append(es, e)
		}
		l.Close()
		return path, anchor, es
	}

	path, anchor, es := setup(t)
	anchor.seq, anchor.hash = es[1].Seq, es[1].Hash // crash before anchoring seq 3
	l, err := Open(path, WithAnchor(anchor))
	if err != nil {
		t.Fatalf("open one entry past the anchor: %v", err)
	}
	if _, err := l.Append(Record{Kind: KindCall, Function: "g", Allowed: true}); err != nil {
		t.Fatal(err)
	}
	l.Close()
	if n, err := Verify(path); err != nil || anchor.seq != n {
		t.Fatalf("after roll-forward: verify=%d %v anchor=%+v", n, err, anchor)
	}
	lines, _ := readLines(path)
	if !strings.Contains(strings.Join(lines, "\n"), `"kind":"repair"`) {
		t.Fatal("the roll-forward was not recorded in the log")
	}

	path, anchor, es = setup(t)
	anchor.seq, anchor.hash = es[0].Seq, es[0].Hash // two behind
	if _, err := Open(path, WithAnchor(anchor)); err == nil {
		t.Fatal("an anchor two entries behind was accepted")
	}

	path, anchor, es = setup(t)
	anchor.seq, anchor.hash = es[1].Seq, "not-the-hash" // one behind, wrong hash
	if _, err := Open(path, WithAnchor(anchor)); err == nil {
		t.Fatal("an anchor one behind with the wrong hash was accepted")
	}
}
