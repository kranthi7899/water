package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ApprovalRow is the persisted form of an approval envelope. Rows are
// immutable apart from status, reason and the decision timestamps; an edit is
// a new row. Validation lives in package approvals.
//
// OriginKind through Provenance are Phase 1b's approval metadata and trail
// columns (docs/slices/UI.md, migration 0018_approval_trail.sql): none of
// them are covered by approvals.PayloadHash, and Deadline/SentAt/RepliedAt
// follow DecidedAt/ExecutedAt's own nullable-timestamp convention (a zero
// time.Time means the column is NULL).
type ApprovalRow struct {
	ID           string
	Action       string
	Recipient    string
	Payload      string // canonical JSON
	PayloadHash  string
	EvidenceRefs []string
	Risk         string
	Origin       string
	Status       string
	Reason       string
	CreatedAt    time.Time
	ExpiresAt    time.Time
	DecidedAt    time.Time
	ExecutedAt   time.Time

	OriginKind   string
	RequestedBy  string
	Kind         string
	SourceCardID string
	Priority     string
	Deadline     time.Time
	ThreadRef    string
	SentAt       time.Time
	RepliedAt    time.Time
	ReplyRef     string
	Provenance   string
}

func (s *Store) InsertApproval(ctx context.Context, r ApprovalRow) error {
	refs, err := json.Marshal(nonNil(r.EvidenceRefs))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO approvals
		(id, action, recipient, payload, payload_hash, evidence_refs, risk, origin, status, reason, created_at, expires_at,
		 origin_kind, requested_by, kind, source_card_id, priority, deadline, provenance)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Action, r.Recipient, r.Payload, r.PayloadHash, string(refs), r.Risk, r.Origin, r.Status, r.Reason,
		r.CreatedAt.UnixNano(), r.ExpiresAt.UnixNano(),
		r.OriginKind, r.RequestedBy, r.Kind, r.SourceCardID, r.Priority, nullableTime(r.Deadline), r.Provenance)
	return err
}

// nullableTime is the write side of the deadline/sent_at/replied_at
// convention: a zero time.Time is stored as SQL NULL, not as a Unix-epoch
// timestamp, matching how they (and the pre-existing decided_at/executed_at)
// are read back in queryApprovals.
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixNano()
}

const approvalCols = `id, action, recipient, payload, payload_hash, evidence_refs, risk, origin, status, reason, created_at, expires_at, decided_at, executed_at,
	origin_kind, requested_by, kind, source_card_id, priority, deadline, thread_ref, sent_at, replied_at, reply_ref, provenance`

func (s *Store) GetApproval(ctx context.Context, id string) (ApprovalRow, error) {
	rows, err := s.queryApprovals(ctx, `SELECT `+approvalCols+` FROM approvals WHERE id = ?`, id)
	if err != nil {
		return ApprovalRow{}, err
	}
	if len(rows) == 0 {
		return ApprovalRow{}, ErrNotFound
	}
	return rows[0], nil
}

// ListApprovals returns rows with the given status, oldest first.
func (s *Store) ListApprovals(ctx context.Context, status string) ([]ApprovalRow, error) {
	return s.queryApprovals(ctx, `SELECT `+approvalCols+` FROM approvals WHERE status = ? ORDER BY created_at, rowid`, status)
}

// TransitionApproval moves id from one status to another only if it is
// still in `from`. The compare-and-set is what makes approvals single-use:
// two executions racing on one envelope cannot both see "approved".
func (s *Store) TransitionApproval(ctx context.Context, id, from, to, reason string, at time.Time) (bool, error) {
	col := "decided_at"
	if to == "executed" {
		col = "executed_at"
	}
	res, err := s.db.ExecContext(ctx, `UPDATE approvals SET status = ?, reason = ?, `+col+` = ? WHERE id = ? AND status = ?`,
		to, reason, at.UnixNano(), id, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) queryApprovals(ctx context.Context, q string, args ...any) ([]ApprovalRow, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ApprovalRow
	for rows.Next() {
		var r ApprovalRow
		var refs string
		var created, expires int64
		var decided, executed, deadline, sentAt, repliedAt sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Action, &r.Recipient, &r.Payload, &r.PayloadHash, &refs, &r.Risk, &r.Origin,
			&r.Status, &r.Reason, &created, &expires, &decided, &executed,
			&r.OriginKind, &r.RequestedBy, &r.Kind, &r.SourceCardID, &r.Priority, &deadline,
			&r.ThreadRef, &sentAt, &repliedAt, &r.ReplyRef, &r.Provenance); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(refs), &r.EvidenceRefs); err != nil {
			return nil, errors.New("approvals: corrupt evidence_refs")
		}
		r.CreatedAt, r.ExpiresAt = time.Unix(0, created).UTC(), time.Unix(0, expires).UTC()
		if decided.Valid {
			r.DecidedAt = time.Unix(0, decided.Int64).UTC()
		}
		if executed.Valid {
			r.ExecutedAt = time.Unix(0, executed.Int64).UTC()
		}
		if deadline.Valid {
			r.Deadline = time.Unix(0, deadline.Int64).UTC()
		}
		if sentAt.Valid {
			r.SentAt = time.Unix(0, sentAt.Int64).UTC()
		}
		if repliedAt.Valid {
			r.RepliedAt = time.Unix(0, repliedAt.Int64).UTC()
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkApprovalSent records that id's approved action executed successfully:
// sent_at (the derived trail's "sent" stage, see approvals.Envelope.Trail)
// and, for a Gmail send, threadRef, so a later inbound reply on that thread
// can be matched back to this approval by MarkApprovalReplied. threadRef is
// "" for every other function; the column's own default already reads back
// as "" so passing "" here is a no-op on it, not a special case.
func (s *Store) MarkApprovalSent(ctx context.Context, id, threadRef string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE approvals SET sent_at = ?, thread_ref = ? WHERE id = ?`,
		at.UnixNano(), threadRef, id)
	return err
}

// MarkApprovalReplied records that an inbound message (msgRef) arrived on
// Gmail thread threadRef, for every approval whose thread_ref matches (set
// once, by MarkApprovalSent, when a gmail.send_message executes). It is a
// no-op, not an error, when no approval's thread_ref matches -- most inbound
// mail is not a reply to anything Water sent, and threadRef == "" always
// matches nothing (the column's own unset default), so that case returns
// immediately without touching the table.
func (s *Store) MarkApprovalReplied(ctx context.Context, threadRef, msgRef string, at time.Time) error {
	if threadRef == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE approvals SET replied_at = ?, reply_ref = ? WHERE thread_ref = ?`,
		at.UnixNano(), msgRef, threadRef)
	return err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
