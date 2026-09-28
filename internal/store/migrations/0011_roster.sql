-- The people roster (twins/<id>/seed/people.yaml, internal/roster): who
-- works on what, loaded once at daemon startup, idempotent. Every table
-- here follows the same normalized-record shape as messages/events/etc.
-- (source, source_id) is always "seed"/<the yaml's own id>. Relationships
-- (team membership, leadership, project allocation, client ownership) live
-- in links, not as columns here, so a new relationship kind never needs a
-- new column or table.

CREATE TABLE people (
	id INTEGER PRIMARY KEY,
	source TEXT NOT NULL,
	source_id TEXT NOT NULL,
	external INTEGER NOT NULL,
	created_at INTEGER,
	updated_at INTEGER,
	name TEXT NOT NULL DEFAULT '',
	role TEXT NOT NULL DEFAULT '',
	home_team TEXT NOT NULL DEFAULT '',
	-- identities is a JSON object, e.g. {"linear_owner_label":"Theo"}: how
	-- each connector refers to this person. Kept as opaque JSON text (not
	-- generic column machinery) since it is the one record in this file
	-- with an open-ended key set.
	identities TEXT NOT NULL DEFAULT '{}',
	UNIQUE (source, source_id)
);

CREATE TABLE teams (
	id INTEGER PRIMARY KEY,
	source TEXT NOT NULL,
	source_id TEXT NOT NULL,
	external INTEGER NOT NULL,
	created_at INTEGER,
	updated_at INTEGER,
	name TEXT NOT NULL DEFAULT '',
	linear_key TEXT NOT NULL DEFAULT '',
	UNIQUE (source, source_id)
);

CREATE TABLE projects (
	id INTEGER PRIMARY KEY,
	source TEXT NOT NULL,
	source_id TEXT NOT NULL,
	external INTEGER NOT NULL,
	created_at INTEGER,
	updated_at INTEGER,
	name TEXT NOT NULL DEFAULT '',
	linear_project TEXT NOT NULL DEFAULT '',
	start_at INTEGER,
	target_at INTEGER,
	UNIQUE (source, source_id)
);

CREATE TABLE clients (
	id INTEGER PRIMARY KEY,
	source TEXT NOT NULL,
	source_id TEXT NOT NULL,
	external INTEGER NOT NULL,
	created_at INTEGER,
	updated_at INTEGER,
	name TEXT NOT NULL DEFAULT '',
	product TEXT NOT NULL DEFAULT '',
	mrr_minor INTEGER NOT NULL DEFAULT 0,
	potential_mrr_minor INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT '',
	renewal_at INTEGER,
	UNIQUE (source, source_id)
);

CREATE TABLE vendors (
	id INTEGER PRIMARY KEY,
	source TEXT NOT NULL,
	source_id TEXT NOT NULL,
	external INTEGER NOT NULL,
	created_at INTEGER,
	updated_at INTEGER,
	name TEXT NOT NULL DEFAULT '',
	product TEXT NOT NULL DEFAULT '',
	monthly_minor INTEGER NOT NULL DEFAULT 0,
	renewal_at INTEGER,
	UNIQUE (source, source_id)
);

-- org_contacts, not "contacts": the existing contacts table (records.go) is
-- a real CRM connector's synced business contacts (HubSpot etc.); this one
-- is the roster's own investor/candidate-style entries, a different shape
-- (role, for_team, stage) that would not fit that table without distorting
-- its real meaning.
CREATE TABLE org_contacts (
	id INTEGER PRIMARY KEY,
	source TEXT NOT NULL,
	source_id TEXT NOT NULL,
	external INTEGER NOT NULL,
	created_at INTEGER,
	updated_at INTEGER,
	name TEXT NOT NULL DEFAULT '',
	role TEXT NOT NULL DEFAULT '',
	for_team TEXT NOT NULL DEFAULT '',
	stage TEXT NOT NULL DEFAULT '',
	UNIQUE (source, source_id)
);

-- links is every relationship between two roster records: kind is
-- member_of (person/project -> team), leads (person -> team/project),
-- allocated_to (person -> project, fraction of a 40h week), or owns_client
-- (person -> client). from_type/to_type name which table from_id/to_id's
-- source_id refers to ("person", "team", "project", "client"). fraction is
-- only meaningful for allocated_to; NULL otherwise.
CREATE TABLE links (
	id INTEGER PRIMARY KEY,
	kind TEXT NOT NULL,
	from_type TEXT NOT NULL,
	from_id TEXT NOT NULL,
	to_type TEXT NOT NULL,
	to_id TEXT NOT NULL,
	fraction REAL,
	created_at INTEGER NOT NULL,
	UNIQUE (kind, from_type, from_id, to_type, to_id)
);

CREATE INDEX links_from ON links (from_type, from_id, kind);
CREATE INDEX links_to ON links (to_type, to_id, kind);
