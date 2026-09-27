package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

// Draft is one editable message the CEO is composing before it becomes an
// approval envelope (docs/slices/UI.md Phase 3c, U10-A: a real table, not an
// envelope-as-draft -- see migration 0021's own comment for why V's earlier
// D2-A model was reopened). Template is a closed enum (see draftTemplates);
// To is a single plain address, not yet run through Slice W's recipient
// checks -- those only ever apply once "Send for approval" actually proposes
// an envelope (drafts.go in internal/gateway), never to this row.
type Draft struct {
	ID           string
	Template     string // "reply" | "delegation" | "investor_update" | "team_message" | "pulse_check" (see draftTemplates)
	To           string
	Subject      string
	Body         string
	SourceCardID string // "" when the draft didn't originate from a decision card
	Provenance   string // "" or "demo_seed" (U21)
	Simulated    bool
	UpdatedAt    time.Time
}

// draftTemplates is Draft.Template's closed set, validated here in Go (not
// just the migration's own CHECK), the same belt-and-suspenders convention
// ideas.go's ideaStages uses for Idea.Stage. team_message and pulse_check
// (docs/slices/UI.md Phase 5b's People workspace buttons, migration 0023)
// are additive to the three Phase 3c originally shipped with; idea_proposal
// (docs/slices/UI.md Phase 5c's "Propose" button, migration 0024) is
// additive again.
var draftTemplates = map[string]bool{
	"reply": true, "delegation": true, "investor_update": true,
	"team_message": true, "pulse_check": true, "idea_proposal": true,
}

// DraftTemplateLabel is Template's display label (docs/slices/UI.md Phase
// 3c: "tagged Reply, Delegation or Investor-update section"). An unknown
// template (which CreateDraft/SaveDraft never persist, but a caller could
// still ask about) returns "" rather than guessing.
func DraftTemplateLabel(template string) string {
	switch template {
	case "reply":
		return "Reply"
	case "delegation":
		return "Delegation"
	case "investor_update":
		return "Investor update section"
	case "team_message":
		return "Team message"
	case "pulse_check":
		return "Pulse check"
	case "idea_proposal":
		return "Idea proposal"
	default:
		return ""
	}
}

func newDraftID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "draft_" + hex.EncodeToString(b[:])
}

const draftCols = `id, template, to_address, subject, body, source_card_id, provenance, simulated, updated_at`

func scanDraft(sc interface{ Scan(...any) error }) (Draft, error) {
	var d Draft
	var simulated, updated int64
	if err := sc.Scan(&d.ID, &d.Template, &d.To, &d.Subject, &d.Body, &d.SourceCardID, &d.Provenance, &simulated, &updated); err != nil {
		return Draft{}, err
	}
	d.Simulated = simulated != 0
	d.UpdatedAt = time.Unix(0, updated).UTC()
	return d, nil
}

// CreateDraft inserts d, generating an id if empty and defaulting UpdatedAt
// to now. It rejects an unknown Template rather than persisting it, exactly
// like CreateIdea rejects an unknown Stage.
func (s *Store) CreateDraft(ctx context.Context, d Draft) (Draft, error) {
	if !draftTemplates[d.Template] {
		return Draft{}, fmt.Errorf("store: unknown draft template %q", d.Template)
	}
	if d.ID == "" {
		d.ID = newDraftID()
	}
	if d.UpdatedAt.IsZero() {
		d.UpdatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO drafts (`+draftCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.Template, d.To, d.Subject, d.Body, d.SourceCardID, d.Provenance, boolToInt(d.Simulated), d.UpdatedAt.UnixNano())
	if err != nil {
		return Draft{}, err
	}
	return d, nil
}

// GetDraft returns ErrNotFound when id has no row.
func (s *Store) GetDraft(ctx context.Context, id string) (Draft, error) {
	d, err := scanDraft(s.db.QueryRowContext(ctx, `SELECT `+draftCols+` FROM drafts WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Draft{}, ErrNotFound
	}
	return d, err
}

// ListDrafts returns every draft, most-recently-updated first (matching how
// GET /v1/approvals' non-pending sets and ListDecisionRecords order their
// own history views -- newest first is the useful default for a working set
// of reusable text the CEO comes back to edit).
func (s *Store) ListDrafts(ctx context.Context) ([]Draft, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+draftCols+` FROM drafts ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Draft
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SaveDraft persists to/subject/body back onto id's row and bumps
// UpdatedAt to now, returning the updated row. Unlike CreateDraft, it
// applies no non-empty validation of its own: a draft is allowed to be
// incomplete (that's the point of a draft), so an empty subject or body
// saves cleanly. It returns ErrNotFound when id has no row -- nothing is
// silently inserted.
func (s *Store) SaveDraft(ctx context.Context, id, to, subject, body string) (Draft, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `UPDATE drafts SET to_address = ?, subject = ?, body = ?, updated_at = ? WHERE id = ?`,
		to, subject, body, now.UnixNano(), id)
	if err != nil {
		return Draft{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Draft{}, err
	}
	if n == 0 {
		return Draft{}, ErrNotFound
	}
	return s.GetDraft(ctx, id)
}
