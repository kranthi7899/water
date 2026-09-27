-- docs/slices/UI.md Phase 5c (Ideas and Research): the Ideas workspace's
-- "Propose" button creates a draft (POST /v1/ideas/{id}/propose ->
-- store.CreateDraft) with a code-built body from the idea's own title/gist,
-- never a model call -- the same posture every other draft in this schema
-- already has. That needs one new Draft.Template value, idea_proposal,
-- alongside the five migration 0023 already allows
-- (reply|delegation|investor_update|team_message|pulse_check). Validated in
-- Go (internal/store/drafts.go's draftTemplates map, extended in the same
-- commit as this migration) and in this table's own CHECK constraint,
-- exactly the belt-and-suspenders convention 0021/0023's own comments
-- describe.
--
-- SQLite has no ALTER TABLE ... DROP/ADD CONSTRAINT, so widening a CHECK
-- means the same recreate 0023 already used: build the new table under a
-- temporary name, copy every existing row across unchanged, drop the old
-- table, and rename the new one into its place.
CREATE TABLE drafts_new (
	id TEXT PRIMARY KEY,
	template TEXT NOT NULL CHECK (template IN ('reply', 'delegation', 'investor_update', 'team_message', 'pulse_check', 'idea_proposal')),
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
