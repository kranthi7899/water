-- docs/slices/UI.md Phase 5b (People workspace): the "Message a team" and
-- "Send pulse check" buttons create drafts (store.CreateDraft, migration
-- 0021's own table) with two new Draft.Template values, team_message and
-- pulse_check, alongside the three migration 0021 already allows
-- (reply|delegation|investor_update). Both are validated in Go
-- (internal/store/drafts.go's draftTemplates map, extended in the same
-- commit as this migration) and in this table's own CHECK constraint,
-- exactly the belt-and-suspenders convention 0021's own comment describes.
--
-- SQLite has no ALTER TABLE ... DROP/ADD CONSTRAINT, so widening a CHECK
-- means the standard SQLite recreate: build the new table under a
-- temporary name, copy every existing row across unchanged (column order
-- and values both preserved byte-for-byte), drop the old table, and rename
-- the new one into its place. This is the first migration in this schema
-- that needs that pattern; every other migration so far has only ever
-- added a column or a whole new table.
CREATE TABLE drafts_new (
	id TEXT PRIMARY KEY,
	template TEXT NOT NULL CHECK (template IN ('reply', 'delegation', 'investor_update', 'team_message', 'pulse_check')),
	to_address TEXT NOT NULL DEFAULT '',
	subject TEXT NOT NULL DEFAULT '',
	body TEXT NOT NULL DEFAULT '',
	source_card_id TEXT NOT NULL DEFAULT '',
	provenance TEXT NOT NULL DEFAULT '',
	simulated INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL
);

INSERT INTO drafts_new (id, template, to_address, subject, body, source_card_id, provenance, simulated, updated_at)
	SELECT id, template, to_address, subject, body, source_card_id, provenance, simulated, updated_at FROM drafts;

DROP TABLE drafts;

ALTER TABLE drafts_new RENAME TO drafts;
