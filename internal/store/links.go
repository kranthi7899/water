package store

import (
	"context"
	"time"
)

// Link kinds the roster (internal/roster) writes. A relationship's meaning
// comes entirely from Kind plus which record types From/To name — there is
// no separate table per relationship, so a new kind never needs a schema
// change.
const (
	LinkMemberOf   = "member_of"    // person/project -> team
	LinkLeads      = "leads"        // person -> team/project
	LinkAllocated  = "allocated_to" // person -> project, Fraction set
	LinkOwnsClient = "owns_client"  // person -> client
)

// Link kinds the workspace/UI slice (Slice V) writes, on the same links
// table — no schema change, just new Kind values and from_type/to_type
// strings.
const (
	LinkAbout       = "about"        // thread -> decision/approval/message/meeting: the thread's anchor
	LinkInWorkspace = "in_workspace" // decision/meeting/job/thread/project -> workspace
	LinkInvolves    = "involves"     // record -> person
	LinkForProject  = "for_project"  // record -> project
)

// Node-type strings used as FromType/ToType across every table this
// package touches so far. Not a Go type — nothing in SQL constrains
// from_type/to_type, and Link's fields are plain strings like the rest of
// this file — just a single place to check before spelling a new one, so
// two callers never invent "workspace" and "work_space" for the same
// thing:
//
//	"decision", "approval", "message", "meeting", "thread", "workspace",
//	"job", "person", "team", "project", "client"

// Link is one edge between two roster records. FromType/ToType name which
// table FromID/ToID's source_id refers to ("person", "team", "project",
// "client"). Fraction is only meaningful for LinkAllocated; zero otherwise.
type Link struct {
	Kind     string
	FromType string
	FromID   string
	ToType   string
	ToID     string
	Fraction float64
}

// AddLink inserts l, or replaces its Fraction if the same (kind, from, to)
// edge already exists — so reloading the roster is idempotent, exactly
// like Upsert for a normalized record.
func (s *Store) AddLink(ctx context.Context, l Link) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO links (kind, from_type, from_id, to_type, to_id, fraction, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (kind, from_type, from_id, to_type, to_id)
		DO UPDATE SET fraction = excluded.fraction`,
		l.Kind, l.FromType, l.FromID, l.ToType, l.ToID, nullableFraction(l), time.Now().UTC().UnixNano())
	return err
}

// RemoveLink deletes the exact (kind, from_type, from_id, to_type, to_id)
// edge l names. No error if it doesn't exist — idempotent, matching
// AddLink's own idempotent-upsert posture.
func (s *Store) RemoveLink(ctx context.Context, l Link) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM links WHERE kind = ? AND from_type = ? AND from_id = ? AND to_type = ? AND to_id = ?`,
		l.Kind, l.FromType, l.FromID, l.ToType, l.ToID)
	return err
}

func nullableFraction(l Link) any {
	if l.Kind != LinkAllocated {
		return nil
	}
	return l.Fraction
}

// LinksFrom returns every edge of kind starting at (fromType, fromID).
func (s *Store) LinksFrom(ctx context.Context, fromType, fromID, kind string) ([]Link, error) {
	return queryLinks(ctx, s, `from_type = ? AND from_id = ? AND kind = ?`, fromType, fromID, kind)
}

// LinksTo returns every edge of kind ending at (toType, toID).
func (s *Store) LinksTo(ctx context.Context, toType, toID, kind string) ([]Link, error) {
	return queryLinks(ctx, s, `to_type = ? AND to_id = ? AND kind = ?`, toType, toID, kind)
}

// LinksOf returns every link of any kind that touches (typ, id), on either
// side of the edge (as From or as To). Each row is matched by a single OR
// condition rather than two queries unioned together, so a self-referential
// edge (typ/id on both sides) is still returned exactly once.
func (s *Store) LinksOf(ctx context.Context, typ, id string) ([]Link, error) {
	return queryLinks(ctx, s, `(from_type = ? AND from_id = ?) OR (to_type = ? AND to_id = ?)`, typ, id, typ, id)
}

func queryLinks(ctx context.Context, s *Store, cond string, args ...any) ([]Link, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT kind, from_type, from_id, to_type, to_id, COALESCE(fraction, 0) FROM links WHERE `+cond, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.Kind, &l.FromType, &l.FromID, &l.ToType, &l.ToID, &l.Fraction); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
