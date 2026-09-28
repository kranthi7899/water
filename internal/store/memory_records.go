package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// This file is the SQLite side of long-term memory (Slice F). It only moves
// rows: validation, the never-store check, bounds and the "current"
// definition all live in internal/memory, which is the only caller. The
// table is append-only (see migration 0014's triggers): rows are inserted,
// and a live row may have its invalidation columns set once. Nothing here
// updates content or deletes.

// ErrMemoryAlreadyInvalidated is MemoryTx.Invalidate's answer for a row
// that already carries an invalidation.
var ErrMemoryAlreadyInvalidated = errors.New("memory record is already invalidated")

// MemoryRecordRow is one memory_records row. Optional times are nil when
// unset; Invalidation is nil while the row is live.
type MemoryRecordRow struct {
	ID           string
	Twin         string
	Type         string
	Statement    string
	Sensitivity  string
	Subjects     []string
	Trigger      string
	SourceRef    string
	AuditSeq     int64
	WrittenBy    string
	ApprovedBy   string
	ObservedAt   time.Time
	ValidFrom    time.Time
	ValidUntil   *time.Time
	ReviewAfter  *time.Time
	Confidence   float64
	Supersedes   string
	Invalidation *MemoryInvalidationRow
}

// MemoryInvalidationRow is the set-once invalidation of a row.
type MemoryInvalidationRow struct {
	At           time.Time
	By           string
	Reason       string
	AuditSeq     int64
	SupersededBy string
}

// MemoryFilter narrows ListMemoryRecords. Twin is required.
type MemoryFilter struct {
	Twin     string
	Types    []string // empty = every type
	LiveOnly bool     // true = only rows with no invalidation
}

// MemoryTx is one write transaction over memory_records. It holds the
// database's write lock from its first statement (BEGIN IMMEDIATE), so a
// read-check-write inside it (bounds, "is the old row still live?") cannot
// race another writer, in this process or another one.
type MemoryTx struct {
	conn *sql.Conn
}

// MemoryTx runs fn in one transaction: everything fn does commits together,
// or, if fn returns an error (or panics), nothing does.
func (s *Store) MemoryTx(ctx context.Context, fn func(*MemoryTx) error) (err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// BEGIN IMMEDIATE rather than database/sql's BeginTx (a deferred BEGIN):
	// a deferred transaction that reads and then writes can fail with
	// SQLITE_BUSY on the lock upgrade instead of waiting on busy_timeout
	// when a second process is writing. The DSN is shared with every other
	// table, so the mode is chosen here, per transaction.
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			// A cancelled ctx must not stop the rollback.
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	if err := fn(&MemoryTx{conn: conn}); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

const memoryColumns = `id, twin, type, statement, sensitivity, subjects, prov_trigger, source_ref,
	audit_seq, written_by, approved_by, observed_at, valid_from, valid_until, review_after,
	confidence, supersedes, invalidated_at, invalidated_by, invalidation_reason,
	invalidation_audit_seq, superseded_by`

type memoryQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Insert appends r. A duplicate id is an error (the primary key refuses it).
func (t *MemoryTx) Insert(ctx context.Context, r MemoryRecordRow) error {
	if r.Twin == "" || r.ID == "" {
		return errors.New("memory record: twin and id are required")
	}
	if r.Invalidation != nil {
		return errors.New("memory record: cannot insert an already-invalidated row")
	}
	subjects, err := json.Marshal(nonNil(r.Subjects))
	if err != nil {
		return err
	}
	_, err = t.conn.ExecContext(ctx, `INSERT INTO memory_records (`+memoryColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, NULL, NULL)`,
		r.ID, r.Twin, r.Type, r.Statement, r.Sensitivity, string(subjects), r.Trigger, r.SourceRef,
		r.AuditSeq, r.WrittenBy, r.ApprovedBy, r.ObservedAt.UnixNano(), r.ValidFrom.UnixNano(),
		memoryNanosOrNil(r.ValidUntil), memoryNanosOrNil(r.ReviewAfter), r.Confidence, memoryNullIfEmpty(r.Supersedes))
	return err
}

// Invalidate sets id's invalidation, once. ErrNotFound if twin has no such
// row; ErrMemoryAlreadyInvalidated if it is already invalidated.
func (t *MemoryTx) Invalidate(ctx context.Context, twin, id string, inv MemoryInvalidationRow) error {
	res, err := t.conn.ExecContext(ctx, `UPDATE memory_records
		SET invalidated_at = ?, invalidated_by = ?, invalidation_reason = ?,
			invalidation_audit_seq = ?, superseded_by = ?
		WHERE id = ? AND twin = ? AND invalidated_at IS NULL`,
		inv.At.UnixNano(), inv.By, inv.Reason, inv.AuditSeq, memoryNullIfEmpty(inv.SupersededBy), id, twin)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 1 {
		return nil
	}
	if _, err := t.Get(ctx, twin, id); err != nil {
		return err
	}
	return ErrMemoryAlreadyInvalidated
}

// Get returns twin's row id, live or not, or ErrNotFound.
func (t *MemoryTx) Get(ctx context.Context, twin, id string) (MemoryRecordRow, error) {
	return getMemoryRecord(ctx, t.conn, twin, id)
}

// Live returns every row of twin that has no invalidation.
func (t *MemoryTx) Live(ctx context.Context, twin string) ([]MemoryRecordRow, error) {
	return listMemoryRecords(ctx, t.conn, MemoryFilter{Twin: twin, LiveOnly: true})
}

// GetMemoryRecord returns twin's row id, live or not, or ErrNotFound.
func (s *Store) GetMemoryRecord(ctx context.Context, twin, id string) (MemoryRecordRow, error) {
	return getMemoryRecord(ctx, s.db, twin, id)
}

// ListMemoryRecords returns f.Twin's rows, ordered by observed_at
// descending then id.
func (s *Store) ListMemoryRecords(ctx context.Context, f MemoryFilter) ([]MemoryRecordRow, error) {
	return listMemoryRecords(ctx, s.db, f)
}

func getMemoryRecord(ctx context.Context, q memoryQueryer, twin, id string) (MemoryRecordRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+memoryColumns+` FROM memory_records WHERE twin = ? AND id = ?`, twin, id)
	if err != nil {
		return MemoryRecordRow{}, err
	}
	out, err := scanMemoryRows(rows)
	if err != nil {
		return MemoryRecordRow{}, err
	}
	if len(out) == 0 {
		return MemoryRecordRow{}, ErrNotFound
	}
	return out[0], nil
}

func listMemoryRecords(ctx context.Context, q memoryQueryer, f MemoryFilter) ([]MemoryRecordRow, error) {
	if f.Twin == "" {
		return nil, errors.New("memory records: twin is required")
	}
	where := []string{"twin = ?"}
	args := []any{f.Twin}
	if len(f.Types) > 0 {
		where = append(where, "type IN (?"+strings.Repeat(", ?", len(f.Types)-1)+")")
		for _, t := range f.Types {
			args = append(args, t)
		}
	}
	if f.LiveOnly {
		where = append(where, "invalidated_at IS NULL")
	}
	rows, err := q.QueryContext(ctx, `SELECT `+memoryColumns+` FROM memory_records WHERE `+
		strings.Join(where, " AND ")+` ORDER BY observed_at DESC, id ASC`, args...)
	if err != nil {
		return nil, err
	}
	return scanMemoryRows(rows)
}

func scanMemoryRows(rows *sql.Rows) ([]MemoryRecordRow, error) {
	defer rows.Close()
	var out []MemoryRecordRow
	for rows.Next() {
		var (
			r                                   MemoryRecordRow
			subjects                            string
			observed, validFrom                 int64
			validUntil, reviewAfter, invAt      sql.NullInt64
			invSeq                              sql.NullInt64
			supersedes, invBy, invReason, supBy sql.NullString
		)
		if err := rows.Scan(&r.ID, &r.Twin, &r.Type, &r.Statement, &r.Sensitivity, &subjects, &r.Trigger,
			&r.SourceRef, &r.AuditSeq, &r.WrittenBy, &r.ApprovedBy, &observed, &validFrom, &validUntil,
			&reviewAfter, &r.Confidence, &supersedes, &invAt, &invBy, &invReason, &invSeq, &supBy); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(subjects), &r.Subjects); err != nil {
			return nil, fmt.Errorf("memory record %s: subjects: %w", r.ID, err)
		}
		if len(r.Subjects) == 0 {
			r.Subjects = nil
		}
		r.ObservedAt = memoryFromNanos(observed)
		r.ValidFrom = memoryFromNanos(validFrom)
		r.ValidUntil = memoryNanosPtr(validUntil)
		r.ReviewAfter = memoryNanosPtr(reviewAfter)
		r.Supersedes = supersedes.String
		if invAt.Valid {
			r.Invalidation = &MemoryInvalidationRow{
				At: memoryFromNanos(invAt.Int64), By: invBy.String, Reason: invReason.String,
				AuditSeq: invSeq.Int64, SupersededBy: supBy.String,
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func memoryFromNanos(n int64) time.Time { return time.Unix(0, n).UTC() }

func memoryNanosPtr(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := memoryFromNanos(n.Int64)
	return &t
}

func memoryNanosOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixNano()
}

func memoryNullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
