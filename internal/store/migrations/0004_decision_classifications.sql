-- decision_classifications caches a decisions.Classifier verdict per item,
-- keyed by the item's own (source, source_id) identity, so a message the
-- candidate heuristic flags once is never re-classified on a later sync
-- tick, even across a daemon restart.
CREATE TABLE decision_classifications (
	source TEXT NOT NULL,
	source_id TEXT NOT NULL,
	needs_decision INTEGER NOT NULL,
	type_id TEXT NOT NULL,
	confidence REAL NOT NULL,
	classified_at INTEGER NOT NULL,
	PRIMARY KEY (source, source_id)
);
