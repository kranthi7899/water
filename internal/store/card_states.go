package store

import (
	"context"
	"database/sql"
	"time"
)

// CardState is the durable record of a decision card the CEO already acted
// on outside its normal approve/deny flow (dismissed it, or staged it for
// later). Decision cards are never persisted themselves — they're rebuilt
// fresh on every Trigger.Run — so without this table a dismissed card would
// simply reappear on the next request. "No state" (GetCardState's
// ErrNotFound) is the normal case for most cards, not an error condition
// callers need to work around beyond checking it.
type CardState struct {
	CardID     string
	Status     string // "dismissed" or "staged"
	Reason     string
	ApprovalID string
	DecidedAt  time.Time
}

// SetCardState upserts cs by CardID.
func (s *Store) SetCardState(ctx context.Context, cs CardState) error {
	if cs.DecidedAt.IsZero() {
		cs.DecidedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO card_states (card_id, status, reason, approval_id, decided_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (card_id) DO UPDATE SET status = excluded.status, reason = excluded.reason,
			approval_id = excluded.approval_id, decided_at = excluded.decided_at`,
		cs.CardID, cs.Status, cs.Reason, cs.ApprovalID, cs.DecidedAt.UnixNano())
	return err
}

// GetCardState returns ErrNotFound when cardID has no recorded state.
func (s *Store) GetCardState(ctx context.Context, cardID string) (CardState, error) {
	var cs CardState
	var decided int64
	err := s.db.QueryRowContext(ctx, `SELECT card_id, status, reason, approval_id, decided_at FROM card_states WHERE card_id = ?`, cardID).
		Scan(&cs.CardID, &cs.Status, &cs.Reason, &cs.ApprovalID, &decided)
	if err == sql.ErrNoRows {
		return CardState{}, ErrNotFound
	}
	if err != nil {
		return CardState{}, err
	}
	cs.DecidedAt = time.Unix(0, decided).UTC()
	return cs, nil
}

// RepointCardState moves every staged card that points at approval
// fromApprovalID to toApprovalID, and reports how many it moved. An approval
// edit (approvals.Queue.Edit) voids the old envelope and proposes a new one;
// without this the card would keep pointing at the voided envelope and look
// unstaged, inviting a second staging. Dismissed cards are left alone (their
// approval_id is history), and an id no card points at moves nothing.
func (s *Store) RepointCardState(ctx context.Context, fromApprovalID, toApprovalID string) (int64, error) {
	if fromApprovalID == "" || toApprovalID == "" {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, `UPDATE card_states SET approval_id = ? WHERE status = 'staged' AND approval_id = ?`,
		toApprovalID, fromApprovalID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// StagedCardStates returns every card in status "staged", keyed by card id,
// so GET /v1/decisions can say which open cards already wait on an
// envelope without a per-card lookup.
func (s *Store) StagedCardStates(ctx context.Context) (map[string]CardState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT card_id, status, reason, approval_id, decided_at FROM card_states WHERE status = 'staged'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]CardState{}
	for rows.Next() {
		var cs CardState
		var decided int64
		if err := rows.Scan(&cs.CardID, &cs.Status, &cs.Reason, &cs.ApprovalID, &decided); err != nil {
			return nil, err
		}
		cs.DecidedAt = time.Unix(0, decided).UTC()
		out[cs.CardID] = cs
	}
	return out, rows.Err()
}

// DismissedCardIDs returns every card id currently in status "dismissed",
// for the needsyou/decisions-list filtering (Slice V-3) to check directly
// without a per-card lookup.
func (s *Store) DismissedCardIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT card_id FROM card_states WHERE status = 'dismissed'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
