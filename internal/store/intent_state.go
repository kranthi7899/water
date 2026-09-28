package store

import (
	"context"
	"time"
)

// SetIntentState upserts a learned intent's enable/disable state. Setting
// disabled=false deletes the row rather than leaving a stale disabled=0 row
// behind, since ListIntentStates only ever returns currently-disabled ids.
func (s *Store) SetIntentState(ctx context.Context, id string, disabled bool, reason string, at time.Time) error {
	if !disabled {
		_, err := s.db.ExecContext(ctx, `DELETE FROM intent_state WHERE intent_id = ?`, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO intent_state (intent_id, disabled, reason, at) VALUES (?, 1, ?, ?)
		ON CONFLICT (intent_id) DO UPDATE SET disabled = 1, reason = excluded.reason, at = excluded.at`,
		id, reason, at.UnixNano())
	return err
}

// ListIntentStates returns every currently-disabled learned intent id mapped
// to its disable reason. An id absent from the map is enabled.
func (s *Store) ListIntentStates(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT intent_id, reason FROM intent_state WHERE disabled = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, reason string
		if err := rows.Scan(&id, &reason); err != nil {
			return nil, err
		}
		out[id] = reason
	}
	return out, rows.Err()
}
