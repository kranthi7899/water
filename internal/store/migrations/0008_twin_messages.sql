-- twin_messages holds twin-to-twin messages (Slice E): what this twin sent
-- to another twin (direction 'out', recorded after a gate-approved send) and
-- what another twin sent to it (direction 'in'). Inbound messages are
-- untrusted external content by construction, so external is pinned by a
-- CHECK to exactly (direction = 'in') rather than left to the writer.
--
-- An id is unique per direction (the sender derives it deterministically
-- from the approved payload, so a re-delivery of the same message is a
-- duplicate, not a second message). content_hash lets a receiver tell an
-- identical re-delivery from a conflicting reuse of the same id.
CREATE TABLE twin_messages (
	direction TEXT NOT NULL CHECK (direction IN ('in', 'out')),
	id TEXT NOT NULL,
	from_twin TEXT NOT NULL,
	to_twin TEXT NOT NULL,
	type TEXT NOT NULL CHECK (type IN ('request', 'response', 'notice')),
	in_reply_to TEXT,
	subject TEXT NOT NULL,
	payload TEXT NOT NULL,
	evidence_refs TEXT NOT NULL DEFAULT '[]',
	needs_human_approval INTEGER NOT NULL DEFAULT 0,
	reply_by INTEGER,
	sent_at INTEGER NOT NULL,
	recorded_at INTEGER NOT NULL,
	content_hash TEXT NOT NULL,
	external INTEGER NOT NULL CHECK (external = (direction = 'in')),
	PRIMARY KEY (direction, id)
);

-- One request, one response: at most one response per request, per
-- direction, enforced by the database rather than by a check-then-insert.
CREATE UNIQUE INDEX twin_messages_one_response ON twin_messages (direction, in_reply_to) WHERE type = 'response';

CREATE INDEX twin_messages_recorded ON twin_messages (direction, recorded_at);
