package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// TestFTS5CompiledIn is the Slice M spike, kept as a regression test: the
// pure-Go modernc.org/sqlite driver has FTS5 built in, with no extension to
// load, and an external-content table plus trigger and MATCH all work.
func TestFTS5CompiledIn(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "s.db")+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE docs (id INTEGER PRIMARY KEY, title TEXT, body TEXT)`,
		`CREATE VIRTUAL TABLE docs_fts USING fts5(title, body, content='docs', content_rowid='id')`,
		`CREATE TRIGGER docs_ai AFTER INSERT ON docs BEGIN INSERT INTO docs_fts(rowid, title, body) VALUES (new.id, new.title, new.body); END`,
		`INSERT INTO docs (title, body) VALUES ('Kafka budget', 'compute spend for Q3'), ('Hiring', 'two engineers')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var title string
	if err := db.QueryRow(`SELECT d.title FROM docs_fts JOIN docs d ON d.id = docs_fts.rowid WHERE docs_fts MATCH ? ORDER BY bm25(docs_fts)`, "kafka").Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Kafka budget" {
		t.Fatalf("title = %q", title)
	}
}

func ftsMatch(t *testing.T, s *Store, table, q string) []string {
	t.Helper()
	col := map[string]string{"messages": "subject", "documents": "title"}[table]
	rows, err := s.db.Query(`SELECT t.source_id, t.`+col+` FROM `+table+`_fts f JOIN `+table+` t ON t.id = f.rowid
		WHERE `+table+`_fts MATCH ? ORDER BY bm25(`+table+`_fts)`, q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id, x string
		if err := rows.Scan(&id, &x); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// TestFTSIndexesTrackUpsert exercises the 0007 migration's indexes through
// the production Open and Upsert paths: an insert is searchable, an update
// (Upsert's ON CONFLICT branch) replaces the old terms, and porter stemming
// matches inflected forms.
func TestFTSIndexesTrackUpsert(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	must := func(r Record) {
		t.Helper()
		if err := s.Upsert(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	must(&Message{Meta: Meta{Source: "gmail", SourceID: "m1", External: true}, From: "priya@acme.com", Subject: "Compute", Body: "The Kafka budgets are over by 20%"})
	must(&Message{Meta: Meta{Source: "gmail", SourceID: "m2", External: true}, From: "dana@acme.com", Subject: "Lunch", Body: "tacos?"})
	must(&Document{Meta: Meta{Source: "gdrive", SourceID: "d1", External: true}, Title: "Kafka budget FY27", Excerpt: "broker costs"})

	if got := ftsMatch(t, s, "messages", "budget"); len(got) != 1 || got[0] != "m1" {
		t.Fatalf("stemmed match = %v, want [m1]", got)
	}
	if got := ftsMatch(t, s, "messages", `sender:priya`); len(got) != 1 || got[0] != "m1" {
		t.Fatalf("column match = %v, want [m1]", got)
	}
	if got := ftsMatch(t, s, "documents", "kafka"); len(got) != 1 || got[0] != "d1" {
		t.Fatalf("documents match = %v, want [d1]", got)
	}
	// Free text with FTS5 syntax characters in it is a query error unless
	// each term is double-quoted.
	if _, err := s.db.Query(`SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?`, `priya's 20%`); err == nil {
		t.Fatal("expected unquoted free text with FTS5 syntax characters to fail")
	}
	if got := ftsMatch(t, s, "messages", `"priya's" OR "20%"`); len(got) != 1 {
		t.Fatalf("quoted free-text match = %v, want one hit", got)
	}

	must(&Message{Meta: Meta{Source: "gmail", SourceID: "m1", External: true}, From: "priya@acme.com", Subject: "Compute", Body: "GPU reservations only"})
	if got := ftsMatch(t, s, "messages", "kafka"); len(got) != 0 {
		t.Fatalf("stale terms still indexed after update: %v", got)
	}
	if got := ftsMatch(t, s, "messages", "gpu"); len(got) != 1 || got[0] != "m1" {
		t.Fatalf("updated terms not indexed: %v", got)
	}
	if _, err := s.db.Exec(`INSERT INTO messages_fts (messages_fts) VALUES ('integrity-check')`); err != nil {
		t.Fatalf("messages_fts integrity-check: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO documents_fts (documents_fts) VALUES ('integrity-check')`); err != nil {
		t.Fatalf("documents_fts integrity-check: %v", err)
	}
}
