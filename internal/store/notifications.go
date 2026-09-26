package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"
)

// Notification is one native notification, at most one per (RecordType,
// RecordID) ever — enforced by the notifications table's own UNIQUE
// constraint, not application bookkeeping, so it survives a daemon restart.
type Notification struct {
	ID          string
	RecordType  string
	RecordID    string
	Title       string
	Body        string
	CreatedAt   time.Time
	DeliveredAt *time.Time // nil until MarkNotificationDelivered
}

func newNotificationID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "ntf_" + hex.EncodeToString(b[:])
}

// InsertNotificationIfNew inserts n (generating n.ID if empty), or does
// nothing if a notification for the same (RecordType, RecordID) already
// exists. created reports which happened, so a caller can tell "first time
// we've notified about this record" from "we already had."
func (s *Store) InsertNotificationIfNew(ctx context.Context, n Notification) (created bool, err error) {
	if n.ID == "" {
		n.ID = newNotificationID()
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO notifications (id, record_type, record_id, title, body, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (record_type, record_id) DO NOTHING`,
		n.ID, n.RecordType, n.RecordID, n.Title, n.Body, n.CreatedAt.UnixNano())
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

// ListUndeliveredNotifications returns every notification not yet marked
// delivered, oldest first.
func (s *Store) ListUndeliveredNotifications(ctx context.Context) ([]Notification, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, record_type, record_id, title, body, created_at, delivered_at
		FROM notifications WHERE delivered_at IS NULL ORDER BY created_at ASC, rowid ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func scanNotification(sc interface{ Scan(...any) error }) (Notification, error) {
	var n Notification
	var created int64
	var delivered sql.NullInt64
	if err := sc.Scan(&n.ID, &n.RecordType, &n.RecordID, &n.Title, &n.Body, &created, &delivered); err != nil {
		return Notification{}, err
	}
	n.CreatedAt = time.Unix(0, created).UTC()
	if delivered.Valid {
		t := time.Unix(0, delivered.Int64).UTC()
		n.DeliveredAt = &t
	}
	return n, nil
}

// MarkNotificationDelivered sets id's delivered_at to now. It is a no-op,
// with no error, when id is already delivered or does not exist — a caller
// re-marking a notification is never itself an error.
func (s *Store) MarkNotificationDelivered(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE notifications SET delivered_at = ? WHERE id = ? AND delivered_at IS NULL`,
		time.Now().UTC().UnixNano(), id)
	return err
}
