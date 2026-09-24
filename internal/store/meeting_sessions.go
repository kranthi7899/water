package store

import (
	"context"
	"database/sql"
	"time"
)

// MeetingSessionRow is one meeting_sessions row. EndedAt is nil while the
// session is still listening; EventID is empty when it was started by hand.
type MeetingSessionRow struct {
	ID        string
	StartedAt time.Time
	EndedAt   *time.Time
	EventID   string
}

// MeetingSegmentRow is one transcript segment. There is no External field:
// every segment is external by construction (the column is pinned to 1).
type MeetingSegmentRow struct {
	SessionID string
	At        time.Time
	Channel   string
	Text      string
}

func (s *Store) InsertMeetingSession(ctx context.Context, r MeetingSessionRow) error {
	var event any
	if r.EventID != "" {
		event = r.EventID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO meeting_sessions (id, started_at, event_id) VALUES (?, ?, ?)`,
		r.ID, r.StartedAt.UnixNano(), event)
	return err
}

// GetMeetingSession returns ErrNotFound for an unknown id.
func (s *Store) GetMeetingSession(ctx context.Context, id string) (MeetingSessionRow, error) {
	var started int64
	var ended sql.NullInt64
	var event sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT started_at, ended_at, event_id FROM meeting_sessions WHERE id = ?`, id).
		Scan(&started, &ended, &event)
	if err == sql.ErrNoRows {
		return MeetingSessionRow{}, ErrNotFound
	}
	if err != nil {
		return MeetingSessionRow{}, err
	}
	r := MeetingSessionRow{ID: id, StartedAt: time.Unix(0, started).UTC(), EventID: event.String}
	if ended.Valid {
		t := time.Unix(0, ended.Int64).UTC()
		r.EndedAt = &t
	}
	return r, nil
}

// EndMeetingSession sets ended_at once; ended is false, with no error, when
// the session was already ended (or does not exist).
func (s *Store) EndMeetingSession(ctx context.Context, id string, at time.Time) (ended bool, err error) {
	res, err := s.db.ExecContext(ctx, `UPDATE meeting_sessions SET ended_at = ? WHERE id = ? AND ended_at IS NULL`, at.UnixNano(), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) InsertMeetingSegment(ctx context.Context, r MeetingSegmentRow) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meeting_segments (session_id, at, channel, text, external) VALUES (?, ?, ?, ?, 1)`,
		r.SessionID, r.At.UnixNano(), r.Channel, r.Text)
	return err
}

// ListMeetingSegments returns a session's segments at or after since (zero
// means all), oldest first.
func (s *Store) ListMeetingSegments(ctx context.Context, sessionID string, since time.Time) ([]MeetingSegmentRow, error) {
	var from int64
	if !since.IsZero() {
		from = since.UnixNano()
	}
	rows, err := s.db.QueryContext(ctx, `SELECT at, channel, text FROM meeting_segments
		WHERE session_id = ? AND at >= ? ORDER BY at, id`, sessionID, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MeetingSegmentRow
	for rows.Next() {
		var at int64
		r := MeetingSegmentRow{SessionID: sessionID}
		if err := rows.Scan(&at, &r.Channel, &r.Text); err != nil {
			return nil, err
		}
		r.At = time.Unix(0, at).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
