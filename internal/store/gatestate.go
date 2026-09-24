package store

import (
	"context"
	"database/sql"
	"time"
)

// RecordHit persists one consumed rate/usage-cap hit for key at "at". The
// gate calls this instead of keeping the sliding window only in memory, so
// caps survive a daemon restart.
func (s *Store) RecordHit(ctx context.Context, key string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO rate_hits (key, at) VALUES (?, ?)`, key, at.UnixNano())
	return err
}

// CountHitsSince counts key's hits strictly after since (the sliding
// window). Strict, not >=, so a hit exactly `per` old falls out of the
// window at the same instant the in-memory implementation would drop it.
func (s *Store) CountHitsSince(ctx context.Context, key string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rate_hits WHERE key = ? AND at > ?`, key, since.UnixNano()).Scan(&n)
	return n, err
}

// PruneHitsBefore deletes hits older than before, keeping the table bounded.
// Callers run it opportunistically; a missed prune only costs disk space, so
// its error is safe to ignore.
func (s *Store) PruneHitsBefore(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM rate_hits WHERE at < ?`, before.UnixNano())
	return err
}

// LoadAuditAnchor returns the last (seq, hash) the gate anchored, if any.
func (s *Store) LoadAuditAnchor(ctx context.Context) (seq int64, hash string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT seq, hash FROM audit_anchor WHERE id = 1`).Scan(&seq, &hash)
	if err == sql.ErrNoRows {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	return seq, hash, true, nil
}

// SaveAuditAnchor records the audit log's current tail.
func (s *Store) SaveAuditAnchor(ctx context.Context, seq int64, hash string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_anchor (id, seq, hash) VALUES (1, ?, ?)
		ON CONFLICT (id) DO UPDATE SET seq = excluded.seq, hash = excluded.hash`, seq, hash)
	return err
}
