-- rate_hits backs the gate's sliding-window rate and usage caps so they
-- survive a daemon restart: one row per consumed hit, pruned periodically.
CREATE TABLE rate_hits (
	id INTEGER PRIMARY KEY,
	key TEXT NOT NULL,
	at INTEGER NOT NULL
);

CREATE INDEX rate_hits_key_at ON rate_hits (key, at);

-- audit_anchor holds the single last-seen (seq, hash) of the audit log, so a
-- truncated tail can be detected on open even though the log file itself has
-- no external anchor.
CREATE TABLE audit_anchor (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	seq INTEGER NOT NULL,
	hash TEXT NOT NULL
);
