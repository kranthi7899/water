package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

// ResearchRun is one research run against an idea (docs/slices/UI.md Phase
// 1d, U2-A / Phase 5c). Status is a closed enum (see researchRunStatuses).
//
// AttachedCardID defaults to "" (unset), the same sentinel convention
// ApprovalRow.SourceCardID/ThreadRef/ReplyRef already use in this schema,
// rather than a real SQL NULL. Only a future Phase 5c Attach action (not
// built in this phase) ever sets it, mirroring CardEvidenceExtra's own
// "storage only, no UI yet" scope from Phase 1c: Attach is meant to insert
// only into card_evidence_extra and set this field, never to touch a
// card's other fields.
//
// FinishedAt is a real nullable timestamp, following
// ApprovalRow.Deadline's own nullable-timestamp convention (a zero
// time.Time is stored as SQL NULL): a run starts with no finish time.
//
// Untrusted is always true. A research report's own text is always
// untrusted, external, model-facing content -- a fixed property of every
// row this table holds, not a per-row choice -- so CreateResearchRun does
// not take an Untrusted argument at all, and this field only exists on
// reads. Do not add a setter for it.
type ResearchRun struct {
	ID             string
	IdeaID         string
	Topic          string
	Status         string // queued|running|finished|failed (see researchRunStatuses)
	AttachedCardID string
	ReportText     string
	Untrusted      bool // always true; see the type comment
	Provenance     string
	CreatedAt      time.Time
	FinishedAt     time.Time // zero means not finished
}

var researchRunStatuses = map[string]bool{"queued": true, "running": true, "finished": true, "failed": true}

// ResearchStep is one ordered step within a ResearchRun (Phase 5c's "5
// fixed, code-built steps": overview, market, competitors, pricing, risks).
// N is 1-based (n=1..5 for today's fixed five-step runner), matching "step
// 3 of 5" language rather than a zero-based index.
type ResearchStep struct {
	RunID       string
	N           int
	Label       string
	Status      string // pending|running|done|failed (see researchStepStatuses)
	SourceCount int
}

var researchStepStatuses = map[string]bool{"pending": true, "running": true, "done": true, "failed": true}

func newRunID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "run_" + hex.EncodeToString(b[:])
}

const researchRunCols = `id, idea_id, topic, status, attached_card_id, report_text, untrusted, provenance, created_at, finished_at`

func scanResearchRun(sc interface{ Scan(...any) error }) (ResearchRun, error) {
	var r ResearchRun
	var untrusted int64
	var created int64
	var finished sql.NullInt64
	if err := sc.Scan(&r.ID, &r.IdeaID, &r.Topic, &r.Status, &r.AttachedCardID, &r.ReportText, &untrusted, &r.Provenance, &created, &finished); err != nil {
		return ResearchRun{}, err
	}
	r.Untrusted = untrusted != 0
	r.CreatedAt = time.Unix(0, created).UTC()
	if finished.Valid {
		r.FinishedAt = time.Unix(0, finished.Int64).UTC()
	}
	return r, nil
}

// CreateResearchRun inserts r, generating an id if empty and defaulting
// CreatedAt to now. It rejects an unknown Status. Untrusted is always
// written as true regardless of r.Untrusted: this table's CHECK constraint
// (untrusted = 1) would reject anything else, and the Go layer enforces the
// same rule so a caller sees a clear error rather than a database one.
func (s *Store) CreateResearchRun(ctx context.Context, r ResearchRun) (ResearchRun, error) {
	if !researchRunStatuses[r.Status] {
		return ResearchRun{}, fmt.Errorf("store: unknown research run status %q", r.Status)
	}
	if r.ID == "" {
		r.ID = newRunID()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	r.Untrusted = true
	_, err := s.db.ExecContext(ctx, `INSERT INTO research_runs (`+researchRunCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.IdeaID, r.Topic, r.Status, r.AttachedCardID, r.ReportText, boolToInt(r.Untrusted), r.Provenance,
		r.CreatedAt.UnixNano(), nullableTime(r.FinishedAt))
	if err != nil {
		return ResearchRun{}, err
	}
	return r, nil
}

// UpdateResearchRunStatus validates and updates id's status. When
// finishedAt is non-zero it is also written (the run has stopped, whether
// finished or failed); pass a zero time.Time for a running/queued
// transition.
func (s *Store) UpdateResearchRunStatus(ctx context.Context, id, status string, finishedAt time.Time) error {
	if !researchRunStatuses[status] {
		return fmt.Errorf("store: unknown research run status %q", status)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE research_runs SET status = ?, finished_at = ? WHERE id = ?`,
		status, nullableTime(finishedAt), id)
	return err
}

// SetResearchRunReport sets id's report_text (the runner's finished
// output). It does not itself change status; call UpdateResearchRunStatus
// too.
func (s *Store) SetResearchRunReport(ctx context.Context, id, reportText string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE research_runs SET report_text = ? WHERE id = ?`, reportText, id)
	return err
}

// AttachResearchRunCard sets id's attached_card_id (the future Phase 5c
// Attach action). It does not touch card_evidence_extra itself; a caller
// wiring up Attach does both.
func (s *Store) AttachResearchRunCard(ctx context.Context, id, cardID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE research_runs SET attached_card_id = ? WHERE id = ?`, cardID, id)
	return err
}

// GetResearchRun returns ErrNotFound when id has no row.
func (s *Store) GetResearchRun(ctx context.Context, id string) (ResearchRun, error) {
	r, err := scanResearchRun(s.db.QueryRowContext(ctx, `SELECT `+researchRunCols+` FROM research_runs WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return ResearchRun{}, ErrNotFound
	}
	return r, err
}

// ListResearchRuns returns every run, oldest first.
func (s *Store) ListResearchRuns(ctx context.Context) ([]ResearchRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+researchRunCols+` FROM research_runs ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResearchRun
	for rows.Next() {
		r, err := scanResearchRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListResearchRunsForIdea returns ideaID's runs, oldest first.
func (s *Store) ListResearchRunsForIdea(ctx context.Context, ideaID string) ([]ResearchRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+researchRunCols+` FROM research_runs WHERE idea_id = ? ORDER BY created_at ASC, id ASC`, ideaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResearchRun
	for rows.Next() {
		r, err := scanResearchRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpsertResearchStep validates Status and inserts or replaces step by
// (RunID, N).
func (s *Store) UpsertResearchStep(ctx context.Context, step ResearchStep) error {
	if !researchStepStatuses[step.Status] {
		return fmt.Errorf("store: unknown research step status %q", step.Status)
	}
	if step.N < 1 {
		return fmt.Errorf("store: research step n must be >= 1, got %d", step.N)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO research_steps (run_id, n, label, status, source_count)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (run_id, n) DO UPDATE SET label = excluded.label, status = excluded.status,
			source_count = excluded.source_count`,
		step.RunID, step.N, step.Label, step.Status, step.SourceCount)
	return err
}

// ListResearchSteps returns runID's steps in step order (n ascending).
func (s *Store) ListResearchSteps(ctx context.Context, runID string) ([]ResearchStep, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_id, n, label, status, source_count
		FROM research_steps WHERE run_id = ? ORDER BY n ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResearchStep
	for rows.Next() {
		var st ResearchStep
		if err := rows.Scan(&st.RunID, &st.N, &st.Label, &st.Status, &st.SourceCount); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}
