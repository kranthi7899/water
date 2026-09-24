package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrMeetingEnded is InsertMeetingSegment's answer for a session that has
// already been stopped.
var ErrMeetingEnded = errors.New("meeting session has ended")

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
	// At is when the speech began (the client's timestamp); it orders the
	// transcript.
	At time.Time
	// ReceivedAt is when the daemon stored the segment; ListMeetingSegments'
	// since bound applies to it. Zero on insert means At.
	ReceivedAt time.Time
	Channel    string
	Text       string
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

// InsertMeetingSegment appends a segment only while the session is open,
// checked in the same statement as the insert so a concurrent stop can't
// slip between the check and the write. It returns ErrMeetingEnded for a
// stopped session and ErrNotFound for an unknown one.
func (s *Store) InsertMeetingSegment(ctx context.Context, r MeetingSegmentRow) error {
	received := r.ReceivedAt
	if received.IsZero() {
		received = r.At
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO meeting_segments (session_id, at, received_at, channel, text, external)
		SELECT ?, ?, ?, ?, ?, 1 WHERE EXISTS (SELECT 1 FROM meeting_sessions WHERE id = ? AND ended_at IS NULL)`,
		r.SessionID, r.At.UnixNano(), received.UnixNano(), r.Channel, r.Text, r.SessionID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return err
	}
	if _, err := s.GetMeetingSession(ctx, r.SessionID); err != nil {
		return err
	}
	return ErrMeetingEnded
}

// ListMeetingSegments returns a session's segments received at or after
// since (zero means all), ordered by when their speech began.
func (s *Store) ListMeetingSegments(ctx context.Context, sessionID string, since time.Time) ([]MeetingSegmentRow, error) {
	var from int64
	if !since.IsZero() {
		from = since.UnixNano()
	}
	rows, err := s.db.QueryContext(ctx, `SELECT at, COALESCE(received_at, at), channel, text FROM meeting_segments
		WHERE session_id = ? AND COALESCE(received_at, at) >= ? ORDER BY at, id`, sessionID, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MeetingSegmentRow
	for rows.Next() {
		var at, received int64
		r := MeetingSegmentRow{SessionID: sessionID}
		if err := rows.Scan(&at, &received, &r.Channel, &r.Text); err != nil {
			return nil, err
		}
		r.At = time.Unix(0, at).UTC()
		r.ReceivedAt = time.Unix(0, received).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
