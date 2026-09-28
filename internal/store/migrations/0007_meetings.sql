-- meeting_sessions is one live-meeting listening session (Slice M), started
-- and stopped manually from the client. event_id is the calendar event's
-- source_id when the session was started from one.
CREATE TABLE meeting_sessions (
	id TEXT PRIMARY KEY,
	started_at INTEGER NOT NULL,
	ended_at INTEGER,
	event_id TEXT
);

-- meeting_segments holds the on-device transcript text the client posts,
-- tagged by channel (mic is the CEO, system is everyone else). Never audio.
-- Every segment is untrusted external content regardless of channel, so
-- external is pinned to 1 by a CHECK rather than left to the writer.
CREATE TABLE meeting_segments (
	id INTEGER PRIMARY KEY,
	session_id TEXT NOT NULL REFERENCES meeting_sessions (id),
	at INTEGER NOT NULL,
	channel TEXT NOT NULL CHECK (channel IN ('mic', 'system')),
	text TEXT NOT NULL,
	external INTEGER NOT NULL DEFAULT 1 CHECK (external = 1)
);

CREATE INDEX meeting_segments_session_at ON meeting_segments (session_id, at);

-- messages_fts and documents_fts are external-content FTS5 indexes over the
-- stored text, kept in sync by triggers so every Upsert path (insert, and
-- the ON CONFLICT update) is covered without Upsert knowing about them.
CREATE VIRTUAL TABLE messages_fts USING fts5 (
	subject, body, sender,
	content = 'messages', content_rowid = 'id', tokenize = 'porter unicode61'
);

CREATE TRIGGER messages_fts_ai AFTER INSERT ON messages BEGIN
	INSERT INTO messages_fts (rowid, subject, body, sender) VALUES (new.id, new.subject, new.body, new.sender);
END;

CREATE TRIGGER messages_fts_ad AFTER DELETE ON messages BEGIN
	INSERT INTO messages_fts (messages_fts, rowid, subject, body, sender) VALUES ('delete', old.id, old.subject, old.body, old.sender);
END;

CREATE TRIGGER messages_fts_au AFTER UPDATE ON messages BEGIN
	INSERT INTO messages_fts (messages_fts, rowid, subject, body, sender) VALUES ('delete', old.id, old.subject, old.body, old.sender);
	INSERT INTO messages_fts (rowid, subject, body, sender) VALUES (new.id, new.subject, new.body, new.sender);
END;

CREATE VIRTUAL TABLE documents_fts USING fts5 (
	title, excerpt,
	content = 'documents', content_rowid = 'id', tokenize = 'porter unicode61'
);

CREATE TRIGGER documents_fts_ai AFTER INSERT ON documents BEGIN
	INSERT INTO documents_fts (rowid, title, excerpt) VALUES (new.id, new.title, new.excerpt);
END;

CREATE TRIGGER documents_fts_ad AFTER DELETE ON documents BEGIN
	INSERT INTO documents_fts (documents_fts, rowid, title, excerpt) VALUES ('delete', old.id, old.title, old.excerpt);
END;

CREATE TRIGGER documents_fts_au AFTER UPDATE ON documents BEGIN
	INSERT INTO documents_fts (documents_fts, rowid, title, excerpt) VALUES ('delete', old.id, old.title, old.excerpt);
	INSERT INTO documents_fts (rowid, title, excerpt) VALUES (new.id, new.title, new.excerpt);
END;

-- Index whatever was stored before this migration.
INSERT INTO messages_fts (messages_fts) VALUES ('rebuild');
INSERT INTO documents_fts (documents_fts) VALUES ('rebuild');
