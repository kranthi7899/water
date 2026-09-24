-- route_log records every turn's routing decision (Slice R). utterance is
-- local-only data; partial transcripts are never stored here, only their
-- counts and timings.
CREATE TABLE route_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	turn_id TEXT NOT NULL UNIQUE,
	client_turn_id TEXT NOT NULL DEFAULT '',
	at INTEGER NOT NULL,
	channel TEXT NOT NULL,
	utterance TEXT NOT NULL,
	tiers_attempted TEXT NOT NULL,
	owner TEXT NOT NULL DEFAULT '',
	answered_by TEXT NOT NULL DEFAULT '',
	intent TEXT NOT NULL DEFAULT '',
	intent_kind TEXT NOT NULL DEFAULT '',
	intent_origin TEXT NOT NULL DEFAULT '',
	slots TEXT NOT NULL DEFAULT '{}',
	escalation_reason TEXT NOT NULL DEFAULT '',
	latency_ms TEXT NOT NULL DEFAULT '{}',
	total_ms INTEGER NOT NULL,
	outcome TEXT NOT NULL,
	warnings TEXT NOT NULL DEFAULT '[]',
	voice INTEGER NOT NULL DEFAULT 0,
	partials INTEGER NOT NULL DEFAULT 0,
	first_partial_lead_ms INTEGER,
	speculation TEXT NOT NULL DEFAULT '{}',
	speculation_model_calls INTEGER NOT NULL DEFAULT 0,
	ack_ms INTEGER,
	first_sentence_ms INTEGER,
	tools_used TEXT NOT NULL DEFAULT '[]',
	tools_attributed INTEGER NOT NULL DEFAULT 1,
	quick_only INTEGER NOT NULL DEFAULT 0,
	tool_signature TEXT NOT NULL DEFAULT '',
	action TEXT NOT NULL DEFAULT '{}',
	possible_miss INTEGER NOT NULL DEFAULT 0,
	confirmed INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX route_log_at ON route_log (at);
CREATE INDEX route_log_intent ON route_log (intent, at);
CREATE INDEX route_log_quick_only ON route_log (tool_signature, at) WHERE quick_only = 1;

-- intent_state is per-intent enable/disable for learned intents (the
-- promotion loop's manual and automatic demotion). Absent = enabled.
CREATE TABLE intent_state (
	intent_id TEXT PRIMARY KEY,
	disabled INTEGER NOT NULL DEFAULT 0,
	reason TEXT NOT NULL DEFAULT '',
	at INTEGER NOT NULL
);

-- The route log's reflex handlers (mail.latest, mail.latest_from,
-- mail.unread_count) filter and order by these; they didn't exist before
-- Slice R needed direct message lookups outside the generic List() path.
CREATE INDEX IF NOT EXISTS messages_sent_at ON messages (sent_at);
CREATE INDEX IF NOT EXISTS messages_sender ON messages (sender);
