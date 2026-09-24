package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/store"
	"water/internal/twins"
)

// testEnv builds a minimal, real (temp-dir-backed) Env for this package's
// tests: a real store and approval queue, a fixed clock, and a fake
// backend. Used across runtime_test.go, brief_test.go and turnfixes_test.go
// — moved here (it used to live in the now-deleted fastpath_test.go) so it
// survives task R-12's deletion of internal/runtime/fastpath.go.
func testEnv(t *testing.T) (Env, context.Context) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	q := approvals.NewQueue(st, log)
	now := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
	q.Now = func() time.Time { return now }
	m := &twins.Manifest{ID: "t"}
	return Env{
		Store: st, Approvals: q, Now: func() time.Time { return now },
		Manifest: m, Backend: backend.NewFake("fake"),
	}, context.Background()
}

// contains is a plain substring check, kept here (moved from the deleted
// fastpath_test.go) since a couple of this package's tests still use it.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
