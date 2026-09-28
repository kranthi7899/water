package store

import (
	"context"
	"database/sql"
	"time"
)

// ListMeetingSessions returns up to limit meeting sessions, most recently
// started first, for the workspace UI's Meetings list (Slice V-ui). limit <=
// 0 means unlimited, matching ThreadMessages' and ListRoutes' convention.
// It reads the existing meeting_sessions table only; no schema change.
func (s *Store) ListMeetingSessions(ctx context.Context, limit int) ([]MeetingSessionRow, error) {
	q := `SELECT id, started_at, ended_at, event_id, recap_signals, project_guess_id, project_guess_confidence
		FROM meeting_sessions ORDER BY started_at DESC, rowid DESC`
	var args []any
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MeetingSessionRow
	for rows.Next() {
		var r MeetingSessionRow
		var started int64
		var ended sql.NullInt64
		var event, signals, guessID sql.NullString
		var guessConf sql.NullFloat64
		if err := rows.Scan(&r.ID, &started, &ended, &event, &signals, &guessID, &guessConf); err != nil {
			return nil, err
		}
		r.StartedAt = time.Unix(0, started).UTC()
		r.EventID = event.String
		r.RecapSignals = signals.String
		r.ProjectGuessID = guessID.String
		if ended.Valid {
			t := time.Unix(0, ended.Int64).UTC()
			r.EndedAt = &t
		}
		if guessConf.Valid {
			v := guessConf.Float64
			r.ProjectGuessConfidence = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EventBySourceID returns the calendar event whose source_id is sourceID,
// whatever its source, or ErrNotFound. A meeting session records only the
// event's SourceID (MeetingSessionRow.EventID), not which connector it came
// from, so this is how the Meetings list names a session's event.
func EventBySourceID(ctx context.Context, s *Store, sourceID string) (*Event, error) {
	out, err := list[Event](ctx, s, "source_id = ?", []any{sourceID}, 1, "events")
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return &out[0], nil
}
