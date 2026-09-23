package audit

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func openTemp(t *testing.T) *Log {
	t.Helper()
	l, err := Open(filepath.Join(t.TempDir(), "audit", "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func TestChainVerifiesAndSurvivesReopen(t *testing.T) {
	l := openTemp(t)
	for _, k := range []Kind{KindCall, KindDecision, KindExecute} {
		if _, err := l.Append(Record{Kind: k, Function: "mail.send_email", Allowed: true, ArgsHash: "abc"}); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", info.Mode().Perm())
	}
	if n, err := Verify(l.Path()); err != nil || n != 3 {
		t.Fatalf("verify: %d %v", n, err)
	}
	l.Close()
	l2, err := Open(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	e, err := l2.Append(Record{Kind: KindDenial, Reason: "x"})
	if err != nil || e.Seq != 4 {
		t.Fatalf("append after reopen: %+v %v", e, err)
	}
	if n, err := Verify(l.Path()); err != nil || n != 4 {
		t.Fatalf("verify after reopen: %d %v", n, err)
	}
}

func TestTamperBreaksChain(t *testing.T) {
	l := openTemp(t)
	for i := 0; i < 3; i++ {
		l.Append(Record{Kind: KindDecision, Function: "f", Allowed: false, Reason: "no"})
	}
	l.Close()
	b, _ := os.ReadFile(l.Path())
	tampered := bytes.Replace(b, []byte(`"allowed":false`), []byte(`"allowed":true`), 1)
	os.WriteFile(l.Path(), tampered, 0o600)
	_, err := Verify(l.Path())
	var be *BreakError
	if !errors.As(err, &be) || be.Line != 1 {
		t.Fatalf("want break at line 1, got %v", err)
	}
	if _, err := Open(l.Path()); err == nil {
		t.Fatal("opened a broken chain for writing")
	}

	lines := bytes.SplitAfter(b, []byte("\n"))
	dropped := append(append([]byte{}, lines[0]...), lines[2]...)
	os.WriteFile(l.Path(), dropped, 0o600)
	if _, err := Verify(l.Path()); !errors.As(err, &be) || be.Line != 2 {
		t.Fatalf("deleted entry not detected at line 2: %v", err)
	}
}

func TestSingleWriter(t *testing.T) {
	l := openTemp(t)
	if _, err := Open(l.Path()); !errors.Is(err, ErrLocked) {
		t.Fatalf("second writer: %v", err)
	}
}

func TestUnwritableFailsAppend(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits do not bind root")
	}
	l := openTemp(t)
	os.Chmod(l.Path(), 0o400)
	if _, err := l.Append(Record{Kind: KindCall}); err == nil {
		t.Fatal("append to read-only log succeeded")
	}
	os.Chmod(l.Path(), 0o600)
	l.Close()
	if _, err := l.Append(Record{Kind: KindCall}); !errors.Is(err, ErrClosed) {
		t.Fatalf("append after close: %v", err)
	}
}
