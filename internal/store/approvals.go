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
}

func (s *Store) InsertApproval(ctx context.Context, r ApprovalRow) error {
	refs, err := json.Marshal(nonNil(r.EvidenceRefs))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO approvals
		(id, action, recipient, payload, payload_hash, evidence_refs, risk, origin, status, reason, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Action, r.Recipient, r.Payload, r.PayloadHash, string(refs), r.Risk, r.Origin, r.Status, r.Reason,
		r.CreatedAt.UnixNano(), r.ExpiresAt.UnixNano())
	return err
}

const approvalCols = `id, action, recipient, payload, payload_hash, evidence_refs, risk, origin, status, reason, created_at, expires_at, decided_at, executed_at`

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
		var decided, executed sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Action, &r.Recipient, &r.Payload, &r.PayloadHash, &refs, &r.Risk, &r.Origin,
			&r.Status, &r.Reason, &created, &expires, &decided, &executed); err != nil {
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
		out = append(out, r)
	}
	return out, rows.Err()
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
