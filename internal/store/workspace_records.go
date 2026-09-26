package store

// Workspace is a UI-level container that can span projects — a grouping the
// embedded web UI's Today/Decisions views use (Slice V) — distinct from
// Project (roster_records.go), which is the people roster's own seed data.
// It follows records.go's normalized-record shape (Meta, db tags, Table())
// and gets Upsert/Get/List for free from its generic machinery.
type Workspace struct {
	Meta
	Name        string `db:"name"`
	Description string `db:"description"`
}

func (*Workspace) Table() string { return "workspaces" }
