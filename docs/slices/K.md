# Slice K: the orchestration clock (durable waits, reminders, timeouts, retries, escalation-on-timeout)

**Planning only. Do not implement until Slice J is built and verified.**
This spec authorizes no code yet. It is one sub-slice of the
intelligence-layer redesign; see the overview doc for the full F–Q
sequence and the global open-questions list. Build it with
`docs/WORKFLOW.md`'s Explore → Plan → Approve → Implement loop, and
re-read the files named below at build time, because J will have changed
the store by then.

Builds on: J (the durable task record, its state machine and its
`next_action_at` index), plus machinery that already exists: `gate`
(R/D/A/S/B levels, P0/P1/P2 origins, `authorize`'s "P2 never reaches level
A" rule), `approvals` (`Queue.Propose`, payload-hash envelopes,
`ExpireStale`'s "silence is a no"), `audit`, `store` (SQLite, migrations
0001–0013), and V-schema's `notifications` table plus `internal/needsyou`.
It does not depend on memory (F/G), skills (H), the analysis lane (I), the
composer (L) or reply ingestion (N).

Before K, a task record exists (J) but nothing moves it: no timer fires,
no one gets reminded, and a task nobody touches sits in `awaiting_inputs`
forever. K adds the one mechanical component that owns **time**, and
nothing else.

## Principle

The clock decides "keep waiting", never the model. K is deterministic,
task-agnostic Go code. It makes **no model call**, reads no message
content, and does not know what a task is about. It knows timers, attempt
counts, deadlines, and the time- and count-based escalation triggers that
authoring (O) stored on the task record. It never sends anything itself.
An outward reminder is at most a *staged* approval envelope, and until the
owner decides the reminder policy (see Open design points) it does not even
do that. It escalates to the CEO and sends nothing.

The brief's second clock, the quality gate, owns *content* and belongs to
L. N owns turning a reply into a signal. K only schedules and fires.

**K owns time only, and never judges completion or quality** (reconfirmed
by the owner's amendment, 2026-09-25; this split must not blur during
implementation). K may decide *when* to look (a timer fires) and apply a
purely structural or count rule to what it sees: every participant
terminal by J's `Structural` read, a deadline passed, reminders exhausted.
It never decides whether collected answers are good enough, whether a
criterion is met, or whether a task is done. Those verdicts are written
into the record by L's verifier, and a task reaches `delivered` only
through that write, never through a K write.

**K does not receive work from anyone, and hands work to no one.** Under
the shared-state model (J's Principle, the overview's §2.1), the task
record is the only thing K shares with N, L and O. N writes a reply's
signal into the record the instant it arrives, and nothing tells K. K's
timers fire on their own schedule, and each firing reads the record as it
stands at that moment. K never calls the composer or the critic. A task
that K moves to `synthesizing` is picked up from the record by L's drafter
on its own schedule (L §3.8).

**This is a different concurrency problem from a background task next to
one live session.** The coding-agent tooling this project is built with
already runs a single background job alongside a live session and reports
back into that still-running process. K's problem is harder in three
specific ways, and each shapes the design:
- **It must survive a multi-day wait.** A fan-out may wait days on people.
  Nothing K needs can live in a goroutine, a channel or process memory
  across that wait. Timers and state are rows (§1), and K keeps no
  in-memory schedule it could lose.
- **It must survive a daemon restart mid-wait.** The process that
  dispatched the asks is usually not the process that notices the last
  reply. There is no caller to "report back" to, so K writes its outcome
  into the record, and whatever needs it reads it from there (coalescing
  and two-phase staging, §2).
- **It acts on partial, asynchronous human replies.** Replies arrive one at
  a time, out of order, some never. K cannot wait on "the result". Each
  timer firing acts on whatever subset of replies the record holds at that
  moment, and the quorum question (open) decides only what K's handler
  does with a partial set, never whether K waits in-process.

## Relationship to existing code: why this is not a workflow engine

Explore-phase reading (2026-09-25) of what K extends:

- **`internal/sync.Refresher`** (`internal/sync/sync.go`). `Run` starts
  independent goroutines, each `loop(ctx, interval, tick)`: tick
  immediately, then on a `time.Ticker`, until ctx is cancelled. `tick`
  does `loadCursor` → `gate.Invoke` (Origin P2, Taint Clean) → on the
  expired sentinel `DeleteCursor`, else `SetCursor(next)`, over the
  generic `sync_cursors` key/value table (`store.GetCursor`/`SetCursor`/
  `DeleteCursor`). Errors are logged and retried next interval, never fatal.
- **`internal/needsyou.Service`**. `Tick(ctx, now)` recomputes a snapshot
  and calls `store.InsertNotificationIfNew` per item. The table's
  `UNIQUE (record_type, record_id)` constraint, not in-memory state, is
  what makes "notify once per record, ever" survive a restart. It is
  driven by its own goroutine in `internal/cli/cmd_daemon.go`, with the
  same "tick now, then on the ticker" shape and deliberately **not**
  nested in the refresher, so a twin with no `gcal.list_events` grant
  still gets notifications.
- **`internal/sync/prefetch.go`**. `prefetchState.done` is an in-memory
  map, so each event is prefetched at most once *per daemon life*. That
  is acceptable for a prefetch and **not** acceptable for a reminder or
  deadline, which must survive a restart.

**Decision (from the brief, recorded here):** K extends this ticker
pattern. It adds one more independent loop of the same shape, and its
cursor is store-backed (`next_action_at` plus a timers table), not the
`sync_cursors` table and not an in-memory set. Water needs no workflow
engine and has none today. **K adds no new dependency, and this includes
any workflow-engine library.** If a later session ever proposes one, it
needs the owner's explicit approval under `CLAUDE.md` ("Ask before adding
anything else") before any code is written.

Two naming hazards found while exploring:

1. `docs/CONTEXT.md` records that the old council's `internal/orchestrator`
   (a Go state-graph executor and checkpointer) was **deliberately
   deleted**, not kept. K must not resurrect that package name or its
   shape. The package is `internal/taskclock`: a timer loop over task
   records, with no graph, no checkpointer and no role-to-role delegation.
   `docs/CONTEXT.md`'s tree also lists an `internal/scheduler/` that does
   not exist. K does not create it either.
2. `internal/config`'s `retiredPrefixes` includes `"orchestration."` (and
   `"skills."`, `"memory."`), which are **silently ignored** on load. K's
   config keys must therefore live under a new prefix, `tasks.clock.*`.
   A key under `orchestration.*` would load without error and do nothing.

## Scope

### 1. Timers: a store-backed table, owned by K

J owns the task row, including `state`, `next_action_at` (indexed) and
archetype state. K adds one table in its own migration. The number is
chosen at commit time, never pre-reserved, because concurrent sessions
collided on migration numbers twice during R.

```sql
CREATE TABLE task_timers (
  id           TEXT PRIMARY KEY,
  task_id      TEXT NOT NULL,          -- J's task id
  kind         TEXT NOT NULL,          -- wait | reminder | deadline | retry | step_timeout
  subject      TEXT NOT NULL DEFAULT '',  -- opaque key: a participant id, a criterion id,
                                           -- a step id; K never interprets it
  due_at       INTEGER NOT NULL,       -- unix nanos, UTC
  attempt      INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 0,  -- 0 = not count-limited
  fired_at     INTEGER,                -- NULL until fired
  outcome      TEXT NOT NULL DEFAULT '',   -- what firing did (see §3)
  envelope_id  TEXT NOT NULL DEFAULT ''    -- set only if a reminder was staged
);
CREATE INDEX task_timers_due ON task_timers(task_id, fired_at, due_at);
```

**Invariant, enforced in one transaction:** a task's `next_action_at`
equals the earliest `due_at` of its unfired timers, or NULL when it has
none. Every K write that adds, fires or cancels a timer recomputes and
stores it in the same transaction. The due-work query then stays J's
single indexed scan over tasks. J's spec already provides the
optimistic-lock column (`task_records.version`, with `ErrVersionConflict`
on a lost compare-and-swap), which §2 relies on.

Why a separate table instead of packing timers into J's archetype state:
reminders, retries and timeouts are per-subject (one per participant in a
fan-out) and need counts. A generic table keeps K ignorant of archetype
JSON, which is what "task-agnostic" requires in practice.

### 2. The due-work loop

```go
// internal/taskclock/clock.go
type Clock struct { /* store, approvals queue, audit log, handlers, cfg, Now */ }

// Tick fires every due timer on every task whose next_action_at <= now,
// in batches of cfg.BatchLimit, then returns. Exported so tests and a
// manual `water tasks tick` drive it with a fixed now, without sleeps,
// the same way needsyou.Service.Tick and sync.Refresher.RunOnce are.
func (c *Clock) Tick(ctx context.Context, now time.Time) error

// Run ticks immediately, then on cfg.Interval, until ctx is done.
func (c *Clock) Run(ctx context.Context)
```

For each due task, the per-task step is: load the task and its due
timers → decide the outcome in pure code → write the new task state, the
fired timers, the new timers, the recomputed `next_action_at` and J's
audit-trail entries in **one transaction**, guarded by the task's
`version` (compare-and-swap). If the swap loses, because a concurrent
writer such as N ingesting a reply wrote the task first, K drops the
result and re-reads on the next tick. Nothing is double-fired. K is never
told about another writer's change. It sees it only as the record's
current contents the next time one of its timers fires.

**Crash and downtime behavior (acceptance-critical):**
- **Coalescing, not catch-up.** After the daemon has been down past
  several reminder intervals, each subject's overdue reminder fires
  **once**, and the next one is scheduled from *now*. It does not fire
  once per missed interval.
- **Outward staging is two-phase.** K first records `outcome =
  stage_pending` on the timer and commits. It then calls
  `approvals.Queue.Propose` with the task id and timer id in
  `EvidenceRefs`, and finally records the returned `envelope_id`. On
  restart, a `stage_pending` timer with no `envelope_id` searches pending
  and approved envelopes for that evidence ref before proposing again. A
  crash can therefore never produce two reminder envelopes for the same
  timer.
- A task in a terminal state (`delivered` or `abandoned`, per J's §3) has
  all its unfired timers cancelled in the same transaction that
  terminates it, and it never appears in the due scan again. J's
  `escalated` and `awaiting_ceo` are parked states, not terminal: their
  timers are not cancelled by K, and a handler must not act on a timer
  while its task is parked (P's monitor handler relies on this, P §1.4).

**Daemon wiring:** its own goroutine in `cmd_daemon.go`, next to
needsyou's and with the same shape, not nested inside the refresher (the
same reasoning as needsyou: the demo twin has no calendar grant). It is
off by default behind `tasks.clock.enabled`. A tick error is logged and
retried next interval, never fatal.

### 3. What firing a timer does

K implements the generic mechanics. Archetype-specific meaning comes from
a handler interface that O (fan-out) and P (monitor, pipeline) register.
K ships only a test handler.

```go
// internal/taskclock/handler.go
type Handler interface {
    // OnTimer is pure: no I/O, no model call. It returns what the task
    // should do next; the Clock performs it.
    OnTimer(task Task, t Timer, now time.Time) Outcome
}

type Outcome struct {
    Transition   string        // a J state, or "" for no change; J's
                               // transition table still rejects illegal moves
    Schedule     []Timer       // new timers
    Cancel       []string      // timer ids
    StageOutward *OutwardStep  // at most one; see §4
    Escalate     *Escalation   // see §5
}
```

Built-in behavior that no handler can override:
- **`wait` / `deadline`**: when a deadline passes, evaluate the task's
  stored time-based triggers (§5). A `wait` timer is also how a handler
  re-reads the record on a cadence (O's `check_inputs`, P's probes). The
  handler decides only the structural consequence, reading J's
  `Structural` at that moment: for example, whether a fan-out whose
  participants are all terminal moves to `synthesizing`. Whether a fan-out
  with a missing reply may proceed with the gap flagged depends on a quorum
  rule, which is an open question, so K's test handler never proceeds on
  partial replies. No handler judges whether the collected content is good
  enough. That is L's verifier, reading the same record.
- **`retry`**: exponential backoff with jitter, capped at
  `tasks.clock.retry_max_delay`, up to `max_attempts`. Retries apply
  **only** to mechanical steps whose function is level R or D. An
  outward (level A) step is never retried by the clock. A connector error
  of `gapi.ErrSendOutcomeUnknown` ("check before retrying") is never
  retried. It escalates with the reason "send outcome unknown". The
  exhaustion of `max_attempts` fires the `retries_exhausted` trigger.
- **`step_timeout`**: fires `step_timeout` when a dispatched mechanical
  step has not reported completion by its deadline.
- **`reminder`**: see §4.

Any gate call K makes (a retried read step) uses **Origin P1** ("a task
the CEO approved or scheduled", `internal/gate/gate.go`), not P2, because
the CEO initiated the task. Today the gate gives P1 no treatment distinct
from P0 beyond validity. Nothing in K changes that.

### 4. Reminders: the mechanism is built, the policy is off

K builds the reminder mechanism: a per-subject `reminder` timer with an
`attempt`/`max_attempts` count and a cadence. When it fires under the
**only policy enabled by default, `notify_ceo`**, it creates a CEO-facing
notification (§5) naming the subject and the task. It **stages nothing
and sends nothing**.

A second policy, `stage_envelope`, is implemented and tested with
`backend.Fake` and a fake connector, but it is reachable only when the
owner sets it explicitly. When it is on:
- The reminder is proposed through `approvals.Queue.Propose` as an
  ordinary envelope (for example `gmail.send_message`, level A, sent as
  the agent alias). It executes only through the existing approval path,
  with the gate check and the payload hash. K never calls `gate.Invoke`
  on a level-A function.
- The payload comes from `OutwardStep`, which O's handler supplies
  (text authored at planning time). K generates no content and makes no
  model call.
- **Envelope expiry must be set explicitly.** `approvals.DefaultTTL` is
  15 minutes, and `ExpireStale` treats silence as a no. A reminder staged
  overnight would expire unseen, so K sets `ExpiresAt` from config. An
  expired reminder envelope is recorded as "not sent (expired)", counts as
  an attempt, and is **not** automatically re-proposed.
- Config loading **fails** if `stage_envelope` is selected without an
  explicit `max_reminders` and cadence. K ships no default count or
  cadence for real sends, because those are open owner questions.

### 5. Escalation-on-timeout, and the one shared escalation sink

K evaluates **only** the authoring-time triggers that are time- or
count-based, as stored on the task by J (written by O):

| trigger | fires when |
|---|---|
| `reminders_exhausted_critical` | a subject's reminder `attempt == max_attempts` and the subject is tied to a criterion marked critical |
| `deadline_passed` | the task's overall deadline passes in a non-terminal state |
| `step_timeout` | §3 |
| `retries_exhausted` | §3 |

K ignores content-based triggers: conflicting replies, a
critic-contradicted criterion, a sensitivity match, and
ambiguity-before-dispatch. N, L and O evaluate those. K does own the
**single escalation sink** they all call, so there is exactly one way a
task reaches the CEO:

```go
func (c *Clock) Escalate(ctx context.Context, taskID string, e Escalation) error
```

It is a **synchronous record write**, not a hand-off to K's loop. It
runs in the caller's own goroutine, needs no timer and no tick, and
involves no judgment by K: the caller (N, L, or an O or P handler) has already decided,
from its own content rule, that the trigger fires. The sink lives in
`internal/taskclock` only so that there is one writer of task
notifications. It moves the task to `escalated` (or `awaiting_ceo`, which
J's table decides) and appends to J's audit trail. It then calls
`store.InsertNotificationIfNew` with `RecordType: "task"` and
`RecordID: taskID + ":" + triggerID`. The per-trigger record id matters,
because the notifications table allows one notification per
`(record_type, record_id)` *ever*: a bare task id would silently swallow
a second, different escalation on the same task.

`internal/needsyou.Compute` gains a third source, tasks in `escalated` or
`awaiting_ceo`, read from the store (`needsyou` keeps its one-way-import
rule: it depends on `store`, never on `taskclock`). Where that block
sorts relative to decisions and approvals is a small V-ui question,
noted and not decided here.

**Escalation target:** the CEO only, through Notification/needsyou. K
never contacts a non-responder's manager and never nudges in the CEO's
name. See Open design points.

### 6. Config (`tasks.clock.*`, all new keys)

`enabled` (default false), `interval_seconds` (proposed 60),
`batch_limit`, `retry_base_seconds`, `retry_max_delay_seconds`,
`retry_max_attempts` (mechanical-retry defaults are proposed in the
Plan phase; they are not owner policy), `reminders.policy`
(`notify_ceo` | `stage_envelope`, default `notify_ceo`),
`reminders.max`, `reminders.cadence_seconds` and
`reminders.envelope_ttl_seconds`. The last three have no default and are
required only if the policy is `stage_envelope`.

### 7. CLI

J's read-only `water tasks show <id>` gains a timers section (due,
fired, attempt/max, outcome, envelope). A new `water tasks tick` runs one
`Clock.Tick` at the current time (the manual trigger, like
`Refresher.RunOnce`). No new write commands. Abandoning a task is O's
CEO-facing concern.

## Do not build in K

- Any model call, any content generation, any reply parsing (N), any
  quality or completeness judgment (L), any planning or trigger authoring
  (O).
- Real archetype handlers (fan-out: O; monitor and pipeline: P).
- Autonomous sending of any kind, standing grants or batch approvals
  (still deferred in `docs/known-gaps.md`).
- A workflow-engine dependency, a revived `internal/orchestrator`, or an
  `internal/scheduler` package.

## Open design points (owner decisions; do not resolve in the build)

These are the redesign's global open questions as they touch K. Until the
owner decides, K's behavior is "escalate to the CEO, send nothing".

- **Reminder count and cadence per participant**, and whether they vary
  by seniority or relationship. K stores `max_attempts`/`due_at` per
  subject, so a per-relationship policy fits later without a schema
  change. K picks no numbers.
- **Escalation target.** The source paste was garbled at exactly this
  point. The plausible readings are: (a) the twin may nudge the
  non-responder *in the CEO's name*, (b) the twin may escalate to the
  non-responder's *manager* (`roster` has `leads`/`member_of` links that
  could resolve one), or (c) the twin only ever escalates *to the CEO*.
  K implements (c) only, as the safe interim. (a) and (b) would each be
  a new outward action needing its own gate level and envelope, and
  neither is built.
- **Quorum** (whether "4 of 5 replied" is enough to proceed when the
  deadline passes). This is handler policy, not clock mechanics. K's test
  handler never proceeds on a partial set.
- **Confidence thresholds.** K uses none. It is purely time- and
  count-based, which is why it can ship before any calibration data
  exists.
- **Reminder-envelope TTL** relative to the CEO's response habits (§4).
  This only matters once `stage_envelope` is enabled.

## Acceptance criteria / tests

All tests use a fake clock (an injected `Now` or an explicit `now` to
`Tick`) and `backend.Fake` where anything touches a backend. No test
sleeps.

- **Durable across restart.** Schedule timers, close the store, reopen it
  and build a new `Clock`, then tick past `due_at`: each fires exactly
  once.
- **Coalescing.** A reminder timer that is 5 cadences overdue fires once,
  and the next is due at `now + cadence`.
- **`next_action_at` invariant.** After every operation (schedule, fire,
  cancel, terminate), a property-style test checks that the task's
  `next_action_at` equals the minimum unfired `due_at`, or NULL.
- **No double fire.** A concurrent `version` bump between load and write
  (simulating N) makes K's write lose cleanly, and the next tick
  re-evaluates.
- **Observed, not pushed.** A record change between ticks (simulating N
  writing a participant's status) triggers no K action by itself. When the
  next timer on that task fires, the test handler receives the record as
  it stands at firing time, including the change. No K API exists for
  another component to notify, wake or call the clock.
- **Time only.** No K code path moves a task to `delivered` or writes a
  criterion status. A guard test over `internal/taskclock` asserts it never
  calls `Service.SetCriterion` and never passes `delivered` to `Move`.
- **Two-phase staging.** A simulated crash after `stage_pending` but
  before `envelope_id` is recorded, followed by a restart and a tick,
  yields exactly one envelope for that timer.
- **Default policy sends nothing.** With the defaults, an exhausted
  reminder on a critical criterion creates exactly one `task` notification
  (`RecordID` = task:trigger), moves the task to `escalated`, and
  **proposes zero envelopes and makes zero gate calls on level-A
  functions**.
- **Distinct triggers notify separately.** Two different triggers on one
  task produce two notifications, and the same trigger twice produces one.
- **Outward only via envelope.** Under `stage_envelope`, the reminder
  appears as a pending envelope with an explicit `ExpiresAt`, nothing
  executes until `Decide(Yes)`, and an expired envelope counts an attempt
  and is not re-proposed.
- **Retry scope.** A failing level-R step backs off (checked delays with
  jitter bounds) up to `max_attempts`, then fires `retries_exhausted`. A
  level-A step is never retried. `gapi.ErrSendOutcomeUnknown` escalates
  immediately.
- **Terminal tasks go quiet.** Terminating a task cancels its timers, and
  it never appears in the due scan again.
- **Config.** `stage_envelope` without `reminders.max`/cadence fails
  `config.Load`, and a `tasks.clock.*` key is recognized, not silently
  ignored the way `orchestration.*` is.
- **Guard tests.** `internal/taskclock` imports none of `backend`,
  `runtime`, `nervous`, `decisions` or `memory` (no model and no content
  path), and `go.mod` gains no requirement in this slice.
- **needsyou.** `Compute` includes escalated tasks, and existing
  decision/approval ordering tests still pass.
- `go vet ./...`, `go test -count=1 ./...` and
  `CGO_ENABLED=0 go build ./cmd/water` pass, as always.

## Dependencies

- **Hard: J.** It provides the task row, the state transition table (K
  calls it and never bypasses it), the indexed `next_action_at`, the
  stored escalation triggers, criteria marked critical, and the audit
  trail. K cannot start until J is verified.
- **Existing, already built:** `approvals.Queue`, `gate`, `audit`,
  `store` notifications (V-schema, migration 0013), `internal/needsyou`,
  and the daemon's background-loop pattern.
- **Not required:** F, G, H, I, L and N. K is buildable and testable
  with a test handler alone, in parallel with N once J exists.
- **Downstream consumers:** L (fires `contradicted` through
  `Clock.Escalate`), N (writes participant status into the record and
  bumps `version`; K reads it when a timer next fires and is never told),
  O (registers the fan-out handler, authors timers and triggers, and
  supplies reminder payloads), and P (monitor and pipeline handlers,
  which use K's retry and step timeout). These are build dependencies. At
  runtime K calls none of them and none of them calls K, except the
  synchronous escalation-sink write (§5).
