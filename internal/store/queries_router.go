package store

import (
	"context"
	"database/sql"
	"net/mail"
	"strings"
	"time"

	"water/internal/nervous/slots"
)

// LatestMessages returns the most recently sent messages, newest first.
func (s *Store) LatestMessages(ctx context.Context, limit int) ([]Message, error) {
	var probe Message
	cols := columns(&probe)
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.name
	}
	q := "SELECT " + strings.Join(names, ", ") + " FROM messages ORDER BY sent_at DESC"
	var args []any
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	return queryMessages(ctx, s, q, args...)
}

// MessagesFrom returns the most recent messages from a sender address,
// newest first. email is matched against the raw sender field, which holds
// either a bare address or an RFC 5322 "Name <email>" form.
func (s *Store) MessagesFrom(ctx context.Context, email string, limit int) ([]Message, error) {
	var probe Message
	cols := columns(&probe)
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.name
	}
	q := "SELECT " + strings.Join(names, ", ") + " FROM messages WHERE sender LIKE ? ORDER BY sent_at DESC"
	args := []any{"%" + email + "%"}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	return queryMessages(ctx, s, q, args...)
}

func queryMessages(ctx context.Context, s *Store, q string, args ...any) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var rec Message
		cols := columns(&rec)
		dests := make([]any, len(cols))
		fills := make([]func() error, len(cols))
		for i, c := range cols {
			dests[i], fills[i] = scanTarget(c.v)
		}
		if err := rows.Scan(dests...); err != nil {
			return nil, err
		}
		for _, f := range fills {
			if err := f(); err != nil {
				return nil, err
			}
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// CountMessagesSince returns how many messages were sent at or after since.
func (s *Store) CountMessagesSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE sent_at >= ?`, since.UnixNano()).Scan(&n)
	return n, err
}

// Senders returns distinct message senders at or after since, most recently
// seen first, parsed from the raw "Name <email>" (or bare-address) sender
// field. Malformed sender fields are skipped rather than erroring the whole
// call. limit caps the number of distinct people returned; 0 means no cap.
func (s *Store) Senders(ctx context.Context, since time.Time, limit int) ([]slots.Person, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sender FROM messages WHERE sent_at >= ? ORDER BY sent_at DESC`, since.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []slots.Person
	for rows.Next() {
		var sender string
		if err := rows.Scan(&sender); err != nil {
			return nil, err
		}
		if sender == "" {
			continue
		}
		addr, err := mail.ParseAddress(sender)
		if err != nil {
			continue
		}
		key := strings.ToLower(addr.Address)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, slots.Person{Name: addr.Name, Email: addr.Address})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

// NextEvent returns the soonest event whose start_at is strictly after
// after. It returns ErrNotFound, like Get, when there is none.
func (s *Store) NextEvent(ctx context.Context, after time.Time) (*Event, error) {
	var probe Event
	cols := columns(&probe)
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.name
	}
	q := "SELECT " + strings.Join(names, ", ") + " FROM events WHERE start_at > ? ORDER BY start_at ASC LIMIT 1"
	row := s.db.QueryRowContext(ctx, q, after.UnixNano())
	var rec Event
	recCols := columns(&rec)
	dests := make([]any, len(recCols))
	fills := make([]func() error, len(recCols))
	for i, c := range recCols {
		dests[i], fills[i] = scanTarget(c.v)
	}
	if err := row.Scan(dests...); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	for _, f := range fills {
		if err := f(); err != nil {
			return nil, err
		}
	}
	return &rec, nil
}

// CursorUpdatedAt returns when a sync cursor key was last set. ok is false,
// with no error, when the key has never been set.
func (s *Store) CursorUpdatedAt(ctx context.Context, key string) (time.Time, bool, error) {
	var updatedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT updated_at FROM sync_cursors WHERE key = ?`, key).Scan(&updatedAt)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return time.Unix(0, updatedAt).UTC(), true, nil
}
