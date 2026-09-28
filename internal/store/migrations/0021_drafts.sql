-- docs/slices/UI.md Phase 3c ("Drafts", U10-A). The plan originally called
-- this "migration 0020"; Phase 1a-1d and later phases (see those migrations'
-- own notes) had already consumed 0017-0020 by the time this phase started,
-- so this lands as 0021, the next free number when Phase 3c started. Phase
-- 3d's own originally-planned "migration 0021" shifts to 0022.
--
-- U10 reopened V's own D2-A ("a pending outward-message envelope IS the
-- draft") and replaced it with option A: a real, separately-persisted
-- drafts table. Save writes here; "Send for approval" reads the caller's
-- current values (never a row from this table -- see drafts.go's own
-- comment on submitDraftRequest) and proposes them through
-- approvals.Queue.Propose, the same chokepoint every other outward action
-- goes through. A row here is never itself an approval, never itself sent,
-- and carries no gate/manifest authority of its own.
--
-- Columns, in the plan's own order (id, template, to, subject, body,
-- source_card_id, provenance, simulated, updated_at), with one forced
-- rename: `to` is a reserved word in SQLite's grammar (confirmed against
-- modernc.org/sqlite: `CREATE TABLE t (to TEXT)` fails to parse), so the
-- column is `to_address`. The Go struct field stays `To` and the JSON key
-- stays "to" (drafts.go), so nothing outside this one migration file and
-- its own accessor's SQL text ever has to know about the rename.
--
-- template is a closed three-value enum (reply|delegation|investor_update,
-- the plan's own "tagged Reply, Delegation or Investor-update section"),
-- validated in drafts.go's own draftTemplates map, exactly like
-- ideas.stage/store.ideaStages (migration 0020) validates its own enum both
-- in a CHECK and in Go. There is no "sent"/"status" column at all: the plan's
-- own column list for this table has none, and Phase 3c's endpoints don't
-- need one -- a draft that has been sent for approval is unchanged here; the
-- approvals table (via the envelope Propose returns) is the durable record
-- of what happened next, exactly as it is for every other staged action in
-- this schema (card_action_states, decision_records, ...). A CEO can submit
-- the same draft more than once, each time proposing a brand-new envelope;
-- nothing here deduplicates that, unlike card_action_states' per-action
-- staging, because a draft is a reusable piece of text, not a one-shot
-- staged action tied to a single decision card.
--
-- provenance/simulated follow the same convention every other Phase 1/3
-- table in this schema already uses (e.g. ideas, decision_records).
CREATE TABLE drafts (
	id TEXT PRIMARY KEY,
	template TEXT NOT NULL CHECK (template IN ('reply', 'delegation', 'investor_update')),
	to_address TEXT NOT NULL DEFAULT '',
	subject TEXT NOT NULL DEFAULT '',
	body TEXT NOT NULL DEFAULT '',
	source_card_id TEXT NOT NULL DEFAULT '',
	provenance TEXT NOT NULL DEFAULT '',
	simulated INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL
);
