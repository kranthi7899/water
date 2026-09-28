# Slice J: durable task record, the four archetypes, and the state machine

**Planning only. No code is authorized yet.** This is one sub-slice of the
intelligence-layer redesign (F through Q). The overview and sequencing doc
for that redesign lives alongside these specs and holds the full
open-questions list. This file is the spec to build J against once the owner
approves it, following `docs/WORKFLOW.md`'s Explore → Plan → Approve →
Implement loop. J depends on nothing earlier in the redesign. It is the
foundation for the task side of the design: K (orchestrator), L (composer),
N (reply ingestion), and through them O, P and Q.

Builds on A1–A4 and V-schema, nothing later: `store` (SQLite, migrations
`0001`–`0013`, the `links` table), `audit` (the hash-chained, single-writer
log whose `audit.Entry.Seq` each task event cites), and the daemon's
authenticated Unix-socket endpoints plus the `daemonClient` CLI pattern.
Before J, Water has no notion of work that outlives a single turn. J adds
the **record** of such work and the rules for how that record may change.
It does not make anything happen: no dispatching, no waiting, no reply
handling, no model call. Nothing in J can send, post, call, delete or spend.

## Principle

Most requests never become a task. A task record exists only when the work
**outlives the turn that asked for it**: there is something to wait for, a
later step to run, or no fixed end. Everything else is a *single action*,
handled by today's turn path exactly as now, and creates no row.

When a record does exist, its state changes only through a **pure transition
table** in code. The model never writes a state directly, and an illegal move
is rejected rather than coerced. The acceptance criteria and escalation
triggers are fixed at authoring time and cannot be edited after dispatch, so
the goalposts can't move once work is out the door. Every change is appended
to a trail and to the audit log. Nothing is ever edited in place or deleted.

**The record is the shared state, not a message passed between components**
(owner amendment, 2026-09-25; see the overview doc's §2.1). The task record,
together with memory (F/G/Q), is the one durable state every task component
reads and writes: the planner (O), the clock (K), reply ingestion (N), the
structural checks, and L's drafter and verifier (the judgment critic).
**No component hands off to another directly.** None of them calls another,
waits on another's return value, or is "next" after another. Each one reads
the record when its own trigger fires (a timer, an arriving reply, a state
it watches for), writes what it knows, and stops. The version
compare-and-swap (§4) is the only coordination between them. The state
sequence in §3 is the record's lifecycle, the order the *record* passes
through, and not a chain of components passing work along. J also provides
the one read every caller shares for "where does this task stand
structurally", `Structural` (§3), so no component keeps its own copy of that
logic.

## What exists today (read 2026-09-25)

- **"task" is already a word in the code, meaning something else.** The
  daemon keeps `d.tasks map[string]context.CancelFunc` for in-flight turns
  (`internal/gateway/daemon.go`), exposed as `POST /v1/tasks/{id}/cancel`
  (`handleCancel`, "no such task" on miss). `nervous.Turn.TaskID`,
  `reflex.Deps.TaskID`/`TaskControl.CancelAllExcept` and
  `store.ThreadMessage.TaskID` (`thread_messages.task_id`, "the route_log
  row (or async task) that produced it") all mean *a turn in flight*.
  `docs/CONTEXT.md`'s P1 tier says "tasks the CEO approved or scheduled",
  and its package tree lists `scheduler/`, which does not exist in the repo.
  J must not collide with any of these (see §6, naming).
- **The `links` table** (`internal/store/links.go`) is generic typed edges,
  and new kinds or node types need no schema change. Its node-type comment
  already lists `"job"`, which nothing in code or docs defines (V's
  migration comment mentions "jobs" in passing, with no spec). J does
  **not** reuse `"job"`, because its meaning was never fixed. See the open
  questions.
- **Store conventions** J follows: application-generated ids with a short
  prefix and 12 random bytes hex (`thr_`, `ntf_`, `env_`), timestamps as
  `INTEGER` Unix nanoseconds, `TEXT NOT NULL DEFAULT ''` text columns,
  `CHECK (... IN (...))` for closed sets (`card_states.status`,
  `thread_messages.role`), and `EXPLAIN QUERY PLAN` index tests
  (`indexes_test.go`'s `queryPlan` helper).
- **Audit-before-effect** is the house ordering. `approvals.Queue.Propose`
  appends a `KindPropose` audit entry first, then inserts the store row. A
  crash between the two leaves an audit entry with no row: the log may
  over-report, and it never under-reports. `audit.Record` is
  `{Kind, Function, EnvelopeID, Origin, Allowed, Reason, ArgsHash}`, and
  `Append` returns the `Entry` with its `Seq`. `entryHash` marshals the
  whole `Entry`, so adding a field to it would change the chain format. J
  adds a `Kind` constant only.
- **The `store` package imports no domain package** (only `config`).
  Business rules live one layer up (`approvals`, `roster`, `needsyou`), and
  J follows that split.
- **CLI reads go through the daemon.** `water decisions list/show` call
  `newDaemonClient()` → an authenticated endpoint, and never open the
  database or the audit log directly. `readonly_test.go` guards that
  read-only commands work while the daemon holds the audit lock.

## Scope

### 1. The four archetypes and when a record exists

The archetype vocabulary is fixed and small. It is structure, not a workflow
per scenario:

| Archetype | Shape | Record? |
|---|---|---|
| `single_action` | Do one thing in this turn. No tracked state. | **Never.** |
| `fan_out` | Several outbound asks, track replies, decide when to proceed, synthesize. | Yes |
| `monitor` | No fixed end. Watch a defined threshold and react. | Yes |
| `pipeline` | Sequential dependent steps, nothing waiting on another person. | Yes |

**Recognizing that a request needs a record is not a classification tier.**
Whether a request fans out to people, waits on something, or is a real
multi-step pipeline is noticed by the same live reasoning that already
decides whether to call any tool: the main model, in its own turn, decides
to call O's `tasks.plan` the way it decides to call `gcal.list_events`.
There is no dedicated classifier pass or classifier model that inspects
each request first and second-guesses the main model. That would repeat the
Tier 1 mistake Slice R already retired (owner amendment, 2026-09-25).
Everything below is **structural validation in code** of what the planner
authored, not a decision about whether the request "is a task".

**How "no record" is enforced, in code and not by judgment:**

1. **Default is single action.** The turn path (`nervous.Handle` and the
   main model path) never touches the task store. J adds no call site
   there. A record can only come from an explicit `tasks.Create` call, and
   in J nothing in production calls it. O's planner is the first real
   caller.
2. **`Create` refuses `single_action`** with `ErrSingleActionNoRecord`, and
   the `task_records.archetype` column's `CHECK` excludes it, so the rule
   holds at the database level too, not only in Go.
3. **A pure function `NeedsRecord(Shape) (Archetype, bool)`** states the
   rule for callers (O) to use before calling `Create`. `Shape` is a small
   struct of structural facts, not a plan: `OutboundAsks int`,
   `DependentSteps int`, `OpenEnded bool` (a watch with no fixed end). It
   returns `fan_out` when `OutboundAsks >= 1` and the work waits on a reply,
   `monitor` when `OpenEnded`, `pipeline` when `DependentSteps >= 2`, and
   `single_action, false` otherwise. More than one true at once is an error
   (`ErrAmbiguousShape`) and never a guess. The planner in O picks the
   archetype. This function only checks that the pick is structurally
   consistent, the same way `decisions` computes `Readiness` in code
   instead of asking the model.

A single outbound ask that waits for one person's reply is structurally a
`fan_out` of one: the machinery (participant status, reminders, timeout) is
identical. This is J's reading of the brief's "multiple outbound asks", and
it is flagged for the owner to confirm at Approve (open question 1).

### 2. The record

New package `internal/tasks` holds the types, validation, the transition
table and the audited service. `internal/store/task_records.go` holds only
rows and SQL, the same split `approvals`/`store.ApprovalRow` uses.

```go
// internal/tasks/task.go
type Archetype string
const (
    SingleAction Archetype = "single_action" // valid value; never persisted
    FanOut       Archetype = "fan_out"
    Monitor      Archetype = "monitor"
    Pipeline     Archetype = "pipeline"
)

type Task struct {
    ID           string    // "tsk_" + 12 random bytes hex (see §6 on naming)
    Goal         string    // the CEO's instruction, distilled; bounded length
    Archetype    Archetype // FanOut | Monitor | Pipeline
    State        State     // §3
    Origin       Origin    // TurnID + Channel of the turn that authored it
    FanOut       *FanOutState   // exactly one of these three is non-nil,
    Monitor      *MonitorState  // matching Archetype; persisted as one
    Pipeline     *PipelineState // archetype_state JSON column
    NextAction   NextAction // "" when nothing is scheduled
    NextActionAt *time.Time // set iff NextAction != "" and State non-terminal
    Criteria     []Criterion
    Triggers     []EscalationTrigger
    OutputSchema *OutputSchema // nil until written; set once (below)
    RefinementPasses int  // verifying → synthesizing count, §3
    Version      int64    // optimistic concurrency, §4
    CreatedAt, UpdatedAt time.Time
    ClosedAt     *time.Time // set on entering a terminal state
}
```

**Archetype-specific state.** It is typed per archetype, not a free map:

```go
type FanOutState struct {
    Participants []Participant
}
type Participant struct {
    PersonID      string            // roster person source_id; "" if not on the roster
    Identity      string            // connector identity, e.g. an email address
    Status        ParticipantStatus // pending | asked | replied | deferred |
                                    // declined | delegated | no_response |
                                    // not_sent (O: the ask envelope was denied or expired)
    AskRef        string            // the approval envelope id or sent message id; a ref, never content
    ThreadRef     string            // e.g. "gmail-thread:<id>"; informational only. N correlates
                                    // replies by RFC Message-ID headers, not Gmail thread ids,
                                    // which differ across mailboxes (N's corrections, item 2)
    RemindersSent int
    LastAskedAt   *time.Time
    DelegatedTo   string            // Identity of the delegate, when Status == delegated
}

type MonitorState struct {
    Watch       string     // what is watched, as a function/metric ref, e.g. "facts.get:runway_months"
    Threshold   string     // the authoring-time condition, stored as structured text; K/P evaluate it
    LastCheckAt *time.Time
    LastValueRef string    // a ref to the last observed value, never the value's source content
    Fired       int        // how many times the reaction fired
}

type PipelineState struct {
    Steps []Step
}
type Step struct {
    ID         string
    Title      string
    DependsOn  []string    // step ids; validated acyclic and resolvable
    Reflection Reflection  // none | structural | judgment (the data shape L fills in)
    Status     StepStatus  // pending | running | done | failed | skipped
    Attempts   int
}
```

`ParticipantStatus.Terminal()` is defined here once: `replied`, `declined`,
`delegated`, `no_response` and `not_sent` are terminal, while `pending`,
`asked` and `deferred` are not. J's `Structural` read (§3) calls this, and
so does every structural check ("has every participant reached a terminal
status?"), whoever asks it. None of them keeps its own list. Whether a
delegation adds the delegate as a new participant is K/N's behavior.
J allows appending a participant, and never allows removing one.

`Reflection` on pipeline steps is only the **data shape** for marking a step
as a reflection point, so authoring has somewhere to put it. What the marks
mean and how they run is L. Choosing which steps to mark is O.

**The output schema** is the structure the delivered output is slotted
into: section order, and what each section answers (a status update and an
investor update are laid out differently). It is itself a retrievable
procedure. O retrieves one from procedural memory (Q) or has one designed
by a separate sub-agent call (L's schema designer), **in parallel with**
the fan-out/collect phase, never after it. The composer later slots content
into whatever schema the record holds (owner amendment, 2026-09-25). J
stores only the data shape:

```go
type OutputSchema struct {
    Kind     string          // a bounded label, e.g. "status_update", "investor_update"
    Sections []SchemaSection // in delivery order
    Source   string          // "procedure:<id>" (retrieved), "generated:<audit seq>"
                             // (designed), or "fallback" (code-built by L)
}
type SchemaSection struct {
    ID       string
    Title    string
    Criteria []int // criterion Ords this section answers; may be empty
}
```

It is **set once**: written after `Create` (because it is produced
concurrently with dispatch, not before it), and never changed after that.
A second write is rejected. It is not part of the frozen-after-dispatch
set, because it may legitimately arrive after dispatch. Section titles are
length-capped like criterion `Text`.

**The composed output lives in the record too.** Under the shared-state
model, L's drafter writes a draft that L's verifier (the judgment critic)
later reads, possibly after a daemon restart. The in-progress draft, its
claim references and the critic's per-criterion reasons must therefore be
durable parts of the task record, not values passed in memory from one
component to the next. Their exact storage (a J column, a J-adjacent table
keyed by task id, or a V `Thread` for the delivered copy) is still the
L-and-J coordination point in L §3.8. What the amendment settles is only
that it is in the record. The never-store rule below applies to it like any
other field: quotes are length-capped verbatim excerpts, as N stores them,
never whole bodies.

**Acceptance criteria**, generated at authoring time:

```go
type Criterion struct {
    Ord         int
    Text        string
    Critical    bool            // K's "max reminders exhausted on a critical criterion" trigger needs it
    Status      CriterionStatus // pending | met | unmet_flagged
    FlagReason  FlagReason      // "" | unmet | contradicted  (L's critic returns met/unmet/contradicted;
                                //  both non-met verdicts land in unmet_flagged with the reason kept)
    EvidenceRef string          // "<scheme>:<id>", required when Status == met
}
```

**Escalation triggers**, set at authoring time, **stored and not evaluated
in J**. The kinds are the brief's §6 list, plus the mechanical and
archetype-specific kinds the sibling specs (K, N, O, P) evaluate, as one
closed set. (The union was reconciled in the planning consistency pass, so
every sibling spec uses these exact names; see
`docs/slice-intelligence-planning.md`.)

```go
type TriggerKind string
const (
    // The brief's §6 list.
    TrigRemindersExhaustedCritical TriggerKind = "reminders_exhausted_critical" // K evaluates
    TrigRepliesConflict            TriggerKind = "replies_conflict_material"    // N detects; see O §5 on the evaluator
    TrigCriticContradicted         TriggerKind = "critic_contradicted"          // L evaluates
    TrigSensitivityMatch           TriggerKind = "sensitivity_match"            // L, N and O hooks; empty rule set until the owner decides tiers
    TrigAmbiguousInstruction       TriggerKind = "ambiguous_instruction"        // O evaluates, before dispatch
    // Time- and count-based kinds K evaluates (K §3, §5).
    TrigDeadlinePassed             TriggerKind = "deadline_passed"
    TrigStepTimeout                TriggerKind = "step_timeout"
    TrigRetriesExhausted           TriggerKind = "retries_exhausted"
    // Archetype-specific kinds.
    TrigReplyUnmatched             TriggerKind = "reply_unmatched"              // N evaluates (N §2)
    TrigReplyDeclined              TriggerKind = "reply_declined"               // O's fan-out handler (O §5)
    TrigNoAsksSent                 TriggerKind = "no_asks_sent"                 // O's fan-out handler (O §5)
    TrigProbeUnavailable           TriggerKind = "probe_unavailable"            // P's monitor handler (P §1.3)
)
type EscalationTrigger struct {
    Ord          int
    Kind         TriggerKind
    CriterionOrd *int            // for criterion-scoped kinds
    Params       json.RawMessage // kind-specific, validated per kind; no numbers are defaulted in J
    FiredAt      *time.Time      // nil in J; set by the evaluating slice via RecordTriggerFired
}
```

J validates only shape: a known kind, `CriterionOrd` present for the
criterion-scoped kinds and pointing at a real criterion, and `Params` that
parse as the kind's struct. J sets **no** thresholds or counts. Those are open
owner questions (reminder count/cadence, what makes a conflict material,
sensitivity tiers) and belong to the slice that evaluates each trigger.

**What a task record never stores.** The same rule F applies to memory
applies here: no raw email or message bodies, no raw transcripts, no
credentials, no payment details, no government ID numbers. Every
`*Ref`/`EvidenceRef` must parse as `<scheme>:<id>` with no whitespace and a
short length cap, so a pasted body fails on shape alone. `Goal`, criterion
`Text` and step `Title` have length caps. J has no dependency on F, so it
does not call F's `CheckNeverStore`. Once F exists, running it over
`Goal`/`Text` is a one-line addition for whichever slice lands second. That
is noted here and not assumed.

### 3. The state machine: a pure transition table

States, exactly the brief's set:

```
created → dispatched → awaiting_inputs → synthesizing → verifying → awaiting_ceo | delivered | escalated | abandoned
```

Terminal states: **`delivered`** and **`abandoned`**. `awaiting_ceo` and
`escalated` are parked states that wait for the CEO and are not terminal.

```go
// internal/tasks/machine.go — no I/O, no clock, no store
type Actor string // planner | orchestrator | composer | critic | reply_ingest | ceo
func Transition(t Task, to State, a Actor) error
```

`critic` is L's verifier, which writes verdicts and moves a task out of
`verifying`. It is a separate actor from `composer` (L's drafter) because
the two are independent writers of the record, and the trail should say
which one wrote what.

The state sequence above is the order the **record** passes through. It is
not an order of components handing work to each other. Each edge is
written by whichever independent writer observes, from the record, that
the edge's condition holds (overview §2.1).

`Transition` is a pure function of `(archetype, from, to, actor, task
contents)`. It returns a typed `*IllegalTransitionError{From, To, Archetype,
Actor, Rule}` and never mutates anything. The legal edges:

| From | To | Allowed for | Notes |
|---|---|---|---|
| `created` | `dispatched` | all | |
| `created` | `awaiting_ceo` | all | Ask-before-dispatch: an ambiguous instruction is asked about *before* anything goes out (brief §6). |
| `created` | `abandoned` | all | A plan that fails validation before anything was sent. Any actor may do this, because nothing outward has happened yet. |
| `dispatched` | `awaiting_inputs` | fan_out, monitor | Monitor's "watching" state is `awaiting_inputs`. |
| `dispatched` | `synthesizing` | pipeline | Steps run while `dispatched`, and the final composition starts here. |
| `awaiting_inputs` | `synthesizing` | fan_out, monitor | Monitor only when its stop condition calls for a report. |
| `synthesizing` | `verifying` | all | |
| `verifying` | `synthesizing` | all | A refinement pass. It increments `RefinementPasses` and is **rejected once that reaches `MaxRefinementPasses`** (2, the top of the brief's decided 1–2 cap; L may lower it and may not raise it). |
| `verifying` | `awaiting_ceo` | all | The normal route for anything needing CEO review. |
| `verifying` | `delivered` | all | **Rejected while any criterion is `pending`.** Delivery must mark every criterion `met` or `unmet_flagged`, so gaps are flagged explicitly and never silently padded (brief §6). |
| any non-terminal except `created` | `escalated` | all | |
| `awaiting_ceo` | `dispatched`, `awaiting_inputs`, `synthesizing`, `delivered`, `abandoned` | all | **Actor must be `ceo`.** `delivered` is still subject to the no-pending-criteria rule. |
| `escalated` | `dispatched`, `awaiting_inputs`, `synthesizing`, `abandoned` | all | **Actor must be `ceo`.** The twin cannot clear its own escalation. |
| any non-terminal except `created` | `abandoned` | all | **Actor must be `ceo`.** Once something may have gone out, only the CEO can drop the task. |

Everything else is illegal: leaving a terminal state, self-loops, skipping
`verifying` on the way to `delivered` (for every archetype, monitor
included), and any edge where the archetype or actor doesn't match. Per-participant or per-step progress is
**not** a state transition. It is an archetype-state update (§4) inside
`awaiting_inputs` or `dispatched`. A monitor's routine reaction (a
notification) likewise stays in `awaiting_inputs` and increments `Fired`.
It does not bounce through `escalated`.

Three rules in that table are J's own design choices, not stated in the
brief. They are flagged here so the owner sees them at Approve:

- **Only the CEO leaves `awaiting_ceo`/`escalated`, and only the CEO
  abandons a dispatched task.** This follows brief §6 (the model never
  decides "keep waiting", and models are bad at judging their own
  escalation), applied to the exits as well as the entries.
- **`delivered` is terminal.** The late-reply policy (amend, send a delta,
  or hold for next cycle) is an open owner question. J adds no
  `delivered → *` edge, so any of the three answers can be added later
  without first removing a wrong one.
- **A monitor's normal stop is `delivered`** (it reported on its stop
  condition), and a CEO stop is `abandoned`. P owns the monitor stop rule
  and may ask for a distinct terminal state. J does not invent one.

**The structural read.** "Has everyone replied or timed out?", "are any
criteria still pending?" and "has a content trigger fired?" are questions
about the record, not stages of a pipeline. J answers them with one pure
function that any component calls at the moment it needs the answer:

```go
// internal/tasks/structural.go — no I/O, no clock, no model
type StructuralState struct {
    ParticipantsTotal    int
    ParticipantsTerminal int      // counted with ParticipantStatus.Terminal()
    Outstanding          []string // participant subject ids not yet terminal
    CriteriaPending, CriteriaMet, CriteriaFlagged int
    FiredTriggers        []TriggerKind // triggers with FiredAt set
    SchemaReady          bool          // OutputSchema != nil
}
func Structural(t Task) StructuralState
```

It runs after nothing and before nothing. The clock's fan-out handler
reads it when its own timer fires, to decide whether to proceed (K §3, O
§6). L's verifier reads it to check readiness before judging (L §3.8).
`water tasks show` renders it. None of these callers waits for another to
"run the check" first, and none of them keeps a private version of it.

### 4. The store and the audited service

**Migration** `00NN_task_records.sql`. The number is the next free one at
commit time (`0014` today), and is **not** reserved here, because concurrent
sessions collided on migration numbers twice during Slice R. Four tables:

```sql
CREATE TABLE task_records (
    id TEXT PRIMARY KEY,
    goal TEXT NOT NULL,
    archetype TEXT NOT NULL CHECK (archetype IN ('fan_out', 'monitor', 'pipeline')),
    state TEXT NOT NULL CHECK (state IN ('created', 'dispatched', 'awaiting_inputs',
        'synthesizing', 'verifying', 'awaiting_ceo', 'delivered', 'escalated', 'abandoned')),
    archetype_state TEXT NOT NULL DEFAULT '{}',
    origin_turn_id TEXT NOT NULL DEFAULT '',
    origin_channel TEXT NOT NULL DEFAULT '',
    next_action TEXT NOT NULL DEFAULT '',
    next_action_at INTEGER,
    output_schema TEXT NOT NULL DEFAULT '',   -- JSON OutputSchema; '' until set, then set once
    refinement_passes INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    closed_at INTEGER
);
-- K polls "due now": a partial index so the scan never touches idle/closed rows.
CREATE INDEX task_records_due ON task_records (next_action_at) WHERE next_action_at IS NOT NULL;
CREATE INDEX task_records_state ON task_records (state, updated_at);

CREATE TABLE task_criteria (
    task_id TEXT NOT NULL REFERENCES task_records(id),
    ord INTEGER NOT NULL,
    text TEXT NOT NULL,
    critical INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL CHECK (status IN ('pending', 'met', 'unmet_flagged')),
    flag_reason TEXT NOT NULL DEFAULT '' CHECK (flag_reason IN ('', 'unmet', 'contradicted')),
    evidence_ref TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (task_id, ord)
);

CREATE TABLE task_triggers (
    task_id TEXT NOT NULL REFERENCES task_records(id),
    ord INTEGER NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('reminders_exhausted_critical', 'replies_conflict_material',
        'critic_contradicted', 'sensitivity_match', 'ambiguous_instruction',
        'deadline_passed', 'step_timeout', 'retries_exhausted',
        'reply_unmatched', 'reply_declined', 'no_asks_sent', 'probe_unavailable')),
    criterion_ord INTEGER,
    params TEXT NOT NULL DEFAULT '{}',
    fired_at INTEGER,
    PRIMARY KEY (task_id, ord)
);

-- The append-only trail. audit_seq ties every row to its hash-chained audit entry.
CREATE TABLE task_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id TEXT NOT NULL REFERENCES task_records(id),
    at INTEGER NOT NULL,
    actor TEXT NOT NULL,
    kind TEXT NOT NULL,        -- created | transition | criterion | archetype_state |
                               -- next_action | trigger_fired
    from_state TEXT NOT NULL DEFAULT '',
    to_state TEXT NOT NULL DEFAULT '',
    detail TEXT NOT NULL DEFAULT '{}',  -- refs and statuses only, never content
    audit_seq INTEGER NOT NULL
);
CREATE INDEX task_events_task ON task_events (task_id, id);
CREATE TRIGGER task_events_no_update BEFORE UPDATE ON task_events
    BEGIN SELECT RAISE(ABORT, 'task_events is append-only'); END;
CREATE TRIGGER task_events_no_delete BEFORE DELETE ON task_events
    BEGIN SELECT RAISE(ABORT, 'task_events is append-only'); END;
```

No migration in the repo uses SQLite triggers yet. They are proposed because
"append-only" should hold against a stray `UPDATE`, not just against the Go
API's lack of one. If the owner prefers API-only enforcement, drop them at
Approve. The test in §Tests then becomes an API-surface check.

**`internal/store/task_records.go`** holds rows and SQL only: insert a record
with its criteria and triggers in one transaction, a compare-and-swap update
(`UPDATE ... WHERE id = ? AND version = ?`, returning `ErrVersionConflict` on
zero rows), `GetTaskRecord`, `ListTaskRecords(filter)`, `ListTaskEvents(id)`,
and `DueTaskRecords(now, limit)`
(`WHERE next_action_at IS NOT NULL AND next_action_at <= ? ORDER BY
next_action_at`). `DueTaskRecords` is the read primitive K polls. J builds
and index-tests it but runs no loop over it. The file has no update or delete
method for `task_events` and no delete method for anything.

**`internal/tasks.Service`** is the only writer, and it wraps `*store.Store`
plus `*audit.Log`:

```go
func (s *Service) Create(ctx context.Context, t Task, a Actor) (Task, error)
func (s *Service) Move(ctx context.Context, id string, version int64, to State, a Actor, reason string) (Task, error)
func (s *Service) SetCriterion(ctx context.Context, id string, version int64, ord int, st CriterionStatus, fr FlagReason, evidenceRef string, a Actor) (Task, error)
func (s *Service) UpdateArchetypeState(ctx context.Context, id string, version int64, fn func(*Task) error, a Actor) (Task, error)
func (s *Service) Schedule(ctx context.Context, id string, version int64, next NextAction, at time.Time, a Actor) (Task, error)
func (s *Service) RecordTriggerFired(ctx context.Context, id string, version int64, ord int, a Actor) (Task, error)
func (s *Service) SetOutputSchema(ctx context.Context, id string, version int64, sch OutputSchema, a Actor) (Task, error) // rejected if already set
func (s *Service) Get(ctx context.Context, id string) (Task, []Event, error)
func (s *Service) List(ctx context.Context, f Filter) ([]Task, error)
```

Every mutating method does the same four things in this order:

1. **Validate** in pure code: `Task.Validate()`, `Transition()` for `Move`,
   and the frozen-after-dispatch rule below.
2. **Append the audit entry first**, the same ordering as
   `approvals.Queue.Propose`. It uses a new `audit.KindTask Kind = "task"`
   and the existing `Record` fields only, with no chain-format change:
   `Function` is `"tasks.<event kind>"`, `Origin` is the actor, `Reason` is
   `"<task id> <from>→<to>: <reason>"` or the equivalent for non-transition
   events, and `ArgsHash` is `canon.Hash` of the event's `detail`.
3. **Commit the store change and the `task_events` row in one SQL
   transaction**, with the returned `Entry.Seq` as `audit_seq`, guarded by
   the version compare-and-swap.
4. Return the fresh `Task`.

A version conflict (K and N racing on the same task later, or any two of
the independent writers) returns `ErrVersionConflict` for the caller to
re-read and retry. It never overwrites. This compare-and-swap is the only
coordination between the task components: they share the record, not
calls to each other. An audit append failure fails the call before any store change,
matching `audit`'s own "a failed write fails the action that caused it".

**Frozen after dispatch.** Once a task leaves `created`, the criteria *set*
(count, order, `Text`, `Critical`) and the triggers *set* (except `FiredAt`)
cannot change. Only criterion status, flag reason and evidence change after
that. The planner authors the goalposts, and nothing later moves them. A
real change of goal is a new task. Participants may be appended in
`fan_out`, and never removed. `OutputSchema` is outside this rule: it is
set once whenever it arrives (§2), because it is designed concurrently with
dispatch rather than before it.

**Invariants `Task.Validate()` enforces**, whatever path wrote the row: the
archetype matches exactly one non-nil archetype state; `NextActionAt` is set
iff `NextAction != ""`; terminal states have no `NextAction` and have
`ClosedAt` set (`Move` into a terminal state clears one and sets the other);
`met` requires `EvidenceRef`, and `unmet_flagged` requires a `FlagReason`;
the pipeline `DependsOn` graph is acyclic and resolvable; there is at least one
criterion; and every ref has the `<scheme>:<id>` shape. The same `Validate`
runs **on read**, so a hand-edited row fails loudly rather than being served.

**`NextAction`** is a string from a closed set declared in one place
(`next_action.go`). J declares the obvious mechanical steps (`dispatch`,
`remind`, `check_inputs`, `timeout`, `check_threshold`, `run_step`) so K
has a fixed vocabulary to extend in its own slice, rather than a free
string K could misspell. J attaches no behavior to any of them. Earlier
drafts also listed `synthesize` and `verify`. They are dropped, because
under the shared-state model the clock never schedules composition or
verification: L's drafter and verifier notice a task in `synthesizing` or
`verifying` by reading the record themselves (L §3.8), so there is nothing
for a timer to fire.

**Links.** On `Create` of a `fan_out` task, one `LinkInvolves` edge
(`task` → `person`) is written per participant who has a roster
`PersonID`, so "what is outstanding with Priya?" can later join through
existing roster queries. `"task"` is added to `links.go`'s node-type list.
`"job"` is left alone (open question 5).

### 5. Read-only surface: `water tasks list/show`

- **Endpoints** (authenticated, read-only, same `d.auth` wrapper as
  `/v1/decisions`): `GET /v1/task-records?state=&archetype=&open=` and
  `GET /v1/task-records/{id}` (the record plus its `task_events` trail).
  They are deliberately **not** under `/v1/tasks/`, because
  `POST /v1/tasks/{id}/cancel` already means "cancel an in-flight turn"
  (§6). J adds no POST, PUT or DELETE route for task records.
- **CLI**: `water tasks list [--state s] [--archetype a] [--all]` (open tasks
  by default, one code-built line each: id, archetype, state, next action
  and when, criteria met/total, goal truncated) and `water tasks show <id>`
  (full code-built render: goal, state, archetype state, criteria with
  status and evidence refs, triggers and whether fired, then the event
  trail with audit seqs). Both go through `newDaemonClient()` like
  `water decisions`, and neither opens the database or audit log itself.
  Render is `Task.Render() string`, built in code, with no model phrasing,
  the same posture as `Card.Render()` and `approvals.ReadBack`.
- In J's own build, the list is **always empty in production**, because
  nothing calls `Create` until O. The endpoints and CLI are exercised
  through tests that seed records via `Service.Create`. This is the same
  honest-stub posture as C-base's registry-only types.

### 6. Naming: "task" is already taken

The brief says "task record", and the design keeps that word in prose and
in the package name `internal/tasks`. Where it would collide with the
existing turn-task meaning, J namespaces instead:

- ids are `tsk_…`, so they can never be mistaken for a turn task id or
  passed to the existing cancel route by accident;
- the tables are `task_records`/`task_criteria`/`task_triggers`/`task_events`;
- the HTTP path is `/v1/task-records`, not `/v1/tasks`;
- the CLI is `water tasks`, where there is no existing command to collide
  with.

If the owner would rather rename the concept outright (for example "job",
which `links.go` already lists undefined, or "work item"), that is a
find-and-replace at Approve, before any code exists (open question 5).

## Do not build in J

- **Anything that makes a task move by itself.** No ticker, no due-work
  loop, no retries, reminders, timeouts or escalation-on-timeout. That is
  **K**. J ships `DueTaskRecords` and nothing that calls it.
- **Evaluating any escalation trigger**, or surfacing anything through
  `needsyou`/notifications. Triggers are stored only. K, L, N and O evaluate
  their own kinds.
- **Any outward action.** No envelope is proposed and no connector is called.
  Nothing in J reaches `gate.Invoke`.
- **Reply ingestion or thread correlation** (**N**), **synthesis, the
  draft-level structural checks, the schema designer and the critic**
  (**L**), **planning, archetype choice and criteria generation** (**O**),
  and **monitor/pipeline behavior** (**P**). J provides only the
  record-level `Structural` read (§3) those components share.
- **Model-callable task tools** (a `tasks.*` connector with `twin.yaml`
  grants). Whether the model ever gets one, and at what level, is decided
  in O. J gives the model no way to read or write tasks.
- **Procedural-memory lookup or promotion** from the event trail (**Q**).
  J's trail is shaped so Q can later read "what actually happened", but J
  does not read it for that purpose.
- A workflow-engine dependency of any kind. J needs none, and K's spec
  addresses the question for the orchestrator.

## Tests

The implementing session runs `go vet ./...`, `go test -count=1 ./...` and
`CGO_ENABLED=0 go build ./cmd/water` before every commit, as always. This
planning doc runs none of them.

- **Transition table, exhaustively.** A table-driven test over every
  `(archetype, from, to, actor)` combination, 3 × 9 × 9 × 6, asserting it
  matches the §3 table exactly. It lists the legal set explicitly, so a new
  edge added later without updating the test fails.
- **Illegal moves are rejected, not coerced.** Leaving a terminal state,
  `verifying → delivered` with a `pending` criterion, a third
  `verifying → synthesizing` pass, a non-CEO actor leaving `awaiting_ceo` or
  `escalated`, and a non-CEO actor abandoning a dispatched task each return
  `*IllegalTransitionError` naming the rule. The stored row, its version and
  `task_events` are unchanged afterwards.
- **Single action creates nothing.** `Create` with `SingleAction` returns
  `ErrSingleActionNoRecord` and writes no row and no audit entry. A raw
  `INSERT` with `archetype = 'single_action'` fails the `CHECK`.
  `NeedsRecord` is table-tested over `Shape` fixtures, including the
  ambiguous-shape error. A guard test asserts that no package under
  `internal/nervous` or `internal/runtime` imports `internal/tasks`, so the
  default turn path cannot start creating records by accident.
- **Validation.** Every §4 invariant has a failing fixture (a mismatched
  archetype state, `NextActionAt` without `NextAction`, `met` with no
  evidence, a cyclic pipeline, zero criteria, a whitespace-containing ref, a
  goal over the cap), and a hand-corrupted row fails on `Get`.
- **Frozen after dispatch.** After `created → dispatched`, changing
  criterion text, adding or removing a criterion, or editing a trigger's
  params is rejected. Setting criterion status/evidence and appending a
  `fan_out` participant is accepted, and removing a participant is rejected.
- **Audit ties in.** Every mutating call produces exactly one `KindTask`
  audit entry and exactly one `task_events` row whose `audit_seq` equals that
  entry's `Seq`. `audit.Verify` passes afterwards. An injected audit-append
  failure leaves the store untouched. An injected store failure after a
  successful append leaves an audit entry with no event row, which is the
  accepted over-report direction, asserted explicitly.
- **Append-only trail.** A direct `UPDATE` or `DELETE` on `task_events`
  fails (the SQLite triggers). The store package exposes no method that
  mutates or deletes an event row (an API-surface check).
- **Concurrency.** Two `Move` calls with the same starting version: exactly
  one wins and the other returns `ErrVersionConflict`. Parallel
  `SetCriterion` calls on different tasks all land.
- **Structural read.** `Structural` is table-tested over fan-out fixtures
  (every participant status, criteria in each status, fired and unfired
  triggers, schema present or absent). It is a pure function: calling it
  twice on the same `Task` gives equal results, and it makes no store call.
- **Output schema, set once.** `SetOutputSchema` succeeds on a task in any
  non-terminal state, including after dispatch, and a second call is
  rejected with the stored schema unchanged. A schema whose section cites a
  nonexistent criterion `Ord` fails `Validate`.
- **Due-work index.** `DueTaskRecords`' query uses `task_records_due` with
  no full-table scan (via `indexes_test.go`'s `queryPlan` helper). Terminal
  and unscheduled tasks never appear in it.
- **Round trip.** One task of each persisted archetype, with criteria,
  triggers and archetype state, round-trips field for field through
  `Create`/`Get`.
- **Read-only surface.** `GET /v1/task-records` and
  `GET /v1/task-records/{id}` require auth, and render seeded records and
  their trail. No mutating route exists under `/v1/task-records`. The
  existing `POST /v1/tasks/{id}/cancel` behaves exactly as before (a
  regression test with a `tsk_` id returns "no such task").
  `water tasks list/show` work while another process holds the audit lock,
  in the style of `readonly_test.go`, and `Task.Render()` output is
  golden-tested.
- **Never content.** No column in any of the four tables can hold a string
  over its cap, and `detail` JSON carries refs and statuses only. A fixture
  that tries to put an email body in `EvidenceRef` or `AskRef` is rejected
  on shape.

## Dependencies

- **Depends on:** nothing earlier in this redesign. It uses only `store`
  (migration runner, `links`), `audit` (a new `Kind` constant, no format
  change) and `canon`, all of which already exist.
- **Blocks:** **K** (it polls `DueTaskRecords` and drives `Move`/`Schedule`),
  **L** (its drafter and verifier read criteria, the output schema and
  pipeline `Reflection` marks, write the draft, criterion verdicts and the
  `synthesizing`/`verifying` transitions, and call `Structural`), and **N**
  (it writes participant status through `UpdateArchetypeState`). O, P and
  Q are blocked transitively. These are build-order dependencies. At
  runtime none of these slices calls another: each reads and writes this
  record independently.
- **Independent of:** **F**, **G**, **H** and **I**. J can be built before,
  after or alongside any of them without touching their files, except a
  migration-number choice at commit time if F also uses SQLite, and the
  optional one-line `CheckNeverStore` call noted in §2 once F exists.
- **No new dependency.** Standard library plus the already-approved
  `modernc.org/sqlite`.

## Open for the owner (J's slice only; the full list is in the overview doc)

These are left undecided on purpose. The implementation must not pick
defaults for them silently.

1. **A single ask that waits on one reply**: `fan_out` of one (J's
   proposal, §1), or something else? This decides whether "ask Dana for the
   Q3 number and tell me when she answers" creates a record.
2. **The three transition-table choices flagged in §3**: CEO-only exits
   from `awaiting_ceo`/`escalated`, CEO-only abandon after dispatch, and a
   monitor's normal stop landing in `delivered`. Confirm or amend at Approve.
3. **`delivered` stays terminal until the late-reply policy is decided.**
   This is J's piece of the global question (amend, send a delta, or hold
   for next cycle). The answer may add a `delivered → …` edge in K or L.
   J adds none.
4. **Trigger parameters.** Reminder counts, what counts as a material
   conflict, sensitivity tiers and any confidence threshold stay unset in
   J. `Params` holds whatever the owner decides, per kind, once decided.
5. **Naming.** Keep "task" with the §6 namespacing, or rename the concept.
   Relatedly, what `links.go`'s already-listed but undefined `"job"` node
   type was meant to be, and whether this is it.
6. **Retention.** Closed task records and their event trails are kept
   indefinitely in J. Whether they are ever archived or compacted is
   undecided. The audit log's own entries are never removed regardless.
7. **SQLite triggers for append-only** (§4): keep them, or rely on API-only
   enforcement.

Source-brief note: §4 of the redesign brief reads cleanly after the
coordinating session's reconstruction, and I found no remaining garbled
phrase in the part J covers. Two things are gaps rather than corruption, and
are recorded above instead of guessed at. The brief lists the four end
states without saying which are terminal or who may leave them (§3 and open
question 2). It also says "multiple outbound asks" for fan-out without
covering a single ask that waits (open question 1).
