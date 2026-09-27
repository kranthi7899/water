-- docs/slices/UI.md Phase 1d ("Ideas, research runs, provenance flags",
-- U2-A). The plan originally called this "migration 0019"; Phase 1a took
-- 0017 in full, Phase 1b took 0018 and Phase 1c took 0019 (see those
-- migrations' own notes), so this lands as 0020, the next free number when
-- this phase started.
--
-- ideas / idea_evidence / research_runs / research_steps are U2-A's
-- deliberately narrow schema for the Research workspace (Phase 5c) only.
-- They are NOT named jobs/tasks so Slice J's own task_records/task_events
-- naming (J Q5) stays open; J may absorb these tables later.
--
-- ideas             One captured idea. stage is a closed two-value enum
--                    today (raw|explored), validated in ideas.go, not just
--                    this CHECK. A future third stage (Phase 5c or J) needs
--                    a new migration to widen both the CHECK and
--                    store.ideaStages, not a silent write of an
--                    unvalidated string.
--
-- idea_evidence     Evidence lines attached to an idea: a source reference
--                    plus a short label, no full body text (unlike
--                    card_evidence_extra's Text column), per the plan's
--                    own terseness for this table. idea_id has a hard FK
--                    to ideas(id) (this database runs with
--                    _pragma=foreign_keys(1), see store.go): unlike
--                    card_evidence_extra's card_id, which may name a card
--                    that only ever exists computed and never gets a
--                    decision_records row, every idea this table can
--                    reference already has a real row in ideas one insert
--                    away, so attaching evidence to a nonexistent idea is
--                    rejected outright (a FK constraint error) rather than
--                    silently orphaned. id/added_at follow the same
--                    generated-id-plus-timestamp shape card_evidence_extra
--                    uses for the same identity/ordering reasons, even
--                    though the plan's own column list for this table
--                    omits them.
--
-- research_runs     One research run against an idea. status is a closed
--                    four-value enum (queued|running|finished|failed),
--                    validated in research_runs.go. attached_card_id
--                    defaults to '' (unset), the same sentinel convention
--                    ApprovalRow.SourceCardID/ThreadRef/ReplyRef already
--                    use in this schema, rather than a real SQL NULL: only
--                    a future Phase 5c Attach action (not built here) ever
--                    sets it, mirroring card_evidence_extra's own
--                    "storage only, no UI yet" scope from Phase 1c.
--                    finished_at is a real nullable INTEGER, following
--                    ApprovalRow.Deadline's nullable-timestamp convention
--                    (zero time.Time <-> SQL NULL): a run starts with no
--                    finish time. untrusted defaults to 1 and is pinned
--                    there by its own CHECK: a research report's text is
--                    always untrusted, external, model-facing web-search
--                    content -- a fixed property of every row this table
--                    holds, not a per-row choice a caller makes. The Go
--                    accessor does not accept an Untrusted argument at
--                    all, for the same reason.
--
-- research_steps    Ordered steps within one run (Phase 5c's "5 fixed,
--                    code-built steps": overview, market, competitors,
--                    pricing, risks). n is 1-based (n=1..5 for today's
--                    fixed five-step runner), matching "step 3 of 5"
--                    language rather than a zero-based index; document
--                    this choice here since the plan doesn't spell it out.
--                    status is a closed four-value enum
--                    (pending|running|done|failed) -- the plan enumerates
--                    ideas.stage and research_runs.status explicitly but
--                    not this table's status, so this set is this
--                    migration's own choice: small, and revisitable when
--                    Phase 5c's actual runner is built.
--
-- record_flags      Finding 16's fix. Flags a seeded/demo row in an
--                    existing, unrelated normalized table (messages,
--                    events, issues, ...) by that row's own (source,
--                    source_id) identity (store.Meta) plus its Table()
--                    name, without adding a provenance/simulated column to
--                    that table's own schema or Go struct: a normalized
--                    table's Meta is shared code many callers already
--                    depend on, and this concern belongs to a future
--                    Phase-7-only demo-population pass, not to every
--                    caller of every normalized table forever. table_name
--                    is deliberately a plain TEXT with no FK: it names one
--                    of several unrelated tables, which SQLite's
--                    single-target foreign keys cannot express. A future
--                    Phase 7 pass sets rows here; nothing reads them yet.
--                    flagged_at is this migration's own addition (every
--                    other table in this schema timestamps its rows; the
--                    plan's column list for this table doesn't mention
--                    one).
CREATE TABLE ideas (
	id TEXT PRIMARY KEY,
	title TEXT NOT NULL,
	gist TEXT NOT NULL DEFAULT '',
	stage TEXT NOT NULL CHECK (stage IN ('raw', 'explored')),
	provenance TEXT NOT NULL DEFAULT '',
	simulated INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);

CREATE TABLE idea_evidence (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	idea_id TEXT NOT NULL REFERENCES ideas (id),
	source TEXT NOT NULL,
	label TEXT NOT NULL,
	added_at INTEGER NOT NULL
);
CREATE INDEX idea_evidence_idea_id ON idea_evidence (idea_id);

CREATE TABLE research_runs (
	id TEXT PRIMARY KEY,
	idea_id TEXT NOT NULL REFERENCES ideas (id),
	topic TEXT NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'finished', 'failed')),
	attached_card_id TEXT NOT NULL DEFAULT '',
	report_text TEXT NOT NULL DEFAULT '',
	untrusted INTEGER NOT NULL DEFAULT 1 CHECK (untrusted = 1),
	provenance TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	finished_at INTEGER
);
CREATE INDEX research_runs_idea_id ON research_runs (idea_id);

CREATE TABLE research_steps (
	run_id TEXT NOT NULL REFERENCES research_runs (id),
	n INTEGER NOT NULL CHECK (n >= 1),
	label TEXT NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'done', 'failed')),
	source_count INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (run_id, n)
);

CREATE TABLE record_flags (
	table_name TEXT NOT NULL,
	source TEXT NOT NULL,
	source_id TEXT NOT NULL,
	provenance TEXT NOT NULL DEFAULT '',
	simulated INTEGER NOT NULL DEFAULT 0,
	flagged_at INTEGER NOT NULL,
	PRIMARY KEY (table_name, source, source_id)
);
