CREATE TABLE messages (
	id INTEGER PRIMARY KEY,
	source TEXT NOT NULL,
	source_id TEXT NOT NULL,
	external INTEGER NOT NULL,
	created_at INTEGER,
	updated_at INTEGER,
	channel TEXT NOT NULL DEFAULT '',
	thread TEXT NOT NULL DEFAULT '',
	sender TEXT NOT NULL DEFAULT '',
	recipients TEXT NOT NULL DEFAULT '[]',
	subject TEXT NOT NULL DEFAULT '',
	body TEXT NOT NULL DEFAULT '',
	sent_at INTEGER,
	UNIQUE (source, source_id)
);

CREATE TABLE meetings (
	id INTEGER PRIMARY KEY,
	source TEXT NOT NULL,
	source_id TEXT NOT NULL,
	external INTEGER NOT NULL,
	created_at INTEGER,
	updated_at INTEGER,
	title TEXT NOT NULL DEFAULT '',
	start_at INTEGER,
	end_at INTEGER,
	attendees TEXT NOT NULL DEFAULT '[]',
	organizer TEXT NOT NULL DEFAULT '',
	transcript_ref TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	UNIQUE (source, source_id)
);

CREATE TABLE events (
	id INTEGER PRIMARY KEY,
	source TEXT NOT NULL,
	source_id TEXT NOT NULL,
	external INTEGER NOT NULL,
	created_at INTEGER,
	updated_at INTEGER,
	title TEXT NOT NULL DEFAULT '',
	start_at INTEGER,
	end_at INTEGER,
	location TEXT NOT NULL DEFAULT '',
	attendees TEXT NOT NULL DEFAULT '[]',
	organizer TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT '',
	UNIQUE (source, source_id)
);

CREATE TABLE documents (
	id INTEGER PRIMARY KEY,
	source TEXT NOT NULL,
	source_id TEXT NOT NULL,
	external INTEGER NOT NULL,
	created_at INTEGER,
	updated_at INTEGER,
	title TEXT NOT NULL DEFAULT '',
	url TEXT NOT NULL DEFAULT '',
	mime_type TEXT NOT NULL DEFAULT '',
	owner TEXT NOT NULL DEFAULT '',
	excerpt TEXT NOT NULL DEFAULT '',
	modified_at INTEGER,
	UNIQUE (source, source_id)
);

CREATE TABLE issues (
	id INTEGER PRIMARY KEY,
	source TEXT NOT NULL,
	source_id TEXT NOT NULL,
	external INTEGER NOT NULL,
	created_at INTEGER,
	updated_at INTEGER,
	title TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL DEFAULT '',
	assignee TEXT NOT NULL DEFAULT '',
	project TEXT NOT NULL DEFAULT '',
	priority TEXT NOT NULL DEFAULT '',
	url TEXT NOT NULL DEFAULT '',
	UNIQUE (source, source_id)
);

CREATE TABLE commits (
	id INTEGER PRIMARY KEY,
	source TEXT NOT NULL,
	source_id TEXT NOT NULL,
	external INTEGER NOT NULL,
	created_at INTEGER,
	updated_at INTEGER,
	repo TEXT NOT NULL DEFAULT '',
	sha TEXT NOT NULL DEFAULT '',
	author TEXT NOT NULL DEFAULT '',
	message TEXT NOT NULL DEFAULT '',
	committed_at INTEGER,
	url TEXT NOT NULL DEFAULT '',
	UNIQUE (source, source_id)
);

CREATE TABLE transactions (
	id INTEGER PRIMARY KEY,
	source TEXT NOT NULL,
	source_id TEXT NOT NULL,
	external INTEGER NOT NULL,
	created_at INTEGER,
	updated_at INTEGER,
	account TEXT NOT NULL DEFAULT '',
	amount_minor INTEGER NOT NULL DEFAULT 0,
	currency TEXT NOT NULL DEFAULT '',
	counterparty TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	posted_at INTEGER,
	UNIQUE (source, source_id)
);

CREATE TABLE contacts (
	id INTEGER PRIMARY KEY,
	source TEXT NOT NULL,
	source_id TEXT NOT NULL,
	external INTEGER NOT NULL,
	created_at INTEGER,
	updated_at INTEGER,
	name TEXT NOT NULL DEFAULT '',
	email TEXT NOT NULL DEFAULT '',
	phone TEXT NOT NULL DEFAULT '',
	org TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '',
	UNIQUE (source, source_id)
);

CREATE TABLE approvals (
	id TEXT PRIMARY KEY,
	action TEXT NOT NULL,
	recipient TEXT NOT NULL DEFAULT '',
	payload TEXT NOT NULL,
	payload_hash TEXT NOT NULL,
	evidence_refs TEXT NOT NULL DEFAULT '[]',
	risk TEXT NOT NULL DEFAULT '',
	origin TEXT NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'denied', 'expired', 'executed')),
	reason TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	decided_at INTEGER,
	executed_at INTEGER
);

CREATE INDEX approvals_status ON approvals (status, created_at);
