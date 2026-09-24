package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Twin-message directions (Slice E).
const (
	TwinInbound  = "in"
	TwinOutbound = "out"
)

var (
	// ErrTwinMessageConflict is InsertTwinMessage's answer when a message
	// with the same direction and id already exists with different content.
	ErrTwinMessageConflict = errors.New("a different twin message with this id already exists")
	// ErrTwinResponseExists is InsertTwinMessage's answer for a second
	// response to the same request (one request, one response).
	ErrTwinResponseExists = errors.New("this request already has a response")
)

// TwinMessageRow is one twin_messages row. External is not a field: it is
// derived from Direction (inbound is always external) and pinned by the
// table's CHECK constraint.
type TwinMessageRow struct {
	Direction          string
	ID                 string
	FromTwin           string
	ToTwin             string
	Type               string
	InReplyTo          string
	Subject            string
	Payload            string
	EvidenceRefs       []string
	NeedsHumanApproval bool
	ReplyBy            time.Time // zero when unset
	SentAt             time.Time
	RecordedAt         time.Time
	ContentHash        string
}

// External reports whether the row is untrusted external content: every
// inbound message is, whoever sent it.
func (r TwinMessageRow) External() bool { return r.Direction == TwinInbound }

// InsertTwinMessage records r. inserted is false, with no error, when an
// identical message (same direction, id and content hash) is already
// stored — a re-delivery. A different message reusing the id is
// ErrTwinMessageConflict; a second response to one request is
// ErrTwinResponseExists.
func (s *Store) InsertTwinMessage(ctx context.Context, r TwinMessageRow) (inserted bool, err error) {
	refs, err := json.Marshal(nonNil(r.EvidenceRefs))
	if err != nil {
		return false, err
	}
	var replyTo, replyBy any
	if r.InReplyTo != "" {
		replyTo = r.InReplyTo
	}
	if !r.ReplyBy.IsZero() {
		replyBy = r.ReplyBy.UnixNano()
	}
	ext := 0
	if r.External() {
		ext = 1
	}
	need := 0
	if r.NeedsHumanApproval {
		need = 1
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO twin_messages
		(direction, id, from_twin, to_twin, type, in_reply_to, subject, payload, evidence_refs,
		 needs_human_approval, reply_by, sent_at, recorded_at, content_hash, external)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (direction, id) DO NOTHING`,
		r.Direction, r.ID, r.FromTwin, r.ToTwin, r.Type, replyTo, r.Subject, r.Payload, string(refs),
		need, replyBy, r.SentAt.UnixNano(), r.RecordedAt.UnixNano(), r.ContentHash, ext)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return false, ErrTwinResponseExists
		}
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return err == nil, err
	}
	prev, err := s.GetTwinMessage(ctx, r.Direction, r.ID)
	if err != nil {
		return false, err
	}
	if prev.ContentHash != r.ContentHash || prev.FromTwin != r.FromTwin || prev.ToTwin != r.ToTwin {
		return false, ErrTwinMessageConflict
	}
	return false, nil
}

const twinMessageCols = `direction, id, from_twin, to_twin, type, in_reply_to, subject, payload, evidence_refs,
	needs_human_approval, reply_by, sent_at, recorded_at, content_hash`

func scanTwinMessage(sc interface{ Scan(...any) error }) (TwinMessageRow, error) {
	var r TwinMessageRow
	var replyTo sql.NullString
	var replyBy sql.NullInt64
	var refs string
	var need int
	var sent, recorded int64
	if err := sc.Scan(&r.Direction, &r.ID, &r.FromTwin, &r.ToTwin, &r.Type, &replyTo, &r.Subject, &r.Payload, &refs,
		&need, &replyBy, &sent, &recorded, &r.ContentHash); err != nil {
		return TwinMessageRow{}, err
	}
	r.InReplyTo = replyTo.String
	if replyBy.Valid {
		r.ReplyBy = time.Unix(0, replyBy.Int64).UTC()
	}
	r.NeedsHumanApproval = need == 1
	r.SentAt = time.Unix(0, sent).UTC()
	r.RecordedAt = time.Unix(0, recorded).UTC()
	if err := json.Unmarshal([]byte(refs), &r.EvidenceRefs); err != nil {
		return TwinMessageRow{}, err
	}
	return r, nil
}

// GetTwinMessage returns ErrNotFound for an unknown (direction, id).
func (s *Store) GetTwinMessage(ctx context.Context, direction, id string) (TwinMessageRow, error) {
	r, err := scanTwinMessage(s.db.QueryRowContext(ctx, `SELECT `+twinMessageCols+` FROM twin_messages WHERE direction = ? AND id = ?`, direction, id))
	if err == sql.ErrNoRows {
		return TwinMessageRow{}, ErrNotFound
	}
	return r, err
}

// TwinResponseTo returns the response recorded in direction to the request
// id, or ErrNotFound.
func (s *Store) TwinResponseTo(ctx context.Context, direction, requestID string) (TwinMessageRow, error) {
	r, err := scanTwinMessage(s.db.QueryRowContext(ctx, `SELECT `+twinMessageCols+` FROM twin_messages
		WHERE direction = ? AND in_reply_to = ? AND type = 'response'`, direction, requestID))
	if err == sql.ErrNoRows {
		return TwinMessageRow{}, ErrNotFound
	}
	return r, err
}

// ListTwinMessages returns up to limit messages in direction ("" for both),
// newest first.
func (s *Store) ListTwinMessages(ctx context.Context, direction string, limit int) ([]TwinMessageRow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+twinMessageCols+` FROM twin_messages
		WHERE (? = '' OR direction = ?) ORDER BY recorded_at DESC, id LIMIT ?`, direction, direction, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TwinMessageRow
	for rows.Next() {
		r, err := scanTwinMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
