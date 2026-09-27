-- docs/slices/UI.md Phase 1c ("Decision records and card fields", U13). The
-- plan pre-assigned "migration 0018" for this; Phase 1a took 0017 in full
-- and Phase 1b took 0018 (see those migrations' own notes), so this lands
-- as 0019, the next free number when this phase started.
--
-- decision_records   U13's persisted-card table. A row is a full serialized
--                     decisions.Card (card_json), decoded and re-encoded
--                     only on the decisions side of the layering (this
--                     package cannot import internal/decisions without a
--                     cycle, the same reason internal/decisions imports
--                     internal/store and not the other way around). Once a
--                     card id has a row here, decisions.Merge treats it as
--                     authoritative over whatever the live classifier/
--                     trigger would compute for that same id -- the whole
--                     point of U13 (a card can be pinned, hand-edited or
--                     demo-seeded, not only freshly computed). provenance
--                     is '' or 'demo_seed' (U21's convention, matching
--                     approvals.provenance); simulated is U8's boolean.
--
-- card_evidence_extra Evidence attached to an existing card after the fact
--                     (e.g. a future research report's "Attach" action --
--                     this migration builds the storage and merge-in logic
--                     only, not that UI). Rows are appended, never edited,
--                     and apply to whichever version of the card wins the
--                     merge (computed or record-sourced) for their card_id.
--                     untrusted is set per row, the same boolean-as-INTEGER
--                     convention as every other taint/external flag in this
--                     schema (messages.external, workspaces.external,
--                     research_runs.untrusted-to-come); decisions.Merge
--                     propagates it into the merged card's own Untrusted.
--
-- card_action_states  Replaces card_states' role for staging specifically
--                     (card_states itself is unchanged and kept for
--                     dismiss). Keyed by (card_id, action_id) rather than
--                     card_id alone, so two different actions proposed by
--                     the same card (e.g. an extension email and a
--                     priority bump) can each be staged into their own
--                     envelope independently -- the one-envelope-per-card
--                     design in card_states could not tell them apart.
CREATE TABLE decision_records (
	card_id TEXT PRIMARY KEY,
	card_json TEXT NOT NULL,
	provenance TEXT NOT NULL DEFAULT '',
	simulated INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);

CREATE TABLE card_evidence_extra (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	card_id TEXT NOT NULL,
	source TEXT NOT NULL,
	text TEXT NOT NULL,
	untrusted INTEGER NOT NULL DEFAULT 0,
	added_at INTEGER NOT NULL
);
CREATE INDEX card_evidence_extra_card_id ON card_evidence_extra (card_id);

CREATE TABLE card_action_states (
	card_id TEXT NOT NULL,
	action_id TEXT NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('staged')),
	approval_id TEXT NOT NULL DEFAULT '',
	staged_at INTEGER NOT NULL,
	PRIMARY KEY (card_id, action_id)
);
