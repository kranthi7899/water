package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func memRow(id string) MemoryRecordRow {
	t0 := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	until := t0.Add(24 * time.Hour)
	return MemoryRecordRow{ID: id, Twin: "ceo", Type: "preference", Statement: "s", Sensitivity: "normal",
		Subjects: []string{"person:dana"}, Trigger: "ceo_statement", SourceRef: "audit:1", AuditSeq: 1,
		WrittenBy: "ceo", ObservedAt: t0, ValidFrom: t0, ValidUntil: &until, Confidence: 0.5}
}

// TestMemoryRecordsAppendOnly covers the table's guarantees independent of
// internal/memory: rows round-trip, a row is invalidated at most once, the
// database itself refuses content updates and deletes (migration 0014's
// triggers), and a MemoryTx whose fn fails leaves nothing behind.
func TestMemoryRecordsAppendOnly(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	r := memRow("mem_a")
	if err := s.MemoryTx(ctx, func(tx *MemoryTx) error { return tx.Insert(ctx, r) }); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMemoryRecord(ctx, "ceo", "mem_a")
	if err != nil || !reflect.DeepEqual(got, r) {
		t.Fatalf("round trip:\n got  %+v\n want %+v (%v)", got, r, err)
	}
	if _, err := s.GetMemoryRecord(ctx, "ceo-demo", "mem_a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other twin's get: %v", err)
	}

	for _, stmt := range []string{
		`UPDATE memory_records SET statement = 'edited' WHERE id = 'mem_a'`,
		`DELETE FROM memory_records WHERE id = 'mem_a'`,
		// REPLACE deletes the conflicting row without firing DELETE
		// triggers (recursive_triggers is off), so it needs its own guard.
		`INSERT OR REPLACE INTO memory_records (id, twin, type, statement, sensitivity, prov_trigger,
			source_ref, audit_seq, written_by, observed_at, valid_from, confidence)
			VALUES ('mem_a', 'ceo', 'preference', 'overwritten', 'normal', 'ceo_statement', 'audit:1', 1, 'ceo', 1, 1, 1)`,
		`REPLACE INTO memory_records (id, twin, type, statement, sensitivity, prov_trigger,
			source_ref, audit_seq, written_by, observed_at, valid_from, confidence)
			VALUES ('mem_a', 'ceo', 'preference', 'overwritten', 'normal', 'ceo_statement', 'audit:1', 1, 'ceo', 1, 1, 1)`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if got, err := s.GetMemoryRecord(ctx, "ceo", "mem_a"); err != nil || !reflect.DeepEqual(got, r) {
		t.Fatalf("row changed after refused writes:\n got  %+v\n want %+v (%v)", got, r, err)
	}

	inv := MemoryInvalidationRow{At: r.ObservedAt.Add(time.Hour), By: "ceo", Reason: "r", AuditSeq: 2}
	if err := s.MemoryTx(ctx, func(tx *MemoryTx) error { return tx.Invalidate(ctx, "ceo", "mem_a", inv) }); err != nil {
		t.Fatal(err)
	}
	if err := s.MemoryTx(ctx, func(tx *MemoryTx) error { return tx.Invalidate(ctx, "ceo", "mem_a", inv) }); !errors.Is(err, ErrMemoryAlreadyInvalidated) {
		t.Fatalf("second invalidate: %v", err)
	}
	if err := s.MemoryTx(ctx, func(tx *MemoryTx) error { return tx.Invalidate(ctx, "ceo", "mem_nope", inv) }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invalidate unknown: %v", err)
	}
	// Clearing an invalidation directly is refused by the database.
	if _, err := s.db.ExecContext(ctx, `UPDATE memory_records SET invalidated_at = NULL WHERE id = 'mem_a'`); err == nil {
		t.Fatal("clearing an invalidation was allowed")
	}
	got, _ = s.GetMemoryRecord(ctx, "ceo", "mem_a")
	if got.Invalidation == nil || *got.Invalidation != inv {
		t.Fatalf("invalidation = %+v", got.Invalidation)
	}

	// Rollback: fn inserts then fails.
	boom := errors.New("boom")
	err = s.MemoryTx(ctx, func(tx *MemoryTx) error {
		if err := tx.Insert(ctx, memRow("mem_b")); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("tx error: %v", err)
	}
	if _, err := s.GetMemoryRecord(ctx, "ceo", "mem_b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rolled-back insert is visible: %v", err)
	}
	rows, err := s.ListMemoryRecords(ctx, MemoryFilter{Twin: "ceo", Types: []string{"preference"}})
	if err != nil || len(rows) != 1 {
		t.Fatalf("list: %d %v", len(rows), err)
	}
	if rows, _ := s.ListMemoryRecords(ctx, MemoryFilter{Twin: "ceo", LiveOnly: true}); len(rows) != 0 {
		t.Fatalf("live-only list returned the invalidated row")
	}
}
