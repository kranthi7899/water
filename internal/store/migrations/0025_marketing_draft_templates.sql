-- docs/slices/UI.md Phase 5d (Marketing): the Marketing workspace's "Draft
-- outreach" (to a prospect, from the existing company_customers.accounts
-- read) and "Draft reply" (to a public review -- see
-- internal/gateway/workspace_detail.go's own doc comment on why there is no
-- real public-reviews data source yet, and how that's handled honestly)
-- buttons create drafts (store.CreateDraft -> POST
-- /v1/workspaces/{id}/drafts, the same route Phase 5b/5c's own draft
-- buttons already use) with two new Draft.Template values,
-- prospect_outreach and review_reply, alongside the six migration 0024
-- already allows (reply|delegation|investor_update|team_message|
-- pulse_check|idea_proposal). Validated in Go
-- (internal/store/drafts.go's draftTemplates map, extended in the same
-- commit as this migration) and in this table's own CHECK constraint,
-- exactly the belt-and-suspenders convention 0021/0023/0024's own comments
-- describe.
--
-- SQLite has no ALTER TABLE ... DROP/ADD CONSTRAINT, so widening a CHECK
-- means the same recreate 0023/0024 already used: build the new table
-- under a temporary name, copy every existing row across unchanged, drop
-- the old table, and rename the new one into its place.
CREATE TABLE drafts_new (
	id TEXT PRIMARY KEY,
	template TEXT NOT NULL CHECK (template IN ('reply', 'delegation', 'investor_update', 'team_message', 'pulse_check', 'idea_proposal', 'prospect_outreach', 'review_reply')),
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
