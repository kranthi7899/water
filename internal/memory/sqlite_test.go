package memory_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	"water/internal/memory"
	"water/internal/memory/storetest"
	"water/internal/store"
)

var (
	pathsMu sync.Mutex
	paths   = map[memory.Backend]string{}
)

func openSQLite(t *testing.T, path string) memory.Backend {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	b, err := memory.NewSQLite(st)
	if err != nil {
		t.Fatal(err)
	}
	pathsMu.Lock()
	paths[b] = path
	pathsMu.Unlock()
	return b
}

// TestSQLiteContract runs the backend contract suite against SQLite, the
// backend the owner chose at F's Approve.
func TestSQLiteContract(t *testing.T) {
	storetest.Run(t, storetest.Harness{
		New: func(t *testing.T) memory.Backend {
			return openSQLite(t, filepath.Join(t.TempDir(), "water.db"))
		},
		// Two store.Open calls on one file, as the daemon and a CLI
		// process would have.
		NewShared: func(t *testing.T) (memory.Backend, memory.Backend) {
			p := filepath.Join(t.TempDir(), "water.db")
			return openSQLite(t, p), openSQLite(t, p)
		},
		InjectSupersedeFault: memory.SetSupersedeFault,
		// A hand edit with the sqlite3 shell: it has to drop the
		// append-only trigger first, which a determined editor can.
		CorruptStatement: func(t *testing.T, b memory.Backend, id, statement string) {
			pathsMu.Lock()
			p := paths[b]
			pathsMu.Unlock()
			db, err := sql.Open("sqlite", "file:"+p+"?_pragma=busy_timeout(5000)")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			if _, err := db.ExecContext(ctx, `DROP TRIGGER memory_records_append_only`); err != nil {
				t.Fatal(err)
			}
			res, err := db.ExecContext(ctx, `UPDATE memory_records SET statement = ? WHERE id = ?`, statement, id)
			if err != nil {
				t.Fatal(err)
			}
			if n, _ := res.RowsAffected(); n != 1 {
				t.Fatalf("corrupted %d rows", n)
			}
		},
	})
}
