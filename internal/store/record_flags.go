package store

import (
	"context"
	"database/sql"
	"time"
)

// RecordFlag flags a seeded/demo row in an existing, unrelated normalized
// table (messages, events, issues, ...) by that row's own (Source,
// SourceID) identity (store.Meta) plus its Table() name -- finding 16's
// fix (docs/slices/UI.md Phase 1d, U2-A).
//
// This is a side table on purpose: a normalized table's Meta/schema is
// shared code many callers already depend on, and adding a
// provenance/simulated pair there for a Phase-7-only demo-marking concern
// would touch far more surface (every reader and writer of that table)
// than flagging it from outside does. No existing normalized table's
// schema or Go struct changes because of this table.
//
// Nothing populates or reads this table yet: a future Phase 7
// demo-seeding pass is the intended writer, and a future render path is
// the intended reader. This phase only builds and proves the mechanism.
type RecordFlag struct {
	TableName  string // e.g. "messages", "events", "issues" -- a Record's Table()
	Source     string // Meta.Source
	SourceID   string // Meta.SourceID
	Provenance string // "" or "demo_seed" (U21)
	Simulated  bool
	FlaggedAt  time.Time
}

const recordFlagCols = `table_name, source, source_id, provenance, simulated, flagged_at`

func scanRecordFlag(sc interface{ Scan(...any) error }) (RecordFlag, error) {
	var f RecordFlag
	var simulated, flagged int64
	if err := sc.Scan(&f.TableName, &f.Source, &f.SourceID, &f.Provenance, &simulated, &flagged); err != nil {
		return RecordFlag{}, err
	}
	f.Simulated = simulated != 0
	f.FlaggedAt = time.Unix(0, flagged).UTC()
	return f, nil
}

// SetRecordFlag inserts or replaces f by (TableName, Source, SourceID).
// FlaggedAt defaults to now when zero.
func (s *Store) SetRecordFlag(ctx context.Context, f RecordFlag) error {
	if f.FlaggedAt.IsZero() {
		f.FlaggedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO record_flags (`+recordFlagCols+`) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (table_name, source, source_id) DO UPDATE SET provenance = excluded.provenance,
			simulated = excluded.simulated, flagged_at = excluded.flagged_at`,
		f.TableName, f.Source, f.SourceID, f.Provenance, boolToInt(f.Simulated), f.FlaggedAt.UnixNano())
	return err
}

// GetRecordFlag returns ErrNotFound when (tableName, source, sourceID) has
// not been flagged. Callers that only need a yes/no answer (e.g. "is this
// row a demo seed?") check errors.Is(err, ErrNotFound) rather than this
// package inventing a separate boolean-returning method.
func (s *Store) GetRecordFlag(ctx context.Context, tableName, source, sourceID string) (RecordFlag, error) {
	f, err := scanRecordFlag(s.db.QueryRowContext(ctx, `SELECT `+recordFlagCols+`
		FROM record_flags WHERE table_name = ? AND source = ? AND source_id = ?`, tableName, source, sourceID))
	if err == sql.ErrNoRows {
		return RecordFlag{}, ErrNotFound
	}
	return f, err
}

// ListRecordFlags returns every flagged row for tableName, oldest first.
func (s *Store) ListRecordFlags(ctx context.Context, tableName string) ([]RecordFlag, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordFlagCols+`
		FROM record_flags WHERE table_name = ? ORDER BY flagged_at ASC`, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecordFlag
	for rows.Next() {
		f, err := scanRecordFlag(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
