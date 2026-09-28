# Slice P: the monitor and pipeline archetypes

**Planning only. Do not implement until Slices O and I are built and
verified.** This spec authorizes no code yet. It is one sub-slice of the
intelligence-layer redesign (F through Q). The redesign's overview doc holds
the ordering and the full open-questions list. Build it with
`docs/WORKFLOW.md`'s Explore → Plan → Approve → Implement loop, and re-read
every file named below at build time. J, K, L, N and O will all have changed
the code by then, and several points below are marked "confirm against the
built K/O" because they depend on interfaces that exist only as specs today.

Covers brief §4 (the remaining two of the four archetypes) and §6 (the
orchestration clock and authoring-time triggers, as they apply to these two).

Builds on: J (the task record, `MonitorState`/`PipelineState`, the
transition table, the `check_threshold`/`run_step` next actions), K
(`internal/taskclock`: the due-work loop, `task_timers`, the `Handler`
interface, retry with backoff, `step_timeout`, two-phase outward staging,
and the single escalation sink `Clock.Escalate`), L (the composer, the
structural and judgment reflection points, and the capped refinement
passes), O (the planner, its plan validator, ask-before-dispatch, and the
fan-out handler that P's two handlers sit next to), and I (`facts.get`, the
sourced metric ledger a monitor watches). It also uses machinery that
already exists: `gate` (levels, origins, rate caps, `authorize`'s "P2 never
reaches level A"), `approvals` (`Queue.Propose`, envelope statuses
`pending`/`approved`/`executed`/`expired`, `DefaultTTL` of 15 minutes),
`store.InsertNotificationIfNew` (V-schema), and `internal/needsyou`.

Before P, a task can be a fan-out and nothing else. O's planner can only say
"ask these people, collect, synthesize". P adds the other two structural
shapes the brief decided on: a watch with no fixed end, and a chain of
dependent steps that waits on no one but the CEO's approvals. P adds **no
new archetype, no new clock and no new composer.** Both archetypes are a
handler on K and a plan shape O validates. Where authoring marked a
reflection point or a `compose` step, the task record says so, and L's
drafter and verifier pick that work up from the record themselves (L
§3.8). P's handler never calls into L.

**Shared state, not a hand-off chain** (owner amendment, 2026-09-25; J's
Principle, the overview's §2.1). A pipeline's steps are genuinely
sequential. The brief defines the archetype that way, because each step
depends on the one before, and that stays. What changed is how the
*components* that run those steps interact. P's handler, K's clock and L's
drafter and verifier never pass a step to each other. Each reads the
task record and writes its part: the handler marks a step `running`, the
component that owns that step kind does the work and writes the result
into the record, and the handler sees the result the next time one of its
own timers fires.

## Principle

The brief's two decided rules apply here unchanged:

- **The timer owns time, never the model.** A monitor's probe schedule, a
  pipeline step's timeout and its retries are K timers. No model call
  decides "check again" or "keep waiting".
- **Reacting is never acting.** A monitor that sees its threshold crossed
  may notify the CEO, escalate, or *stage* an approval envelope that was
  authored at planning time. It never performs an outward action itself. A
  pipeline's outward step is always an envelope the CEO approves.

Two rules are specific to P:

- **A threshold is evaluated in code, never by the model.** A monitor's
  condition is a structured predicate over a sourced value (a `facts.get`
  fact, or a count from an R-level list function), evaluated by a pure Go
  function. A routine probe makes **zero** model calls. This is what makes
  an open-ended watch affordable on a subscription with usage caps.
- **Every monitor has a defined end.** The brief requires a stop/abandon
  rule. P makes it a validation rule: a monitor plan without one is rejected
  before a task record is created.

## What exists today, and what the sibling specs fix (read 2026-09-25)

- **J's `MonitorState`** is `{Watch, Threshold, LastCheckAt, LastValueRef,
  Fired}`. `Threshold` is "structured text; K/P evaluate it". P defines that
  structure (§1.1). J's table already routes a monitor as `dispatched →
  awaiting_inputs` (watching), keeps a routine reaction inside
  `awaiting_inputs` with `Fired` incremented, and says "a monitor's normal
  stop is `delivered`" and that "P owns the monitor stop rule and may ask
  for a distinct terminal state". P does **not** ask for one (§1.4).
- **J's `PipelineState`** is `{Steps []Step}`, and each `Step` has
  `DependsOn`, `Reflection` (none | structural | judgment), `Status`
  (pending | running | done | failed | skipped) and `Attempts`. J validates
  `DependsOn` as an acyclic graph. The brief says "sequential dependent
  steps", so P runs steps **one at a time in a topological order**, and
  never in parallel (§2.1). The pipeline route in J's table is `dispatched
  → synthesizing`: steps run while the task is `dispatched`.
- **J's `NextAction` vocabulary** already declares `check_threshold` and
  `run_step`. P attaches behavior to both.
- **J's trigger kinds.** When P was drafted, J's closed set (`CHECK` in
  `task_triggers`) held only the brief's five kinds, and K's spec evaluated
  `deadline_passed`, `step_timeout` and `retries_exhausted`, which were not
  in it. **P found this inconsistency between the J and K specs.** The
  planning consistency pass has since widened J's spec to the union,
  including P's new kind `probe_unavailable` (§1.3). P still checks the
  built set in its first task: if the built J lacks any of `step_timeout`,
  `retries_exhausted` or `probe_unavailable`, adding them is an additive
  migration that widens the `CHECK`, plus J's exhaustive transition and
  validation tests updated to match.
- **K's `Handler.OnTimer` is pure** ("no I/O, no model call"). A monitor
  probe and a pipeline step both need I/O: a `gate.Invoke`. K's spec speaks
  of "a dispatched mechanical step" that reports completion, and of retries
  applying to "mechanical steps whose function is level R or D", but its
  `Outcome` struct has no field for running one. **This is an interface gap
  in the K spec**, flagged rather than papered over. P's plan phase must
  confirm against the built K. If K has a step-execution hook, P uses it. If
  not, P adds the smallest one to `internal/taskclock`: an `Executor`
  interface that K calls outside the state transaction, whose result is then
  passed to the pure handler. P does **not** start a second loop of its own.
  The seam covers **mechanical** gate calls only (`read` and `draft` steps,
  and probes), which are time-and-retry work K already owns. It never runs
  a `compose` step or a judgment point. Those are model work, and L's
  drafter and verifier pick them up from the record (§2.1, §2.5), so the
  clock never hands work to the composer.
- **K's `Clock.Escalate`** moves a task to `escalated` and writes a
  notification with `RecordID = taskID + ":" + triggerID`. That fits a
  monitor's `probe_unavailable` and a pipeline's `retries_exhausted`. It
  does **not** fit a monitor's routine breach reaction, which J keeps inside
  `awaiting_inputs`. Routing a breach through `Escalate` would park the
  monitor in `escalated` and stop it watching. So P needs a
  non-escalating notification path (§1.2).
- **`store.InsertNotificationIfNew`** dedupes on `(record_type, record_id)`
  *forever*. A monitor that crosses its threshold, recovers and crosses
  again must be able to notify twice, so the breach record id carries the
  breach ordinal (§1.2).
- **`needsyou.Compute`** lists decisions and approvals, and K adds tasks in
  `escalated`/`awaiting_ceo`. A monitor in `awaiting_inputs` that just fired
  a breach notification would not appear in Today. P must decide whether it
  should (open point 6). The notification itself is stored either way.
- **Gate origins.** `gate.authorize` denies P2 anything not on
  `auto_allowlist` (today only `gcal.list_events`, `gmail.list_messages`,
  `agentmail.list_messages`) and anything above level D. K's spec runs
  task-driven gate calls at **P1** ("a task the CEO approved or scheduled"),
  which today gets no treatment distinct from P0 beyond validity, so it is
  not bound by `auto_allowlist`. I's spec left `facts.get` off
  `auto_allowlist` and wrote "background use is P's business". P adopts K's
  P1 convention and compensates in its own validator: a monitor's probe
  must be level R (§1.1). Whether background probes should instead be P2
  and pass through `auto_allowlist` is an owner question (open point 3).
- **Manifest rate caps** matter for an open-ended watch. Every R function
  in `twins/ceo/twin.yaml` is capped at 60 or 120 calls per hour, and the
  cap is shared with the CEO's own live use. A monitor probing every minute
  would eat half of `company_finance.cash_position`'s 60/hour.
- **Envelope expiry.** `approvals.DefaultTTL` is 15 minutes and
  `ExpireStale` treats silence as a no. K already sets `ExpiresAt`
  explicitly on the envelopes it stages. P does the same for a monitor's
  staged reaction and for a pipeline's outward step.
- **Config.** `internal/config`'s `retiredPrefixes` silently ignores
  `orchestration.*`, `skills.*` and `memory.*`. K put its keys under
  `tasks.clock.*`. P's go under `tasks.monitor.*` and `tasks.pipeline.*`.

## Scope

### 1. The monitor archetype

#### 1.1 The watch and the threshold: a closed, structured predicate

`MonitorState.Watch` and `MonitorState.Threshold` get a fixed shape, parsed
and validated in code. P's first cut supports exactly two value sources:

```go
// next to O's fan-out handler (internal/tasks/fanout); proposed
// internal/tasks/monitor/monitor.go (confirm the layout against the built O)
type Watch struct {
    Kind     WatchKind // "fact" | "count"
    Function string    // "facts.get" for fact; an R-level list function for count
    Args     map[string]any // authored at planning time; schema-checked against the function
    Metric   string    // fact only: a facts.Catalog() id, e.g. "cash.runway_months"
    Period   string    // fact only; "current" is the only period I ships
}

type Threshold struct {
    Op    string  // "lt" | "le" | "gt" | "ge" (no "eq" on floats)
    Value float64
    Unit  string  // must equal the fact's Unit; ignored for count
}
```

- **`fact`** reads `facts.get(metric, period)` through `gate.Invoke` and
  compares `Fact.Value`. The metric must be in I's catalog at planning time,
  and the unit must match. Today that means the three `cash.*` metrics.
- **`count`** calls an R-level list function (e.g. `hubspot.list_deals`,
  `linear.list_issues`, `gmail.list_messages` with a fixed query) and
  compares the number of returned records. It never reads a field inside a
  record. A JSONPath-style extractor over arbitrary connector output is out
  of scope (open point 2).
- **Evaluation is a pure function** `Eval(w Watch, th Threshold, result)
  (Observation, error)`, where `Observation` is `breached | clear |
  unknown`. The model is never asked whether a threshold was crossed.
- **`unknown` is not `clear`.** I's `facts.get` returns `not_available` or
  `no_data`, and flags `stale` and `zero_may_mean_missing`. A probe whose
  fact is not `ok`, or carries either of those flags, observes `unknown`.
  A gate denial, a rate-cap refusal or a connector error also observes
  `unknown`. So does a `count` probe that hit a connector page limit, since
  the true count may be higher. Only a clean, fresh, sourced value can be
  `breached` or `clear`. This carries I's "never launder a missing cell
  into a number" rule into the watch.
- `LastValueRef` stores a ref only (for a fact, `<Source>:<SourceID>@<AsOf>`,
  and for a count, the audit seq of the probe call), per J's never-content
  rule. The observed number goes into the task event's `detail` as a
  status, the same way J records statuses and not content.

#### 1.2 Reacting: edge-triggered, never an outward action

A monitor reacts on the **transition** from `clear` to `breached`, not on
every probe that is still breached. It re-arms after one `clear` probe.
This is the proposed default: edge-triggered with a one-probe re-arm. A
level-triggered or hysteresis-band alternative is open point 4.

Authoring (O's planner, extended by P) picks one reaction per monitor from
a closed set:

| reaction | what it does | state change |
|---|---|---|
| `notify` | one CEO notification, code-built text: what was watched, the value, the source ref and as-of, the threshold | none; stays `awaiting_inputs`, `Fired++` |
| `escalate` | `Clock.Escalate` with the breach as the reason | `awaiting_inputs → escalated` (CEO must act) |
| `stage` | one approval envelope whose action and payload were authored at planning time, plus a `notify` | none; stays `awaiting_inputs`, `Fired++` |

- **`notify` has its own path, not `Clock.Escalate`.** P adds a
  non-escalating notify alongside K's sink (proposed: `Clock.Notify(ctx,
  taskID, recordSuffix, title, body)`, in `internal/taskclock`, so there is
  still one place that writes task notifications). It calls
  `store.InsertNotificationIfNew` with `RecordType: "task"` and `RecordID:
  taskID + ":breach:" + Fired`. The ordinal is what lets a second,
  distinct breach notify, while a crash-and-retry of the same breach does
  not notify twice.
- **`stage` never interpolates probe data into the outward payload.** The
  payload is fixed when the plan is validated, and it reaches the CEO for
  approval exactly as authored. The observation (value, source, as-of) goes
  into the envelope's `EvidenceRefs` and the code-built read-back only. So
  external data can never write the words of an outward message, and a
  tainted probe cannot turn into an untainted send. The envelope's
  `ExpiresAt` is set explicitly from config, as K does for reminders. An
  expired or rejected staged reaction is recorded and is **not**
  re-proposed. It counts as that breach's reaction having happened.
- Staging uses K's two-phase staging (`stage_pending`, then `Propose`, then
  record `envelope_id`), so a crash never produces two envelopes for one
  breach.
- **No model call on a breach.** The notification and read-back text are
  built in code. A model-phrased breach summary is out of scope (see "Do
  not build in P").

#### 1.3 Probing on the clock

- A monitor's probe is a K `wait` timer on the subject `probe`, with
  `NextAction = check_threshold`. After each probe, the handler schedules the
  next one at `now + interval`. K's coalescing rule already guarantees that
  a daemon that was down for ten intervals probes **once** on restart and
  does not replay the ten it missed.
- A probe that errors is **not** retried with K's backoff. The next
  scheduled probe is the retry. That keeps one monitor from multiplying its
  own call rate against a shared rate cap.
- **`probe_unavailable`.** After `tasks.monitor.max_consecutive_unknown`
  consecutive `unknown` observations, the handler calls `Clock.Escalate`
  with the new trigger kind `probe_unavailable`. A watch that cannot see is
  surfaced, never left silently "clear". The count is stored in the monitor
  state. Proposed default: 3, provisional like every number in this
  redesign.
- Probes run at **Origin P1** with **Taint Clean** on the call, K's
  convention for CEO-initiated tasks. The result's `Untrusted` flag is kept
  on the observation and in the event detail.

#### 1.4 The stop rule, and where a monitor ends

The plan validator rejects a monitor without `Stop.Until`. The `Stop` shape:

```go
type Stop struct {
    Until          time.Time // required; hard end of the watch
    AfterFires     int       // optional; 0 = no limit
    OnFirstBreach  bool      // optional; stop after the first reaction fires
}
```

`Until` must be no later than `now + tasks.monitor.max_lifetime`. Whether
an indefinite monitor is ever allowed is open point 1. A monitor ends in
one of three ways, all on J's existing edges, so P needs **no new terminal
state**:

1. **Its stop rule is met** (`Until` passes, `AfterFires` is reached, or
   `OnFirstBreach` fires). The stop rule is time or count, so it is the
   clock's to apply. On the firing where it is met, the handler writes a
   code-built closing summary into the record's draft slot (how long it
   watched, how many probes, breaches and unknowns, each reaction and its
   envelope outcome) and moves the task `awaiting_inputs → synthesizing →
   verifying` in that one write. No model is involved, so L's drafter has
   nothing to do for a monitor. Whether K's `Outcome` carries two
   transitions and a draft for this, or the Plan stage finds an equivalent,
   is confirmed against the built K and L. L's **verifier** then finds the
   task in `verifying` like any other. It evaluates the structural checks
   against the event trail and, because a monitor's close carries a
   `structural` reflection point only, makes no critic call. It sets every
   criterion `met` or `unmet_flagged`, per J's no-pending-criteria rule,
   and routes the task to `delivered`. The clock never decides the monitor
   is complete. A monitor's criteria are authored to be checkable
   structurally ("the watch ran until its stop condition", "every breach
   produced its authored reaction"). No judgment critic runs at a
   monitor's close by default.
2. **The CEO stops it.** `abandoned`, which J's table allows only for the
   `ceo` actor after dispatch. P uses whatever CEO-facing abandon command O
   ships and adds none of its own.
3. **It escalates** (`escalate` reaction, or `probe_unavailable`). It parks
   in `escalated`, and all its timers stay live but inert. Per J, only the
   CEO can move it back to `awaiting_inputs` to resume watching, or abandon
   it. **Confirm against the built K:** K's spec cancels timers on terminal
   states only, and `escalated` is not terminal. P's handler must not probe
   a monitor in `escalated`. It skips the probe and schedules nothing, and
   the CEO's resume reschedules it.

#### 1.5 Budget: rate caps and concurrency

At validation, and again at every CEO resume:

- `interval >= tasks.monitor.min_interval`. Proposed default: 15 minutes,
  provisional.
- The summed hourly probe rate of all active monitors on one function must
  stay under `tasks.monitor.rate_share` of that function's manifest rate
  cap. Proposed default: 25%, so the CEO's live use is never starved.
- At most `tasks.monitor.max_active` monitors in a non-terminal state.
  Proposed default: 10.

A plan that would break any of these is rejected with a code-written reason
that O's planner can relay to the CEO. It is never silently widened.

### 2. The pipeline archetype

#### 2.1 Sequential dependent steps

A pipeline is two or more steps (J's `NeedsRecord` requires
`DependentSteps >= 2`, and one step is a single action). P runs them **one
at a time**, in the stable topological order of `DependsOn` (ties broken by
step order in the plan). No two steps are ever `running` at once. Parallel
branches are out of scope.

Each step has exactly one kind, set at planning time:

| step kind | runs as | how it completes |
|---|---|---|
| `read` | `gate.Invoke` at P1, level R | inline; retried by K with backoff |
| `draft` | `gate.Invoke` at P1, level D (e.g. `gmail.draft_for_review`) | inline; retried by K with backoff |
| `outward` | an approval envelope (level A, or level S when the task is tainted) | when the envelope reaches `executed`; never retried |
| `compose` | L's drafter, which finds the step marked `running` in the record and drafts over refs to earlier steps' outputs | when the drafter (and, for a marked step, the verifier) writes its result into the step; the handler sees it on its next `wait` timer |

"Nothing waiting on another person" is the brief's definition. The only
human a pipeline waits on is the CEO approving its own envelopes. **The
validator rejects any step that waits on a reply** (any step shaped like a
fan-out ask). That work is a fan-out, which O builds.

#### 2.2 Retries, timeouts and failure (K's mechanics, P's policy)

- `read` and `draft` failures use K's `retry` timers, capped at
  `max_attempts`. Exhaustion fires `retries_exhausted`, which escalates.
- Every step has a K `step_timeout` timer. Firing it escalates with
  `step_timeout`.
- An `outward` step is **never** retried, per K. `gapi.ErrSendOutcomeUnknown`
  escalates immediately with "send outcome unknown". An envelope that
  expires or is rejected moves the step to `failed` and escalates, and it
  is not re-proposed. A pipeline never stages the same outward step twice.
- The pipeline observes an outward step's envelope by its status
  (`pending`/`approved`/`executed`/`expired`), on a `wait` timer. **Confirm
  against the built code** how the daemon reports an executed envelope's
  connector error back. P must not guess at a send's outcome.
- A failed step's dependents become `skipped`. The task escalates, and
  completed steps are not undone. There is no rollback or compensation
  step, because anything outward has already been approved and sent.

#### 2.3 Data between steps: refs, not content

J stores refs, never content. P keeps it that way:

- A `read` or `draft` step's args are **fixed at planning time**. P adds
  no templating language that splices one step's output into another's
  args (open point 5).
- A step's output is recorded as refs only: the normalized store records a
  connector's `Normalize` wrote (`<source>:<source_id>`), a `facts.get`
  fact's `Source`, or the audit seq of the call.
- A `compose` step's record carries the refs of the steps it `DependsOn`.
  L's drafter reads them from the record and resolves them from the store
  into scoped context. This is the only place where earlier results are
  read as content, and it is L's drafter, with its grounding and
  source-quote checks.
- An `outward` step's payload is either authored at planning time, or it is
  the output of a `compose` step it depends on. In both cases it reaches the
  CEO as an envelope with a code-built read-back. A composed payload is the
  normal case, e.g. "pull the numbers, draft the board update, stage it for
  me".

#### 2.4 Taint is sticky for the whole task

The daemon's model-tool path keeps taint session-sticky. A pipeline gets
the same rule at task scope. P adds `Tainted bool` to `PipelineState`'s
JSON. That field lives in J's `archetype_state` column, so no migration is
needed. It is set, and never cleared, as soon as any step's result is
`Untrusted` (e.g. any `company_finance.*` or `facts.get` read, which I
marks `External`). From then on, every S-level step is staged as an
envelope (`gate.NeedsEnvelope(S, tainted)`), exactly as the chat path
queues it. A pipeline can never launder external content into an inline
S-level call by splitting the work across steps.

#### 2.5 Reflection points and the finish

- A step marked `structural` in J's `Reflection` is gated by L's
  structural checks, which are pure reads over that step's output. The
  handler evaluates them from the record when its own timer fires, before
  it lets any dependent start. A failure fails the step (no retry, because
  a structural check is deterministic) and escalates.
- A step marked `judgment` is picked up from the record by L's verifier,
  which runs the separate-context critic as its own invocation and writes
  the verdict into the step. L owns the critic, its interface and its
  refinement cap. P neither calls the critic nor waits on it in-process.
  Its handler reads the verdict from the record on its next timer and
  obeys it: `met` continues, `unmet` fails the step and escalates,
  `contradicted` means L's verifier has already fired
  `critic_contradicted` through the escalation sink.
- After the last step, the handler moves `dispatched → synthesizing`, a
  structural move ("every step is done", read from the record). From
  there L's drafter and verifier take the task from the record like any
  other, through `verifying` to `delivered | awaiting_ceo` per J's
  pipeline edges. J's `MaxRefinementPasses` cap on `verifying →
  synthesizing` applies unchanged.
- Which steps are marked, and with which kind, is the planner's call (O).
  P's validator only enforces shape: `judgment` is allowed on `compose`
  steps only, and a step that feeds an `outward` step must be marked (at
  least `structural`), so nothing reaches the CEO's approval queue
  unchecked. Over-marking is bounded by a cap on judgment points per
  pipeline (`tasks.pipeline.max_judgment_points`, proposed 2, provisional),
  which mirrors the brief's warning that over-marking recreates
  unstructured live reasoning.

### 3. The planner gains two archetypes (an extension of O, not a new planner)

O's planning pass and plan validator are extended, not forked:

- The planner's archetype choice now includes `monitor` and `pipeline`. The
  structured plan output gains the §1.1/§1.4 fields for a monitor and the
  §2.1 step list for a pipeline.
- **O's validator gains P's rules**, all in code, all before
  `tasks.Create`: J's `NeedsRecord(Shape)` must agree with the chosen
  archetype (`OpenEnded` for monitor, `DependentSteps >= 2` for pipeline,
  and `ErrAmbiguousShape` is a rejection, never a guess); monitor watch,
  threshold, stop and budget rules (§1.1, §1.4, §1.5); pipeline step kinds,
  the no-reply-wait rule, the reflection-mark rules and the step-count cap
  (§2.1, §2.5); only manifest functions referenced; and every `outward`
  step at level A or S, staged.
- **Ask before dispatch** (O's `ambiguous_instruction` mechanism) covers the
  new shapes: an instruction that names a watch without a stop condition,
  or a threshold without a unit or a number ("tell me if runway gets
  tight"), produces a question to the CEO and, as in O, **no task record**.
  The planner does **not** pick a number for "tight".
- O's procedural-memory hook stays a no-op. Q fills it for every archetype.
  Its skills and procedural-memory lookups still run concurrently, as O §2
  requires, whatever the archetype.

### 4. Surfaces

- J's `water tasks show <id>` (with K's timers section) gains, in the same
  code-built render: for a monitor, the watch, threshold, stop rule, the
  last observation and its source ref, consecutive unknowns and the breach
  history; for a pipeline, each step's kind, status, attempts, reflection
  mark and result refs, plus `Tainted`.
- **No new write command.** Creating a monitor or pipeline goes through O's
  CEO-facing path. Stopping one goes through O's abandon. Resuming an
  escalated one goes through J's CEO-only transitions, however O exposes
  them.
- Config: `tasks.monitor.{min_interval_seconds, max_lifetime_seconds,
  max_active, rate_share, max_consecutive_unknown,
  stage_envelope_ttl_seconds}` and `tasks.pipeline.{max_steps,
  max_judgment_points, step_timeout_seconds, outward_envelope_ttl_seconds}`.
  Every proposed default above is provisional, set in the Plan phase for the
  owner to accept or change, and none of them is an owner policy decision
  in disguise. Each key is recognized by `config.Load`, not silently
  ignored.

## Do not build in P

- A fifth archetype, or a monitor/pipeline hybrid ("watch X, then run these
  steps"). Compose the two later if the owner wants it (open point 7).
- Parallel pipeline branches, rollback/compensation steps, or a templating
  language between step args.
- Any model call on a routine probe, on a breach reaction, or in threshold
  evaluation. A model-phrased breach summary, or a judgment critic at a
  monitor's close, is not built.
- Any autonomous outward action, standing grants or batch approvals (still
  deferred in `docs/known-gaps.md`). A monitor that stages the same reaction
  on every breach still produces one envelope per breach for the CEO to
  approve.
- A second loop, poller or scheduler. P's mechanical work runs on K's
  clock, and its compose and judgment work is picked up by L's existing
  drafter and verifier loops (L §3.8). P adds no loop of its own. No
  workflow-engine dependency. If one were ever proposed, it would need the
  owner's approval under `CLAUDE.md` first.
- Reply ingestion (N), fan-out (O), procedural-memory retrieval or
  promotion (Q), and any change to the decisions strict lane or to I's
  `facts` ledger beyond reading it.
- Adding `facts.get` or any list function to `auto_allowlist` (open point 3).

## Task list

Each task is one commit with its own tests, in this order. The first two
are reconciliation tasks and may turn out to be empty if J and K were built
with them already.

1. **Reconcile trigger kinds.** Make sure `step_timeout`,
   `retries_exhausted` and `probe_unavailable` exist in the built closed set
   (an additive migration if needed, number chosen at commit time), with J's
   exhaustive tests updated.
2. **Reconcile K's step execution.** Confirm K's hook for running a
   mechanical step, or add the minimal `Executor` seam to
   `internal/taskclock` plus `Clock.Notify` (§1.2), with K's existing tests
   passing unmodified.
3. Monitor types and the pure `Eval` (§1.1), table-tested over every
   `facts.Result` status and flag, and over count probes.
4. The monitor handler: probe scheduling, edge-triggered reactions, the
   unknown counter, the stop rule and the close path (§1.2–§1.4).
5. The monitor budget checks and the O validator rules (§1.5, §3).
6. Pipeline step kinds, topological sequencing and sticky taint (§2.1,
   §2.4).
7. The pipeline handler: retries, timeouts, outward envelopes, failure and
   skip propagation (§2.2, §2.3).
8. Reflection and `compose`-step marks in the record for L's drafter and
   verifier to pick up, the handler reading their results on its own
   timers, and the pipeline validator rules (§2.5, §3).
9. `water tasks show` rendering, config keys, the two scenario tests (below),
   an update to `docs/EVOLUTION_PLAN.md`, and stop.

## Acceptance criteria / tests

All tests use a fake clock and `backend.Fake`. No test sleeps, and no test
touches a live connector.

**Monitor**

1. **No model on the hot path.** 200 probe ticks with the value `clear`
   make **zero** `backend.Run` calls and zero `gate.ModelCall` charges,
   counted on `backend.Fake` and the gate.
2. **Unknown is never clear.** Each of `not_available`, `no_data`, `stale`,
   `zero_may_mean_missing`, a gate denial, a rate-cap refusal, a connector
   error and a truncated count observes `unknown`, and none of them fires a
   reaction. `max_consecutive_unknown` of them in a row escalates exactly
   once with `probe_unavailable`, and a single `clear` resets the counter.
3. **Edge-triggered.** The sequence `clear, breached, breached, breached,
   clear, breached` fires exactly two reactions and two notifications with
   distinct `RecordID`s (`:breach:1`, `:breach:2`). A simulated crash after
   the notification insert but before the task write, followed by a
   restart, still yields exactly two notifications.
4. **Reacting is not acting.** Across every reaction kind, zero level-A or
   level-S gate calls are made. `stage` produces exactly one pending
   envelope per breach, with an explicit `ExpiresAt`, a payload
   byte-identical to the planning-time payload, and the observation present
   in `EvidenceRefs` only. An expired staged envelope is not re-proposed.
5. **Stop rule enforced.** A monitor plan with no `Until`, or an `Until`
   past `max_lifetime`, is rejected by the validator and creates no task
   record. `Until`, `AfterFires` and `OnFirstBreach` each end the task in
   `delivered`, via `synthesizing` and `verifying`, with every criterion
   `met` or `unmet_flagged`, all timers cancelled, and no further probe.
6. **Escalated monitors don't probe.** A monitor in `escalated` makes no
   gate call on its probe timer. A CEO resume to `awaiting_inputs`
   schedules the next probe from now.
7. **Budget.** A plan below `min_interval`, one that pushes a function past
   its `rate_share`, and the `max_active + 1`th monitor are each rejected
   with a code-written reason.
8. **Coalescing after downtime.** A daemon down for 10 intervals probes
   once on restart, not ten times.

**Pipeline**

9. **Strictly sequential.** With a `DependsOn` graph that allows two steps
   to run together, the handler never has two steps `running` at once, and
   the run order matches the stable topological order.
10. **No reply waits.** A pipeline plan with a step that waits on a reply
    from anyone other than the CEO's own approval is rejected.
11. **Retry scope.** A failing `read` step backs off and then fires
    `retries_exhausted`. An `outward` step is never retried.
    `ErrSendOutcomeUnknown` escalates immediately. An expired or rejected
    outward envelope fails the step, skips its dependents, escalates, and
    is not re-proposed.
12. **Sticky taint.** After an `External` read step, a later S-level step
    is staged as an envelope instead of running inline, and `Tainted` never
    returns to false within the task.
13. **Checked before approval.** A plan in which an `outward` step depends
    on an unmarked step is rejected. A structural failure on a marked step
    fails it without retry. A `contradicted` verdict from L's critic (via
    `backend.Fake`) fires `critic_contradicted`.
14. **Refs, not content.** No `PipelineState` or `MonitorState` field and no
    event `detail` holds a string that fails J's `<scheme>:<id>` ref shape
    or exceeds its cap. A fixture that tries to store a fetched email body
    as a step output is rejected.

**Planner and end to end**

15. **Shape consistency.** For each archetype, O's validator rejects a plan
    whose `NeedsRecord(Shape)` disagrees with the chosen archetype, and an
    ambiguous shape is rejected, never guessed.
16. **Ask before dispatch.** "Tell me if runway gets tight" (no number) and
    "keep an eye on open deals" (no stop condition) each produce a CEO
    question and no task record (O's ask-before-dispatch), with zero probes
    run.
17. **Scenario tests** in the style of
    `internal/gateway/scenario_s13_test.go`, over the real HTTP surface,
    gate, store and daemon wiring, with `backend.Fake` and a fake
    `company_finance`/`facts` source: (a) "tell me if runway drops below 9
    months over the next 30 days": planned, probed on the fake clock,
    breached once, notified once, then delivered at `Until`; (b) "pull this
    month's cash position, draft the board update and stage it for me": a
    read, a compose (with a structural mark), and an outward step that
    waits in the approval queue, runs only after `Decide(Yes)`, and ends
    `delivered`.
18. **Guard tests.** P's handler package imports neither `backend` nor
    `runtime`, and never calls L's drafter, verifier or critic (model work
    reaches a P task only through L's loops reading the record). It makes
    no `gate.Invoke` on a level-A function, and `go.mod` gains no
    requirement.
19. `go vet ./...`, `go test -count=1 ./...` and
    `CGO_ENABLED=0 go build ./cmd/water` pass, as always.

## Invariant checks (WORKFLOW.md, restated for this slice)

- No connector function runs without a gate permit. Every probe and step
  goes through `gate.Invoke` or an approval envelope.
- Nothing outward happens without a passing gate check and an approved
  envelope. P2 still never reaches level A.
- Untrusted content stays tagged. `Untrusted` is carried from a probe into
  the observation and from a step into the task's sticky taint.
- No metered dependency. All model calls go through L, over the
  subscription CLI. The binary still builds statically.
- The audit log still verifies, and every task change still goes through
  J's audited `Service`.

## Dependencies

- **Hard: O.** P extends O's planner and plan validator, sits next to O's
  fan-out handler, and uses O's CEO-facing create/abandon path and its
  ask-before-dispatch mechanism. It also inherits, through O, J (record,
  transitions, `NeedsRecord`), K (clock, timers, retries, timeouts,
  staging, escalation sink) and L (composer, structural and judgment
  checks). P cannot start until O is verified.
- **Hard: I.** A monitor's `fact` watch is `facts.get` over I's catalog,
  and its `unknown` rule depends on I's `Result` statuses and flags. A
  pipeline's `read` steps commonly use it too.
- **Reconciliation with J and K** (task list items 1–2): the trigger-kind
  mismatch between the J and K specs, and K's missing step-execution field
  in `Outcome`. Both were found while planning P and are flagged above for
  whoever builds J and K first. If they are resolved there, P's first two
  tasks are empty.
- **Not required:** F and G (memory), N (reply ingestion; a pipeline waits
  on no one), and Q (the procedural-memory hook stays a no-op).
- **Downstream:** Q promotes completed monitor and pipeline plans into
  procedure memory, using J's event trail, the same way as fan-out.

## Open design points (owner decisions; do not resolve in the build)

These are P's own open points, plus the redesign's global open questions
where they touch P. The full global list lives in the overview doc. Until
the owner decides, P's behavior is the conservative one stated in each item.

1. **Indefinite monitors.** The brief requires a stop/abandon rule. It does
   not say whether "watch this forever until I say stop" is acceptable with
   a CEO stop as the only rule. P requires `Until` and caps it with
   `max_lifetime` until the owner decides. The cap's value is provisional.
2. **What a monitor may watch.** P ships `fact` and `count` only. Watching a
   field inside a connector record (a deal's stage, an issue's priority)
   needs a code-defined extractor per field, the same concern as I's
   "don't trust header positions". Which fields, if any, is the owner's
   call.
3. **Origin for background probes.** P1 (K's convention, R-only enforced by
   P's validator) versus P2 plus `auto_allowlist`. P2 would put a probe
   under the auto-mode rules and the subscription usage split, which is
   arguably what "background" means, but it requires deciding which
   functions join `auto_allowlist`. I left this to P, and P leaves it to the
   owner.
4. **Re-arm policy.** Edge-triggered with a one-probe re-arm (default), a
   hysteresis band, or a periodic "still breached" reminder. Each changes
   how often the CEO hears about the same condition.
5. **Data flow between steps.** Refs into a `compose` step only (default),
   or a small, validated way to bind a prior step's output into a later
   step's args (e.g. "read the latest invoice, then fetch that client's
   thread"). The second is more capable, and it is a templating language
   with its own taint and injection questions.
6. **Do monitor breaches appear in Today?** A `notify` breach writes a
   notification but leaves the task in `awaiting_inputs`, which K's
   `needsyou` extension does not list. Whether `needsyou.Compute` should
   show recent breaches, and where they sort, is open. Relatedly, which
   reaction kinds a given watch may use at all overlaps with the global
   **sensitivity tiers** question (which outputs always need CEO review).
7. **Monitor-then-pipeline compositions** ("when runway drops below 9
   months, draft a cost-cut memo"). Out of scope. The default is a `stage`
   reaction with an authored payload, not a triggered pipeline.
8. **Global questions touching P**, not resolved here:
   - **Confidence thresholds.** P uses none. Monitor thresholds are the
     CEO's own numbers, and the planner never invents one ("tight" is a
     question, not a number). Any numeric default in this spec is
     provisional until checked against real CEO accept/edit/reject
     outcomes.
   - **Critic architecture** applies to pipeline judgment points through L's
     interface. P is agnostic.
   - **Escalation target.** The source paste was garbled at this point. The
     readings are: the twin nudges in the CEO's name, the twin escalates to
     a non-responder's manager, or the twin only ever escalates to the CEO.
     P implements CEO-only, through K's sink and notifications. For a
     monitor and a pipeline there is usually no "non-responder", so this
     matters less here than in fan-out, but P does not assume an answer.
   - **"Same shape" for procedural-memory retrieval** (Q) must also say
     whether two monitors with different thresholds, or two pipelines with
     the same step kinds over different functions, are the same shape.
   - Reminder cadence, quorum, material conflicts and the late-reply policy
     are fan-out concerns (K, N, O) and do not apply to P.

Source-brief note: in the parts P covers, the brief's §4 heading reads
"Task/workflow arch", which looks truncated (probably "architecture"). The
meaning is clear and nothing depends on it, but it is **possibly corrupted
in the source paste**. The Planner bullet's "doing a distinct reasoning
part" may also have lost a word (possibly "pass"), and is flagged the same
way. Neither changes P's scope. The brief gives the monitor archetype one
line ("no fixed end, watch and react to a defined threshold") and the
pipeline one line ("sequential dependent steps, nothing waiting on another
person"). Everything more specific above is P's proposal for the owner to
approve at the Plan phase, not something the brief decided.
