-- The workspace/UI slice (Slice V): the embedded web UI's own state, none of
-- which fits the connector-normalized-record model above. Five tables:
--
--   workspaces      A UI-level container that can span projects (grouping
--                    decisions, meetings, jobs, threads). Follows the same
--                    normalized-record shape as roster_records.go and
--                    finance_records.go (source/source_id identity, Upsert/
--                    Get/List for free) — distinct from the roster's own
--                    "projects" table, which is seed data, not a UI concept.
--
--   threads          A conversation the CEO has with the twin, optionally
--                    anchored to one other record (a decision, approval,
--                    message or meeting) via anchor_type/anchor_id. Unlike
--                    every table above, this is NOT a normalized record: its
--                    id is application-generated ("thr_<random>", see
--                    threads.go) and addressed directly, since nothing
--                    upstream assigns it a (source, source_id) identity.
--                    anchor_context is a snapshot of the anchored record
--                    (e.g. a rendered decision card) captured once at
--                    thread-creation time, so opening a thread never needs to
--                    re-fetch or rebuild what it was originally about.
--                    anchor_untrusted marks that snapshot as attacker-
--                    reachable content (an email body, meeting notes) the UI
--                    must render as text only, never as markup.
--                    threads_anchor is a partial unique index, not a plain
--                    UNIQUE constraint: it only applies to anchored threads
--                    (anchor_type <> ''), so tapping the same record twice
--                    reuses one thread, while any number of un-anchored,
--                    free-standing chat threads (anchor_type = '') can exist
--                    side by side.
--
--   thread_messages  One turn in a thread, CEO or twin, in conversation
--                    order. channel/task_id are carried through from
--                    nervous.Handle's own turn metadata so a thread message
--                    can be traced back to the route_log row (or async task)
--                    that produced it.
--
--   card_states      The durable record of "the CEO already dismissed or
--                    staged this card." Decision cards themselves are never
--                    persisted — they're rebuilt fresh on every
--                    Trigger.Run — so without this table, a dismissed card
--                    would simply reappear on the next request.
--
--   notifications    At-most-once-ever native notifications. The
--                    UNIQUE(record_type, record_id) constraint is load-
--                    bearing: InsertNotificationIfNew relies on it (an
--                    INSERT that is ignored on conflict) so "have we ever
--                    notified about this record" survives a daemon restart
--                    without any application-level bookkeeping.

CREATE TABLE workspaces (
	id INTEGER PRIMARY KEY,
	source TEXT NOT NULL,
	source_id TEXT NOT NULL,
	external INTEGER NOT NULL,
	created_at INTEGER,
	updated_at INTEGER,
	name TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	UNIQUE (source, source_id)
);

-- title is NOT NULL DEFAULT '', matching the NOT NULL DEFAULT '' convention
-- every other text column in this migration (and 0011/0012 before it) uses,
-- rather than left nullable — this table's own Go code (threads.go) never
-- writes NULL into it, and a plain '' keeps scanning uniform with the rest
-- of the package instead of needing a sql.NullString special case here alone.
CREATE TABLE threads (
	id TEXT PRIMARY KEY,
	title TEXT NOT NULL DEFAULT '',
	anchor_type TEXT NOT NULL DEFAULT '',
	anchor_id TEXT NOT NULL DEFAULT '',
	anchor_context TEXT NOT NULL DEFAULT '',
	anchor_untrusted INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

-- Anchoring the same (anchor_type, anchor_id) twice must return the existing
-- thread, not create a duplicate; a thread with no anchor (anchor_type = '')
-- is exempt, since many un-anchored threads can coexist.
CREATE UNIQUE INDEX threads_anchor ON threads (anchor_type, anchor_id) WHERE anchor_type <> '';

CREATE TABLE thread_messages (
	id INTEGER PRIMARY KEY,
	thread_id TEXT NOT NULL REFERENCES threads(id),
	role TEXT NOT NULL CHECK (role IN ('ceo', 'twin')),
	channel TEXT NOT NULL DEFAULT '',
	task_id TEXT NOT NULL DEFAULT '',
	text TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);

CREATE INDEX thread_messages_thread ON thread_messages (thread_id, created_at);

CREATE TABLE card_states (
	card_id TEXT PRIMARY KEY,
	status TEXT NOT NULL CHECK (status IN ('dismissed', 'staged')),
	reason TEXT NOT NULL DEFAULT '',
	approval_id TEXT NOT NULL DEFAULT '',
	decided_at INTEGER NOT NULL
);

CREATE TABLE notifications (
	id TEXT PRIMARY KEY,
	record_type TEXT NOT NULL,
	record_id TEXT NOT NULL,
	title TEXT NOT NULL DEFAULT '',
	body TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	delivered_at INTEGER,
	UNIQUE (record_type, record_id)
);
