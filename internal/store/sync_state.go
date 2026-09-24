package store

import (
	"context"
	"database/sql"
	"time"
)

// GetCursor returns the last-persisted value for an incremental-sync cursor
// key (e.g. "gmail:history_id", "gcal:primary:sync_token"). ok is false, with
// no error, when the key has never been set.
func (s *Store) GetCursor(ctx context.Context, key string) (value string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT value FROM sync_cursors WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// SetCursor upserts a cursor's value, overwriting whatever was there before.
func (s *Store) SetCursor(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sync_cursors (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().UnixNano())
	return err
}

// DeleteCursor drops a cursor, e.g. after the connector reports it expired
// (Gmail's historyId too old, gcal's syncToken invalidated), so the next
// sync tick falls back to a full fetch and re-establishes a fresh one.
func (s *Store) DeleteCursor(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sync_cursors WHERE key = ?`, key)
	return err
}

// GetBrief returns the cached morning brief for a local calendar day
// ("YYYY-MM-DD"). ok is false, with no error, when nothing is cached yet.
func (s *Store) GetBrief(ctx context.Context, day string) (text string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT text FROM briefs WHERE day = ?`, day).Scan(&text)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return text, true, nil
}

// SetBrief upserts the cached brief text for a day.
func (s *Store) SetBrief(ctx context.Context, day, text string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO briefs (day, text, created_at) VALUES (?, ?, ?)
		ON CONFLICT (day) DO UPDATE SET text = excluded.text, created_at = excluded.created_at`,
		day, text, time.Now().UnixNano())
	return err
}
