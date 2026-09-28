package store

import (
	"context"
	"database/sql"
	"time"
)

// CardActionState is the durable record of one decision-card action being
// staged into an approval envelope (docs/slices/UI.md Phase 1c). It
// replaces card_states' role for staging specifically: card_states itself
// is unchanged and kept for dismiss (see card_states.go). Keying by
// (CardID, ActionID) rather than CardID alone is the whole point -- a card
// that proposes two actions (e.g. an extension email and a priority bump)
// can have each staged into its own envelope independently, which the old
// one-envelope-per-card design could not represent: staging a second action
// while the first was pending simply clobbered it.
//
// ActionID identifies one of a card's proposed actions. It is opaque to
// this package -- a caller may key by the action's function name when a
// card names each function at most once, or by decisions.Suggestion.ID when
// two suggestions can share a function (docs/slices/UI.md finding 20).
type CardActionState struct {
	CardID     string
	ActionID   string
	Status     string // "staged" (the only status this table models today)
	ApprovalID string
	StagedAt   time.Time
}

// SetCardActionState upserts cs by (CardID, ActionID). StagedAt defaults to
// now when zero.
func (s *Store) SetCardActionState(ctx context.Context, cs CardActionState) error {
	if cs.StagedAt.IsZero() {
		cs.StagedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO card_action_states (card_id, action_id, status, approval_id, staged_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (card_id, action_id) DO UPDATE SET status = excluded.status,
			approval_id = excluded.approval_id, staged_at = excluded.staged_at`,
		cs.CardID, cs.ActionID, cs.Status, cs.ApprovalID, cs.StagedAt.UnixNano())
	return err
}

// GetCardActionState returns ErrNotFound when (cardID, actionID) has no
// recorded state.
func (s *Store) GetCardActionState(ctx context.Context, cardID, actionID string) (CardActionState, error) {
	var cs CardActionState
	var staged int64
	err := s.db.QueryRowContext(ctx, `SELECT card_id, action_id, status, approval_id, staged_at
		FROM card_action_states WHERE card_id = ? AND action_id = ?`, cardID, actionID).
		Scan(&cs.CardID, &cs.ActionID, &cs.Status, &cs.ApprovalID, &staged)
	if err == sql.ErrNoRows {
		return CardActionState{}, ErrNotFound
	}
	if err != nil {
		return CardActionState{}, err
	}
	cs.StagedAt = time.Unix(0, staged).UTC()
	return cs, nil
}

// CardActionStates returns every staged action of cardID, in no particular
// order beyond staged_at ascending.
func (s *Store) CardActionStates(ctx context.Context, cardID string) ([]CardActionState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT card_id, action_id, status, approval_id, staged_at
		FROM card_action_states WHERE card_id = ? ORDER BY staged_at ASC`, cardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CardActionState
	for rows.Next() {
		var cs CardActionState
		var staged int64
		if err := rows.Scan(&cs.CardID, &cs.ActionID, &cs.Status, &cs.ApprovalID, &staged); err != nil {
			return nil, err
		}
		cs.StagedAt = time.Unix(0, staged).UTC()
		out = append(out, cs)
	}
	return out, rows.Err()
}
