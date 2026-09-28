-- docs/slices/UI.md Phase 1b ("Approvals metadata and trail"). The plan
-- originally called this "migration 0017, second half", meant to share one
-- file with Phase 1a's workspace columns; Phase 1a landed using 0017 in
-- full for the workspace-only half (see 0017_workspace_specs.sql's own
-- note), so this metadata half gets the next free number instead.
--
-- origin_kind   agent_draft|person_request (U21): distinct from the
--               existing `origin` column, which is the gate's P0/P1/P2
--               priority origin, not this. Defaults to agent_draft so
--               every pre-existing row (proposed only by the model, never
--               by a person) reads back correctly with no change.
-- requested_by  a roster person id, for a person_request envelope (U15).
-- kind          email|money|signature|flag|message, for icon choice
--               (approvals.deriveKind, consulted at Propose).
-- source_card_id  which decision card, if any, produced this approval.
-- priority        the U14 priority rule's own words ("urgent"|"high"|
--                 "normal"), computed and stored at propose/request time.
-- deadline        a nullable timestamp, same convention as decided_at/
--                 executed_at above: no NOT NULL, no DEFAULT, INTEGER
--                 (unix nanoseconds), NULL meaning "no deadline".
-- thread_ref      the Gmail threadId a send executed into (set once by
--                 decideAndExecute via store.MarkApprovalSent).
-- sent_at         when the approval's action executed (the derived trail's
--                 "sent" stage: approvals.Envelope.Trail reads Status plus
--                 this and replied_at, rather than a stored trail enum).
--                 Nullable, same convention as deadline.
-- replied_at      when an inbound message on thread_ref arrived
--                 (store.MarkApprovalReplied). Nullable, same convention.
-- reply_ref       that inbound message's own id.
-- provenance      '' or 'demo_seed' (U21): the demo-seed marker, distinct
--                 from origin and origin_kind.
--
-- None of these are covered by approvals.PayloadHash (queue.go): they are
-- bookkeeping about an envelope, never part of what the CEO is approving.
ALTER TABLE approvals ADD COLUMN origin_kind TEXT NOT NULL DEFAULT 'agent_draft';
ALTER TABLE approvals ADD COLUMN requested_by TEXT NOT NULL DEFAULT '';
ALTER TABLE approvals ADD COLUMN kind TEXT NOT NULL DEFAULT '';
ALTER TABLE approvals ADD COLUMN source_card_id TEXT NOT NULL DEFAULT '';
ALTER TABLE approvals ADD COLUMN priority TEXT NOT NULL DEFAULT '';
ALTER TABLE approvals ADD COLUMN deadline INTEGER;
ALTER TABLE approvals ADD COLUMN thread_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE approvals ADD COLUMN sent_at INTEGER;
ALTER TABLE approvals ADD COLUMN replied_at INTEGER;
ALTER TABLE approvals ADD COLUMN reply_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE approvals ADD COLUMN provenance TEXT NOT NULL DEFAULT '';
