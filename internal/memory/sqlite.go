package memory

import (
	"context"
	"errors"

	"water/internal/store"
)

// SQLite is the memory Backend over the state store's memory_records
// table (migration 0014). The owner chose SQLite at F's Approve
// (2026-09-25): real indexes, a transactional Supersede, and the records
// sit next to what G and Q join against.
//
// Atomicity: Update runs fn inside store.MemoryTx, one BEGIN IMMEDIATE
// transaction that holds the database write lock from its first
// statement, so a read-check-write in fn (bounds, "is the old record still
// live?") cannot race another writer in this process or another one, and
// an error anywhere in fn rolls back every write fn made.
type SQLite struct {
	st *store.Store
}

// NewSQLite returns a Backend over st. The caller owns st and closes it.
func NewSQLite(st *store.Store) (*SQLite, error) {
	if st == nil {
		return nil, errors.New("memory: nil store")
	}
	return &SQLite{st: st}, nil
}

var _ Backend = (*SQLite)(nil)

func (s *SQLite) Update(ctx context.Context, twin string, fn func(Tx) error) error {
	return s.st.MemoryTx(ctx, func(tx *store.MemoryTx) error {
		return fn(sqliteTx{tx: tx, twin: twin})
	})
}

func (s *SQLite) Get(ctx context.Context, twin, id string) (Record, error) {
	row, err := s.st.GetMemoryRecord(ctx, twin, id)
	if err != nil {
		return Record{}, mapStoreErr(err)
	}
	return fromRow(row), nil
}

func (s *SQLite) List(ctx context.Context, twin string, f Filter) ([]Record, error) {
	types := make([]string, len(f.Types))
	for i, t := range f.Types {
		types[i] = string(t)
	}
	rows, err := s.st.ListMemoryRecords(ctx, store.MemoryFilter{Twin: twin, Types: types, LiveOnly: f.LiveOnly})
	if err != nil {
		return nil, err
	}
	return fromRows(rows), nil
}

type sqliteTx struct {
	tx   *store.MemoryTx
	twin string
}

func (t sqliteTx) Get(ctx context.Context, id string) (Record, error) {
	row, err := t.tx.Get(ctx, t.twin, id)
	if err != nil {
		return Record{}, mapStoreErr(err)
	}
	return fromRow(row), nil
}

func (t sqliteTx) Live(ctx context.Context) ([]Record, error) {
	rows, err := t.tx.Live(ctx, t.twin)
	if err != nil {
		return nil, err
	}
	return fromRows(rows), nil
}

func (t sqliteTx) Insert(ctx context.Context, r Record) error {
	return t.tx.Insert(ctx, toRow(t.twin, r))
}

func (t sqliteTx) Invalidate(ctx context.Context, id string, inv Invalidation) error {
	return mapStoreErr(t.tx.Invalidate(ctx, t.twin, id, store.MemoryInvalidationRow{
		At: inv.At, By: inv.By, Reason: inv.Reason, AuditSeq: inv.AuditSeq, SupersededBy: inv.SupersededBy,
	}))
}

func mapStoreErr(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, store.ErrMemoryAlreadyInvalidated):
		return ErrAlreadyInvalidated
	}
	return err
}

func toRow(twin string, r Record) store.MemoryRecordRow {
	return store.MemoryRecordRow{
		ID: r.ID, Twin: twin, Type: string(r.Type), Statement: r.Statement,
		Sensitivity: string(r.Sensitivity), Subjects: r.Subjects,
		Trigger: string(r.Provenance.Trigger), SourceRef: r.Provenance.SourceRef,
		AuditSeq: r.Provenance.AuditSeq, WrittenBy: r.Provenance.WrittenBy, ApprovedBy: r.Provenance.ApprovedBy,
		ObservedAt: r.Time.ObservedAt, ValidFrom: r.Time.ValidFrom,
		ValidUntil: r.Time.ValidUntil, ReviewAfter: r.Time.ReviewAfter,
		Confidence: r.Confidence, Supersedes: r.Supersedes,
	}
}

func fromRow(row store.MemoryRecordRow) Record {
	r := Record{
		ID: row.ID, Type: Type(row.Type), Statement: row.Statement,
		Sensitivity: Sensitivity(row.Sensitivity), Subjects: row.Subjects,
		Provenance: Provenance{
			Trigger: Trigger(row.Trigger), SourceRef: row.SourceRef, AuditSeq: row.AuditSeq,
			WrittenBy: row.WrittenBy, ApprovedBy: row.ApprovedBy,
		},
		Time: TimeBounds{
			ObservedAt: row.ObservedAt, ValidFrom: row.ValidFrom,
			ValidUntil: row.ValidUntil, ReviewAfter: row.ReviewAfter,
		},
		Confidence: row.Confidence, Supersedes: row.Supersedes,
	}
	if inv := row.Invalidation; inv != nil {
		r.Invalidation = &Invalidation{
			At: inv.At, By: inv.By, Reason: inv.Reason, AuditSeq: inv.AuditSeq, SupersededBy: inv.SupersededBy,
		}
	}
	return r
}

func fromRows(rows []store.MemoryRecordRow) []Record {
	out := make([]Record, len(rows))
	for i, row := range rows {
		out[i] = fromRow(row)
	}
	return out
}
