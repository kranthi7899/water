package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"
)

// Thread is a conversation between the CEO and the twin, optionally anchored
// to one other record (a decision, approval, message or meeting). Unlike
// every type in records.go/roster_records.go/finance_records.go, Thread is
// NOT a normalized Record: its id is application-generated (thr_<random>,
// see newThreadID) and addressed directly, since nothing upstream gives it a
// (source, source_id) identity to key off of.
type Thread struct {
	ID    string
	Title string
	// AnchorType/AnchorID name the one other record this thread is about
	// ("" for a free-standing thread with no anchor). AnchorContext is a
	// snapshot of that record (e.g. a rendered decision card) captured once
	// at creation time, so opening the thread never re-fetches or rebuilds
	// what it was originally about. AnchorUntrusted marks that snapshot as
	// attacker-reachable content (an email body, meeting notes): the UI must
	// render it as text only, never as markup.
	AnchorType      string
	AnchorID        string
	AnchorContext   string
	AnchorUntrusted bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ThreadMessage is one turn in a Thread, CEO or twin, in conversation order.
// Channel/TaskID carry the turn's own routing metadata through, so a message
// can be traced back to the route_log row (or async task) that produced it.
type ThreadMessage struct {
	ID        int64
	ThreadID  string
	Role      string
	Channel   string
	TaskID    string
	Text      string
	CreatedAt time.Time
}

func newThreadID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "thr_" + hex.EncodeToString(b[:])
}

const threadCols = `id, title, anchor_type, anchor_id, anchor_context, anchor_untrusted, created_at, updated_at`

func scanThread(sc interface{ Scan(...any) error }) (Thread, error) {
	var t Thread
	var untrusted int64
	var created, updated int64
	if err := sc.Scan(&t.ID, &t.Title, &t.AnchorType, &t.AnchorID, &t.AnchorContext, &untrusted, &created, &updated); err != nil {
		return Thread{}, err
	}
	t.AnchorUntrusted = untrusted != 0
	t.CreatedAt = time.Unix(0, created).UTC()
	t.UpdatedAt = time.Unix(0, updated).UTC()
	return t, nil
}

// CreateThread inserts t, generating an id if t.ID is empty and setting
// CreatedAt/UpdatedAt to now.
func (s *Store) CreateThread(ctx context.Context, t Thread) (Thread, error) {
	if t.ID == "" {
		t.ID = newThreadID()
	}
	now := time.Now().UTC()
	t.CreatedAt, t.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx, `INSERT INTO threads (`+threadCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Title, t.AnchorType, t.AnchorID, t.AnchorContext, boolToInt(t.AnchorUntrusted), t.CreatedAt.UnixNano(), t.UpdatedAt.UnixNano())
	if err != nil {
		return Thread{}, err
	}
	return t, nil
}

// GetOrCreateThreadForAnchor returns the thread anchored to (anchorType,
// anchorID), creating it (with title/anchorContext/untrusted) if none exists
// yet. created reports which happened. It is race-safe under concurrent
// callers: the insert and its uniqueness check happen in one statement
// (INSERT ... ON CONFLICT ... DO NOTHING against the partial unique index
// threads_anchor), the same compare-and-swap idiom TransitionApproval
// (approvals) and EndMeetingSession (meeting_sessions.go) use elsewhere in
// this package — whichever caller's insert is ignored on conflict simply
// reads back the row that won.
func (s *Store) GetOrCreateThreadForAnchor(ctx context.Context, anchorType, anchorID, title, anchorContext string, untrusted bool) (Thread, bool, error) {
	t := Thread{
		ID: newThreadID(), Title: title, AnchorType: anchorType, AnchorID: anchorID,
		AnchorContext: anchorContext, AnchorUntrusted: untrusted,
	}
	now := time.Now().UTC()
	t.CreatedAt, t.UpdatedAt = now, now
	res, err := s.db.ExecContext(ctx, `INSERT INTO threads (`+threadCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (anchor_type, anchor_id) WHERE anchor_type <> '' DO NOTHING`,
		t.ID, t.Title, t.AnchorType, t.AnchorID, t.AnchorContext, boolToInt(t.AnchorUntrusted), t.CreatedAt.UnixNano(), t.UpdatedAt.UnixNano())
	if err != nil {
		return Thread{}, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Thread{}, false, err
	}
	if n == 1 {
		return t, true, nil
	}
	existing, err := scanThread(s.db.QueryRowContext(ctx, `SELECT `+threadCols+` FROM threads WHERE anchor_type = ? AND anchor_id = ?`, anchorType, anchorID))
	if err != nil {
		return Thread{}, false, err
	}
	return existing, false, nil
}

// GetThreadByAnchor returns the thread anchored to (anchorType, anchorID),
// or ErrNotFound. Unlike GetOrCreateThreadForAnchor it never creates one, so
// a caller can reopen an existing thread without first rebuilding the
// anchor snapshot a new one would need (the anchored record may no longer
// exist — a decision card that has since closed).
func (s *Store) GetThreadByAnchor(ctx context.Context, anchorType, anchorID string) (Thread, error) {
	if anchorType == "" {
		return Thread{}, ErrNotFound
	}
	t, err := scanThread(s.db.QueryRowContext(ctx, `SELECT `+threadCols+` FROM threads WHERE anchor_type = ? AND anchor_id = ?`, anchorType, anchorID))
	if err == sql.ErrNoRows {
		return Thread{}, ErrNotFound
	}
	return t, err
}

// GetThread returns ErrNotFound for an unknown id.
func (s *Store) GetThread(ctx context.Context, id string) (Thread, error) {
	t, err := scanThread(s.db.QueryRowContext(ctx, `SELECT `+threadCols+` FROM threads WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Thread{}, ErrNotFound
	}
	return t, err
}

// ListThreads returns every thread, newest-updated first — for a sidebar
// list. rowid (SQLite's implicit row order) breaks ties between threads
// updated in the same instant in insertion order.
func (s *Store) ListThreads(ctx context.Context) ([]Thread, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+threadCols+` FROM threads ORDER BY updated_at DESC, rowid DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AppendThreadMessage inserts m, sets its CreatedAt/ID, and touches the
// parent thread's updated_at so ListThreads reflects the new activity.
func (s *Store) AppendThreadMessage(ctx context.Context, m ThreadMessage) (ThreadMessage, error) {
	m.CreatedAt = time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `INSERT INTO thread_messages (thread_id, role, channel, task_id, text, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		m.ThreadID, m.Role, m.Channel, m.TaskID, m.Text, m.CreatedAt.UnixNano())
	if err != nil {
		return ThreadMessage{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return ThreadMessage{}, err
	}
	m.ID = id
	if _, err := s.db.ExecContext(ctx, `UPDATE threads SET updated_at = ? WHERE id = ?`, m.CreatedAt.UnixNano(), m.ThreadID); err != nil {
		return ThreadMessage{}, err
	}
	return m, nil
}

// ThreadMessages returns threadID's messages oldest first (conversation
// reading order), optionally capped at limit (0 means unlimited, matching
// ListRoutes' own limit=0-means-unlimited convention in route_log.go).
func (s *Store) ThreadMessages(ctx context.Context, threadID string, limit int) ([]ThreadMessage, error) {
	q := `SELECT id, thread_id, role, channel, task_id, text, created_at FROM thread_messages WHERE thread_id = ? ORDER BY created_at ASC, id ASC`
	args := []any{threadID}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThreadMessage
	for rows.Next() {
		var m ThreadMessage
		var created int64
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Channel, &m.TaskID, &m.Text, &created); err != nil {
			return nil, err
		}
		m.CreatedAt = time.Unix(0, created).UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}
