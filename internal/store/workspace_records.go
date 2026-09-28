package store

// Workspace is a UI-level container that can span projects — a grouping the
// embedded web UI's Today/Decisions views use (Slice V) — distinct from
// Project (roster_records.go), which is the people roster's own seed data.
// It follows records.go's normalized-record shape (Meta, db tags, Table())
// and gets Upsert/Get/List for free from its generic machinery.
//
// Template, PrimarySource and SpecHash (migration 0017, Phase 1a) are
// filled in only for a workspace loaded from a twins/<id>/workspaces/*.yaml
// spec (internal/workspaces); a hand-created, "ui"-sourced workspace row
// from before that slice leaves them at their ” default.
type Workspace struct {
	Meta
	Name        string `db:"name"`
	Description string `db:"description"`
	// Template is the closed set project|finance|clients|people|ideas|
	// research|marketing.
	Template string `db:"template"`
	// PrimarySource is the spec's own `source` field, e.g. "linear_team:WAT"
	// or "company_finance".
	PrimarySource string `db:"primary_source"`
	// SpecHash is a sha256 of the spec file's raw bytes at load time, so a
	// later phase can tell its content changed since it was last loaded.
	SpecHash string `db:"spec_hash"`
}

func (*Workspace) Table() string { return "workspaces" }
