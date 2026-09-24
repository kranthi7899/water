-- sync_cursors holds one incremental-sync cursor per connector (e.g. Gmail's
-- history_id, gcal's syncToken), so a restart resumes instead of re-scanning.
CREATE TABLE sync_cursors (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);

-- briefs caches the computed morning brief text per local calendar day
-- ("YYYY-MM-DD"), so the one-model-call exception in the fast path only
-- fires once per day.
CREATE TABLE briefs (
	day TEXT PRIMARY KEY,
	text TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
