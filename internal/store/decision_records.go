package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// DecisionRecord is one persisted decisions.Card (docs/slices/UI.md Phase
// 1c, U13). CardJSON is the full serialized decisions.Card; this package
// never decodes it. internal/decisions already imports internal/store (to
// build a Card from a store.Record), so decoding/encoding CardJSON happens
// on that side, not here -- the same one-way layering build.go documents
// for its own dependency on this package.
//
// Once a card id has a row here, decisions.Merge treats it as authoritative
// over whatever the live classifier/trigger would compute for that same
// id: a persisted record wins.
type DecisionRecord struct {
	CardID     string
	CardJSON   string
	Provenance string // "" or "demo_seed" (U21)
	Simulated  bool
	CreatedAt  time.Time
}

// UpsertDecisionRecord inserts or replaces r by CardID. CreatedAt defaults
// to now when zero.
func (s *Store) UpsertDecisionRecord(ctx context.Context, r DecisionRecord) error {
	if r.CardID == "" {
		return errors.New("store: decision record card_id is required")
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO decision_records (card_id, card_json, provenance, simulated, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (card_id) DO UPDATE SET card_json = excluded.card_json, provenance = excluded.provenance,
			simulated = excluded.simulated, created_at = excluded.created_at`,
		r.CardID, r.CardJSON, r.Provenance, boolToInt(r.Simulated), r.CreatedAt.UnixNano())
	return err
}

// GetDecisionRecord returns ErrNotFound when cardID has no persisted record.
func (s *Store) GetDecisionRecord(ctx context.Context, cardID string) (DecisionRecord, error) {
	var r DecisionRecord
	var simulated int64
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT card_id, card_json, provenance, simulated, created_at FROM decision_records WHERE card_id = ?`, cardID).
		Scan(&r.CardID, &r.CardJSON, &r.Provenance, &simulated, &created)
	if err == sql.ErrNoRows {
		return DecisionRecord{}, ErrNotFound
	}
	if err != nil {
		return DecisionRecord{}, err
	}
	r.Simulated = simulated != 0
	r.CreatedAt = time.Unix(0, created).UTC()
	return r, nil
}

// ListDecisionRecords returns every persisted record, oldest first, for
// decisions.Merge to combine with the cards computed live for the same
// request.
func (s *Store) ListDecisionRecords(ctx context.Context) ([]DecisionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT card_id, card_json, provenance, simulated, created_at FROM decision_records ORDER BY created_at ASC, card_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DecisionRecord
	for rows.Next() {
		var r DecisionRecord
		var simulated, created int64
		if err := rows.Scan(&r.CardID, &r.CardJSON, &r.Provenance, &simulated, &created); err != nil {
			return nil, err
		}
		r.Simulated = simulated != 0
		r.CreatedAt = time.Unix(0, created).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteDecisionRecord removes cardID's persisted record, if any. It never
// errors on a missing row: "no longer pinned" and "never was pinned" are
// the same outcome to a caller.
func (s *Store) DeleteDecisionRecord(ctx context.Context, cardID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM decision_records WHERE card_id = ?`, cardID)
	return err
}
