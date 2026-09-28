-- Long-term memory (Slice F, internal/memory): distilled facts with
-- provenance, never the source content itself. One row per record.
--
-- Append-only. A record is immutable once written. The only mutation that
-- exists is setting the invalidation columns (invalidated_at and friends)
-- exactly once; a correction writes a new row whose supersedes names the
-- old one and invalidates the old one in the same transaction
-- (memory_records.go's MemoryTx). The two triggers below enforce this in
-- the database too, so no code path (and no stray UPDATE) can overwrite a
-- statement, clear an invalidation or delete a row.
--
-- twin scopes every row (ceo, ceo-demo, counterparty): memory.Bind's handle
-- only ever queries its own twin.
--
-- Times are Unix nanoseconds so a record round-trips field-for-field.
-- subjects is a JSON array of typed refs ("person:<id>").
-- supersedes / superseded_by are NULL when there is no link.
--
-- Retention (owner decision, 2026-09-25, provisional): invalidated rows are
-- kept forever. There is no compaction or archive.

CREATE TABLE memory_records (
	id TEXT PRIMARY KEY,
	twin TEXT NOT NULL,
	type TEXT NOT NULL,
	statement TEXT NOT NULL,
	sensitivity TEXT NOT NULL,
	subjects TEXT NOT NULL DEFAULT '[]',
	-- prov_ prefix: "trigger" is an SQL keyword.
	prov_trigger TEXT NOT NULL,
	source_ref TEXT NOT NULL,
	audit_seq INTEGER NOT NULL,
	written_by TEXT NOT NULL,
	approved_by TEXT NOT NULL DEFAULT '',
	observed_at INTEGER NOT NULL,
	valid_from INTEGER NOT NULL,
	valid_until INTEGER,
	review_after INTEGER,
	confidence REAL NOT NULL,
	supersedes TEXT,
	invalidated_at INTEGER,
	invalidated_by TEXT,
	invalidation_reason TEXT,
	invalidation_audit_seq INTEGER,
	superseded_by TEXT
);

CREATE INDEX memory_records_twin_type ON memory_records (twin, type);
CREATE INDEX memory_records_valid_until ON memory_records (valid_until);
CREATE INDEX memory_records_review_after ON memory_records (review_after);
CREATE INDEX memory_records_supersedes ON memory_records (supersedes);

-- No row is ever deleted.
CREATE TRIGGER memory_records_no_delete BEFORE DELETE ON memory_records
BEGIN
	SELECT RAISE(ABORT, 'memory_records is append-only: rows are never deleted');
END;

-- No row is ever replaced. INSERT OR REPLACE (and REPLACE INTO) deletes the
-- conflicting row without firing the DELETE trigger above, because
-- recursive_triggers is off, so an insert over an existing id is refused
-- here, before conflict resolution runs.
CREATE TRIGGER memory_records_no_replace BEFORE INSERT ON memory_records
WHEN EXISTS (SELECT 1 FROM memory_records WHERE id = NEW.id)
BEGIN
	SELECT RAISE(ABORT, 'memory_records is append-only: an existing row is never replaced');
END;

-- The only permitted UPDATE sets the invalidation columns on a live row,
-- leaving every content column unchanged.
CREATE TRIGGER memory_records_append_only BEFORE UPDATE ON memory_records
WHEN OLD.invalidated_at IS NOT NULL
	OR NEW.invalidated_at IS NULL
	OR NEW.id IS NOT OLD.id
	OR NEW.twin IS NOT OLD.twin
	OR NEW.type IS NOT OLD.type
	OR NEW.statement IS NOT OLD.statement
	OR NEW.sensitivity IS NOT OLD.sensitivity
	OR NEW.subjects IS NOT OLD.subjects
	OR NEW.prov_trigger IS NOT OLD.prov_trigger
	OR NEW.source_ref IS NOT OLD.source_ref
	OR NEW.audit_seq IS NOT OLD.audit_seq
	OR NEW.written_by IS NOT OLD.written_by
	OR NEW.approved_by IS NOT OLD.approved_by
	OR NEW.observed_at IS NOT OLD.observed_at
	OR NEW.valid_from IS NOT OLD.valid_from
	OR NEW.valid_until IS NOT OLD.valid_until
	OR NEW.review_after IS NOT OLD.review_after
	OR NEW.confidence IS NOT OLD.confidence
	OR NEW.supersedes IS NOT OLD.supersedes
BEGIN
	SELECT RAISE(ABORT, 'memory_records is append-only: only a live row may be invalidated, once');
END;
