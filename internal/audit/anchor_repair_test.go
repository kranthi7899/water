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

// anchoredLog writes n anchored entries and returns the path, the anchor and
// the file's bytes.
func anchoredLog(t *testing.T, n int) (string, *memAnchor, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	anchor := &memAnchor{}
	l, err := Open(path, WithAnchor(anchor))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := l.Append(Record{Kind: KindCall, Function: "f", Allowed: true, Reason: "original"}); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, anchor, b
}

func assertUnchanged(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("repair changed the log:\n got %q\nwant %q", got, want)
	}
	if m, _ := filepath.Glob(path + ".repair-dropped-*"); len(m) != 0 {
		t.Fatalf("repair dropped a line it refused: %v", m)
	}
}

// An edited (complete, valid JSON) final entry is tamper evidence, not a
// torn write: Repair must refuse and leave the file byte-for-byte as it was.
func TestRepairRefusesAnEditedFinalEntry(t *testing.T) {
	path, anchor, _ := anchoredLog(t, 3)
	b, _ := os.ReadFile(path)
	if strings.Count(string(b), `"reason":"original"`) != 3 {
		t.Fatalf("unexpected log shape: %s", b)
	}
	// Edit only the last line.
	i := strings.LastIndex(string(b), `"reason":"original"`)
	edited := []byte(string(b[:i]) + `"reason":"edited"` + string(b[i+len(`"reason":"original"`):]))
	if err := os.WriteFile(path, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Repair(path, WithAnchor(anchor)); err == nil {
		t.Fatal("repair accepted an edited final entry")
	}
	assertUnchanged(t, path, edited)
	if err := Repair(path); err == nil {
		t.Fatal("repair without an anchor accepted an edited final entry")
	}
	assertUnchanged(t, path, edited)
}

// A garbled final line the anchor already covers was a complete entry once:
// dropping it would leave the log behind its anchor and the evidence gone.
func TestRepairRefusesToDropAnAnchoredLine(t *testing.T) {
	path, anchor, _ := anchoredLog(t, 3)
	lines, err := readLines(path)
	if err != nil {
		t.Fatal(err)
	}
	lines[2] = lines[2][:len(lines[2])/2] // looks torn, but seq 3 is anchored
	if err := truncateToLines(path, lines); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := Repair(path, WithAnchor(anchor)); err == nil {
		t.Fatal("repair dropped an anchored line")
	}
	assertUnchanged(t, path, before)
}

// A real torn write under an anchor still repairs, and keeps the dropped
// bytes next to the log.
func TestRepairWithAnAnchorDropsATornLineAndKeepsIt(t *testing.T) {
	path, anchor, _ := anchoredLog(t, 2)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	torn := `{"seq":3,"kind":"call","prev_hash":"x"`
	f.WriteString(torn)
	f.Close()
	if err := Repair(path, WithAnchor(anchor)); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if n, err := Verify(path); err != nil || n != 3 || anchor.seq != 3 {
		t.Fatalf("post-repair verify=%d %v anchor=%+v", n, err, anchor)
	}
	m, _ := filepath.Glob(path + ".repair-dropped-*")
	if len(m) != 1 {
		t.Fatalf("dropped-line files = %v, want 1", m)
	}
	b, _ := os.ReadFile(m[0])
	if strings.TrimSpace(string(b)) != torn {
		t.Fatalf("kept %q, want %q", b, torn)
	}
	if st, _ := os.Stat(m[0]); st.Mode().Perm() != 0o600 {
		t.Fatalf("dropped-line file mode %v", st.Mode().Perm())
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
