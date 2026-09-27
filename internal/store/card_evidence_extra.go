package store

import (
	"context"
	"time"
)

// CardEvidenceExtra is one piece of evidence attached to an existing
// decision card after the fact (docs/slices/UI.md Phase 1c; e.g. a future
// research report's "Attach" action). Rows are appended, never edited, and
// apply to whichever version of the card (computed or record-sourced) wins
// decisions.Merge for their CardID. Untrusted marks the content as
// attacker-reachable, the same taint convention as store.Meta.External and
// approvals.Envelope's own untrusted-content handling: Merge propagates it
// into the merged card's Untrusted rather than dropping it silently.
type CardEvidenceExtra struct {
	ID        int64
	CardID    string
	Source    string
	Text      string
	Untrusted bool
	AddedAt   time.Time
}

// AddCardEvidenceExtra appends e, generating its ID and defaulting AddedAt
// to now when zero.
func (s *Store) AddCardEvidenceExtra(ctx context.Context, e CardEvidenceExtra) (CardEvidenceExtra, error) {
	if e.AddedAt.IsZero() {
		e.AddedAt = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO card_evidence_extra (card_id, source, text, untrusted, added_at)
		VALUES (?, ?, ?, ?, ?)`,
		e.CardID, e.Source, e.Text, boolToInt(e.Untrusted), e.AddedAt.UnixNano())
	if err != nil {
		return CardEvidenceExtra{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return CardEvidenceExtra{}, err
	}
	e.ID = id
	return e, nil
}

// ListCardEvidenceExtra returns cardID's extra evidence, oldest first.
func (s *Store) ListCardEvidenceExtra(ctx context.Context, cardID string) ([]CardEvidenceExtra, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, card_id, source, text, untrusted, added_at
		FROM card_evidence_extra WHERE card_id = ? ORDER BY added_at ASC, id ASC`, cardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCardEvidenceExtra(rows)
}

// AllCardEvidenceExtra returns every row, grouped by card id, the same
// "read everything, key by card id" shape StagedCardStates uses for
// card_states -- what decisions.Merge needs to attach extra evidence to
// whichever card (computed or record-sourced) wins for each id, in one
// query rather than one per card.
func (s *Store) AllCardEvidenceExtra(ctx context.Context) (map[string][]CardEvidenceExtra, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, card_id, source, text, untrusted, added_at
		FROM card_evidence_extra ORDER BY added_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all, err := scanCardEvidenceExtra(rows)
	if err != nil {
		return nil, err
	}
	out := map[string][]CardEvidenceExtra{}
	for _, e := range all {
		out[e.CardID] = append(out[e.CardID], e)
	}
	return out, nil
}

func scanCardEvidenceExtra(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]CardEvidenceExtra, error) {
	var out []CardEvidenceExtra
	for rows.Next() {
		var e CardEvidenceExtra
		var untrusted, added int64
		if err := rows.Scan(&e.ID, &e.CardID, &e.Source, &e.Text, &untrusted, &added); err != nil {
			return nil, err
		}
		e.Untrusted = untrusted != 0
		e.AddedAt = time.Unix(0, added).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
