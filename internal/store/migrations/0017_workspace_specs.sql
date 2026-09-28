-- docs/slices/UI.md Phase 1a ("workspaces, links, dashboard specs"): the
-- first half of the plan's migration 0017 — the approvals-metadata half
-- (origin_kind, kind, trail columns) is Phase 1b's job, a separate task,
-- and lands in its own later migration instead of sharing this number.
--
-- internal/workspaces loads twins/<id>/workspaces/*.yaml and upserts one
-- row per file into the existing `workspaces` table (0013_workspace_ui.sql
-- added the table itself for hand-created, "ui"-sourced groupings).
--
--   template       The closed set project|finance|clients|people|ideas|
--                   research|marketing (internal/workspaces.Spec.Template).
--   primary_source The spec's own `source` field (e.g. "linear_team:WAT",
--                   "company_finance") — the single upstream a workspace's
--                   control-room tiles read from (Phase 5).
--   spec_hash      sha256 of the raw YAML file's bytes at load time, so a
--                   later phase can tell a spec's content changed since it
--                   was last loaded without re-parsing and diffing every
--                   field.
--
-- All three default to '' so a pre-existing, hand-created workspace row
-- (Slice V; store.Workspace's Name/Description columns) still reads back
-- cleanly with no template/source/hash of its own.
ALTER TABLE workspaces ADD COLUMN template TEXT NOT NULL DEFAULT '';
ALTER TABLE workspaces ADD COLUMN primary_source TEXT NOT NULL DEFAULT '';
ALTER TABLE workspaces ADD COLUMN spec_hash TEXT NOT NULL DEFAULT '';
