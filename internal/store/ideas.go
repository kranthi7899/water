package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

// Idea is one captured idea for the Research workspace (docs/slices/UI.md
// Phase 1d, U2-A). Deliberately not named "task" or "job": Slice J's own
// task_records/task_events naming (J Q5) stays open, and J may absorb this
// table later.
//
// Stage is a closed enum. Only "raw" and "explored" exist today (see
// ideaStages); a future third stage needs a new migration to widen both the
// CHECK on the ideas table and ideaStages, not a caller silently writing an
// unvalidated string.
type Idea struct {
	ID         string
	Title      string
	Gist       string
	Stage      string // "raw" or "explored" (see ideaStages)
	Provenance string // "" or "demo_seed" (U21)
	Simulated  bool
	CreatedAt  time.Time
}

var ideaStages = map[string]bool{"raw": true, "explored": true}

// IdeaEvidence is one evidence line attached to an idea: a source reference
// plus a short label, no full body text (unlike CardEvidenceExtra.Text),
// per the plan's own terseness for this table. IdeaID has a hard foreign
// key to ideas(id) (migration 0020): unlike a card, which may exist only
// computed and never get a decision_records row, every idea this table can
// name already has a real row, so AddIdeaEvidence for an unknown idea id
// fails with the database's own foreign-key-constraint error rather than
// silently orphaning the row.
type IdeaEvidence struct {
	ID      int64
	IdeaID  string
	Source  string
	Label   string
	AddedAt time.Time
}

func newIdeaID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "idea_" + hex.EncodeToString(b[:])
}

const ideaCols = `id, title, gist, stage, provenance, simulated, created_at`

func scanIdea(sc interface{ Scan(...any) error }) (Idea, error) {
	var idea Idea
	var simulated, created int64
	if err := sc.Scan(&idea.ID, &idea.Title, &idea.Gist, &idea.Stage, &idea.Provenance, &simulated, &created); err != nil {
		return Idea{}, err
	}
	idea.Simulated = simulated != 0
	idea.CreatedAt = time.Unix(0, created).UTC()
	return idea, nil
}

// CreateIdea inserts idea, generating an id if empty and defaulting
// CreatedAt to now. It rejects an unknown Stage rather than persisting it
// (docs/slices/UI.md Phase 1d).
func (s *Store) CreateIdea(ctx context.Context, idea Idea) (Idea, error) {
	if !ideaStages[idea.Stage] {
		return Idea{}, fmt.Errorf("store: unknown idea stage %q", idea.Stage)
	}
	if idea.ID == "" {
		idea.ID = newIdeaID()
	}
	if idea.CreatedAt.IsZero() {
		idea.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO ideas (`+ideaCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		idea.ID, idea.Title, idea.Gist, idea.Stage, idea.Provenance, boolToInt(idea.Simulated), idea.CreatedAt.UnixNano())
	if err != nil {
		return Idea{}, err
	}
	return idea, nil
}

// SetIdeaStage validates and updates id's stage (e.g. raw -> explored). It
// rejects an unknown stage the same way CreateIdea does.
func (s *Store) SetIdeaStage(ctx context.Context, id, stage string) error {
	if !ideaStages[stage] {
		return fmt.Errorf("store: unknown idea stage %q", stage)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE ideas SET stage = ? WHERE id = ?`, stage, id)
	return err
}

// GetIdea returns ErrNotFound when id has no row.
func (s *Store) GetIdea(ctx context.Context, id string) (Idea, error) {
	idea, err := scanIdea(s.db.QueryRowContext(ctx, `SELECT `+ideaCols+` FROM ideas WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Idea{}, ErrNotFound
	}
	return idea, err
}

// ListIdeas returns every idea, oldest first.
func (s *Store) ListIdeas(ctx context.Context) ([]Idea, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+ideaCols+` FROM ideas ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Idea
	for rows.Next() {
		idea, err := scanIdea(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, idea)
	}
	return out, rows.Err()
}

// AddIdeaEvidence appends e, generating its ID and defaulting AddedAt to
// now. It fails if e.IdeaID names no row in ideas (a foreign-key
// constraint error from SQLite, since this database runs with
// _pragma=foreign_keys(1)): evidence for a nonexistent idea is rejected,
// not silently orphaned.
func (s *Store) AddIdeaEvidence(ctx context.Context, e IdeaEvidence) (IdeaEvidence, error) {
	if e.AddedAt.IsZero() {
		e.AddedAt = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO idea_evidence (idea_id, source, label, added_at) VALUES (?, ?, ?, ?)`,
		e.IdeaID, e.Source, e.Label, e.AddedAt.UnixNano())
	if err != nil {
		return IdeaEvidence{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return IdeaEvidence{}, err
	}
	e.ID = id
	return e, nil
}

// ListIdeaEvidence returns ideaID's evidence, oldest first.
func (s *Store) ListIdeaEvidence(ctx context.Context, ideaID string) ([]IdeaEvidence, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, idea_id, source, label, added_at
		FROM idea_evidence WHERE idea_id = ? ORDER BY added_at ASC, id ASC`, ideaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IdeaEvidence
	for rows.Next() {
		var e IdeaEvidence
		var added int64
		if err := rows.Scan(&e.ID, &e.IdeaID, &e.Source, &e.Label, &added); err != nil {
			return nil, err
		}
		e.AddedAt = time.Unix(0, added).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
