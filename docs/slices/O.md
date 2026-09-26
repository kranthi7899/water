# Slice O: the Planner, plus the fan-out/collect/synthesize archetype end to end

**Planning only. Do not implement until Slices H, K, L and N are built and
verified.** This spec authorizes no code yet. It is one sub-slice of the
intelligence-layer redesign; see the overview doc for the full F–Q
sequence and the global open-questions list. Build it with
`docs/WORKFLOW.md`'s Explore → Plan → Approve → Implement loop, and
re-read every file named below at build time, because J, K, L and N will
all have changed the tree by then. Where this spec names a type or package
that J, L or N own (the task record, the composer, the reply signal), the
name in that slice's own spec wins over the placeholder used here.

Covers brief §4 (the Planner role) and §6 (authoring-time escalation
triggers, and "ask before dispatching, not after").

Builds on:
- **H:** the skills registry (`internal/skills`, `Registry.Block()`). The
  planner reads skills only as **descriptions** of responsibilities. It
  never treats a skill's function list as the task's context (brief §4:
  "do not assume a skill's default bundle covers it").
- **J:** the durable task record, the pure transition table (created →
  dispatched → awaiting_inputs → synthesizing → verifying → awaiting_ceo |
  delivered | escalated | abandoned), archetype-specific state, the
  acceptance-criteria list, stored escalation triggers and the audit trail.
- **K:** `internal/taskclock`: the due-work loop, the `task_timers` table,
  the `Handler` interface (`OnTimer(task, timer, now) Outcome`), the
  `OutwardStep` payload hook for reminders, the reminder policy
  (`notify_ceo` by default, `stage_envelope` only when the owner sets it),
  and the single escalation sink `Clock.Escalate`.
- **L:** the composer: structural checks (pure code), grounded extraction
  with source quotes, synthesis, and the separate-context critic (a cold
  `backend.Run`, capped at 1–2 refinement passes) that returns
  met/unmet/contradicted per criterion. L also defines the data shape by
  which authoring marks a step as a reflection point.
- **N:** human-reply ingestion: a Gmail reply turned into a structured,
  untrusted signal (participant, fields with verbatim quotes,
  deferral/decline/delegation), correlated to the outbound ask.
- **Already built:** `gate` (R/D/A/S/B, P0/P1/P2, "P2 never reaches level
  A"), `approvals` (`Queue.Propose`, payload-hash envelopes, `DefaultTTL`
  15 minutes, `ExpireStale`'s "silence is a no"), `audit`, `store`,
  `roster` (`PersonByIdentity`), `gmail.send_message` (level A, sent from
  the agent alias), `internal/needsyou`, and the gateway's single
  approved-envelope execution path `decideAndExecute`
  (`internal/gateway/actions.go`).

Before O, every piece of the task machinery exists and none of it has a
real job. J stores tasks, K ticks timers with only a test handler, L
composes, and N parses replies, but nothing turns a CEO instruction into a
task, and no archetype runs end to end. O adds the one model-driven
authoring step and runs the first real archetype end to end across all
four.

**How the pieces interact at runtime: shared state, not a pipeline**
(owner amendment, 2026-09-25; J's Principle, the overview's §2.1). An
earlier draft of this spec described the run as a sequential hand-off:
planner, then clock, then reply ingestion, then composer, each passing work
to the next. That was wrong. O's run is one durable task record, plus
memory, that every component reads and writes independently:
- the planner writes the record once;
- the ask observer and N write replies and statuses the moment they
  happen;
- K's timers read the record on their own schedule;
- L's drafter and verifier pick up the states they own from the record.

No component calls another at runtime or waits on another's return. The
numbered list in §6 describes what the record goes through and who writes
each part. It is not a chain of calls. The **build order** (H, K, L and N
before O) is unchanged by this. It is a real constraint on what must exist
before O can be built, not a description of runtime control flow.

## Principle

The planner is **the model, doing a distinct reasoning pass**, and
everything it produces is checked by code before it can matter. It
chooses a shape, decomposes the goal, and writes the criteria and
triggers. It does not choose what runs when (K), judge its own output
(L's critic), or send anything (every outward ask is a separate
gate-approved envelope). A plan that fails validation creates nothing.

**Separate sub-agent invocations, by design.** The main agent (the warm
session) does not plan inline in its own context and then go on to draft
and judge in the same serial chain. The planning pass is a separate
invocation: one cold `backend.Run` in a fresh context, which returns a
structured, code-validated plan (a summary) and never its transcript. The
same holds for the pieces it sets going: the output-schema designer (L
§3.0), the drafter and the critic (L §3) are each their own invocation,
and each writes its result into the task record rather than returning it
to a caller. This is a design requirement of the amendment, not an
implementation detail left for later. It mirrors this project's
coding-agent precedent: subagents run in a fresh context and return a
summary, not their whole transcript.

**Deciding that a request needs a task is not a separate classifier.** The
main model notices, in its own live reasoning, that a request fans out to
people, waits on something or is a real multi-step pipeline, and so
decides to call `tasks.plan`. That is the same judgment it already makes
when it decides to call any tool. O adds no classification pass or
classifier model that screens requests first and second-guesses the main
model, which would repeat the Tier 1 mistake Slice R retired (owner
amendment, 2026-09-25; J §1).

Ambiguity is resolved **before dispatch, not after**. If the instruction is
ambiguous, the planner returns questions to the CEO and **no task record is
created**. Nothing is half-dispatched while the twin waits for an answer.

Fan-out is the only archetype O makes selectable. Monitor and pipeline
stay rejected by the validator until Slice P, so O remains one bounded,
verifiable end-to-end path.

## Findings (Explore phase, read 2026-09-25; re-verify at build time)

1. **No planning pass exists.** Every model call today is either the warm
   main-path session or a cold one-shot `backend.Run` with a fixed system
   prompt: `decisions.ModelClassifier.Classify` (`internal/decisions/classify.go`),
   the phraser, the recap, and `promote.Draft`
   (`internal/nervous/promote/draft.go`). `promote.Draft` is the closest
   precedent. It makes exactly one cold call through a plain
   `backend.Backend`, so the warm session cannot be touched by construction,
   and code (never the model) overwrites identity and provenance fields
   after the reply. The planner follows that pattern exactly.
2. **One envelope per outward ask, no batch.** `approvals.Queue.Propose`
   takes one `Envelope` (`Action`, `Recipient`, `Payload`, `EvidenceRefs`,
   `Origin`, `ExpiresAt`). Standing grants and batch approvals are still
   deferred (`docs/known-gaps.md`). A five-person fan-out is therefore five
   envelopes, and O does not change that.
3. **`DefaultTTL` is 15 minutes.** An ask envelope left for the default TTL
   expires unseen if the CEO steps away. `Propose` honours an explicit
   `ExpiresAt`, so O sets one (§4).
4. **Envelope execution has one path and no observer.** `decideAndExecute`
   (`internal/gateway/actions.go`) decides, then runs `gate.Invoke` with
   the `EnvelopeID` (the gate `Claim`s it), and returns the connector
   `Output`. Nothing downstream learns that a particular envelope executed.
   `gmail.send_message`'s output (`writeMessageOutput`) carries `id` and
   `thread_id`, which N needs to correlate a reply to the ask, and that
   output is currently returned to the HTTP caller and dropped. Expiry
   happens separately, in `ExpireStale`, also with no hook.
5. **`gmail.send_message` cannot reply in-thread.** `writeMessageSchema`
   (`internal/connectors/google/gmail/gmail.go`) accepts only `to`,
   `subject`, `body` and `html_attachment`. There is no `thread_id` or
   `In-Reply-To`. A reminder, if the owner ever enables sending one, starts
   a new Gmail thread. N's correlation therefore cannot rely on the Gmail
   thread alone (§4, and Risks).
6. **Participants resolve through the roster.** `roster.PersonByIdentity(ctx,
   st, key, value)` resolves one identity to one `store.Person` and
   returns `ErrNoSuchIdentity` on a miss. A roster is optional (ceo-demo and
   counterparty have none). Name-to-person resolution for the planner's
   participants needs a lookup that can report **zero, one or many**
   matches. `PersonByIdentity` returns the first match only.
7. **Config prefixes.** `internal/config`'s `retiredPrefixes` silently
   ignores `orchestration.`, `skills.` and `memory.` (K found this). O's
   keys live under `tasks.planner.*` and `tasks.fanout.*`, next to K's
   `tasks.clock.*`.
8. **No scenario-numbering convention exists** (`docs/EVOLUTION_PLAN.md`,
   M's planning entry). The one existing end-to-end precedent is
   `internal/gateway/scenario_s13_test.go`: the real HTTP surface over
   `newHarness`, the real gate/store/daemon wiring, and `backend.Fake` as the
   only model. O's scenario test copies that shape and gets a descriptive
   name rather than an invented number.

## Scope

### 1. Entry points

All CEO-initiated. Planning is never triggered by a sync tick or by P2.

- `POST /v1/task-records {goal, answers?}` (same auth as `/v1/decisions`) and
  `water tasks new "<goal>"`, a thin client over it. Origin P0.
- A model-callable `tasks.plan` function, registered as an internal
  connector (the `internal/twinlink` precedent) and granted in
  `twins/ceo/twin.yaml`. When the main model's live reasoning recognizes
  that a request like "ask the five leads for their Q3 numbers and give me
  a summary" needs the task machinery, it calls `tasks.plan` the way it
  calls any other tool, and the CEO never switches surfaces. No separate
  classifier decides this first (Principle). It reaches the model through
  the existing `twinFunctions()` / `handleToolInvoke` path. **Its level is an owner decision** (see Open
  design points). The proposed default is D: the call validates a plan
  and stages envelopes, and staging is not itself outward. A `tasks.plan`
  call from a tainted session records `untrusted: true` on the task, and
  that flag is shown on every ask envelope's read-back.
- H's `delegation` skill gains `tasks.plan` in the same commit that grants
  it, per H's rule that skill files never run ahead of the manifest.
- `water tasks abandon <id>` and `water tasks proceed <id>` (§6). K left
  abandoning to O as the CEO-facing concern.

### 2. The planning pass: `internal/planner`

```go
// internal/planner/planner.go
type Planner struct {
    Backend    backend.Backend // plain Backend: never the warm session
    Model      string          // the manifest's strong tier
    Timeout    time.Duration
    Charge     func() error    // gate.ModelCall(P0), as ModelPhraser.Charge is
    Procedures ProcedureLookup // Q fills this; O ships NoProcedures
}

type Request struct {
    Goal    string
    Answers []QA // CEO answers to an earlier round of questions
    Now     time.Time
}

func (p *Planner) Plan(ctx context.Context, r Request, env PlanEnv) (Result, error)

// Result is exactly one of: a validated Plan, Questions for the CEO, or
// SingleAction (code's NeedsRecord found nothing to track; no task record,
// and the main path answers the request as an ordinary turn).
type Result struct {
    Plan         *Plan
    Questions    []string
    SingleAction bool
}
```

`PlanEnv` is built in code before the call: the skills index (H's
`Block()` text, descriptions only), the non-B manifest functions with their
levels, the roster's people (name, role, team; **no contact details**, which
code resolves after the reply), and the current local time and time zone.

**Procedural-memory hook, a no-op in O.**

```go
type ProcedureLookup interface {
    // Match returns an approved procedure whose shape fits the request, or
    // ok=false. It takes the raw request, not the built PlanEnv, so it
    // depends on nothing the skills lookup produces. O ships NoProcedures,
    // which always returns ok=false.
    Match(ctx context.Context, r Request) (proc Procedure, ok bool, err error)
}
```

Q fills this. Its matching criterion is an open owner question, and O
defines only the call site and the "no match means plan fresh" behavior
(retrieve, or else generate).

**The two lookups run concurrently, never in a chain** (owner amendment,
2026-09-25). At planning time, building the relevant-skills part of
`PlanEnv` and `ProcedureLookup.Match` are two independent queries, and
nothing depends on either result until the model call. So `Plan` starts
both at once, together with the other independent `PlanEnv` reads (the
manifest functions and the roster view). It waits for all of them, then
makes the one model call with whatever came back. They do **not** run as a
sequence where skills are listed first and memory is checked after, or the
other way round. A lookup error is reported as that lookup's miss (plan
fresh, or plan with no skills index) and never blocks the other lookup.
This uses standard-library concurrency (`sync.WaitGroup`, channels) only,
and adds no `errgroup` or other new dependency.

**One cold call.** One `backend.Run` with a fixed system prompt, charged
against the usage cap before the call, returning one JSON document. Like
`promote.Draft`, code overwrites the task id, timestamps, provenance
(`drafted_by: planner`, model, plan hash) and every roster-derived field
after the reply. The model never writes an email address. It names a
participant, and code resolves the address.

### 3. The plan

```go
// internal/planner/plan.go
type Plan struct {
    Archetype    string        // "fan_out" in O; "" is SingleAction, never stored
    Goal         string
    Deadline     time.Time     // required for fan_out
    Participants []Participant
    Steps        []Step
    Context      []ContextSource
    Criteria     []Criterion
    Triggers     []Trigger
    Questions    []string      // ambiguity: non-empty means ask, create nothing
}

type Participant struct {
    Name      string // as the model wrote it; code resolves PersonID/Email
    PersonID  string // code-filled
    Email     string // code-filled from the roster identity
    Ask       Ask    // subject + body, model-authored prose
    Reminder  string // model-authored text, used only under K's stage_envelope
    Criteria  []string // criterion ids this participant's reply serves
    // Requested fields for N to extract (N §4: name, description, optional
    // kind, and a binding to a criterion). N requires J to carry them; the
    // exact shape is settled with J and N at O's Plan stage.
}

type Step struct {
    ID         string
    Kind       string   // mechanical | reflection
    Check      string   // reflection only: structural | judgment (L's shape)
    Function   string   // mechanical only: a manifest function id
    Outward    bool     // code-derived from Function's level, never model-set
    DependsOn  []string
}

type ContextSource struct {
    Function string         // an R-level manifest function
    Args     map[string]any
    Why      string         // one line, shown in `water tasks show`
}

type Criterion struct {
    ID       string
    Text     string
    Critical bool   // count-based triggers may only bind to critical criteria
    Material bool   // a conflict between replies on this criterion is escalation-worthy
}

type Trigger struct {
    ID     string
    Kind   string            // see §5
    Params map[string]string // e.g. criterion id
}
```

**Context scoping.** `Context` is the task's own list of reads to run at
synthesis time (for example, last quarter's `company_finance.budget_status`
to compare the replies against). It is authored per task. It is not
derived from any skill, and a skill's function list is never copied into
it. Only R-level functions are allowed (a context read never writes or
sends).

**Reflection points.** The fan-out skeleton has fixed structural anchors
that the validator requires (§4): the structural condition "every
participant terminal" that gates the move to `synthesizing` (J's
`Structural` read, which the clock's handler consults when its own timer
fires, not a stage that runs after replies finish), and one judgment check
(L's verifier and critic) on the draft.
The planner may mark further steps as reflection points, but the validator
caps judgment points per plan (proposed: 2, provisional, see Open design
points), because over-marking recreates unstructured live reasoning and
under-marking recreates a rigid script (brief §5).

### 4. Validation in code, before anything is created

`Plan.Validate(m *twins.Manifest, roster RosterView, cfg Config) error`
runs on every plan. A failure creates no task, stages no envelope, and is
returned to the CEO with the failing rule named. Rules:

- **Archetype.** `fan_out` only. `monitor` and `pipeline` fail with "not
  available until Slice P". Any other value fails.
- **Criteria.** Non-empty. Every id is unique. Every participant serves at
  least one criterion, and every criterion is served by at least one
  participant or context source.
- **Functions.** Every `Step.Function` and `ContextSource.Function` is in
  the manifest and not level B. Context sources are level R only.
- **Outward steps.** `Outward` is set by code from the manifest level, never
  by the model. Every outward step must be level **A**. A plan that routes
  an outward effect through an S-level or D-level function is rejected. In
  O the only outward function a fan-out may use is `gmail.send_message`,
  because N ingests Gmail replies only. Twinlink and Slack asks are out of
  scope (there is no Slack connector).
- **Rate budget.** The number of asks does not exceed the manifest's
  `gmail.send_message` rate cap (today 20 per hour). A larger fan-out is
  rejected rather than half-sent.
- **Participants.** Each resolves to exactly one roster person with an
  email identity. **Zero or several matches do not fail validation: they
  become questions** (below). The CEO's own address and the agent alias are
  rejected as participants.
- **Deadline.** Present, and in the future.
- **Skeleton.** The step graph is acyclic, and every `DependsOn` names a
  real step. The required structural and judgment anchors are present, and
  judgment points do not exceed the cap.
- **Triggers.** Every kind is known (§5). A count-based trigger binds to a
  critical criterion.
- **Text.** Ask subjects and bodies are non-empty and within a length cap.
  They contain no email address other than the participant's own. There
  is **no general outbound-content scrubber in the repo today** (only
  per-connector token scrubs such as `gapi`'s `scrub` and the gate's
  `redact`). If F's never-store validator exposes its credential and
  government-ID detectors cleanly, the asks reuse them. Otherwise the rule
  is dropped from O rather than a new detector being invented here.

**Ask before dispatch.** Questions come from two sources and are merged:
the model's own `Questions` (it judged the instruction ambiguous), and
code-detected gaps (an unresolved or ambiguous participant name, no roster
at all, a missing or past deadline, a goal with no inferable criteria). If
the merged list is non-empty, `Plan` returns `Result{Questions}` and
**nothing is persisted**. The CEO answers with `answers` on a second
`POST /v1/task-records`, and the planner runs again with the goal plus the Q&A.
This is the brief's `ambiguous_instruction` trigger, evaluated before
dispatch and never after.

**Single action.** The planning pass is not asked "is this a task?". That
recognition already happened in the main model's live reasoning when it
chose to call `tasks.plan` (Principle). Code then applies J's structural
`NeedsRecord` to what the planner authored. If the authored shape has
nothing to track (no participants, nothing to wait on, fewer than two
dependent steps), the result is `SingleAction` and no task record is
created. `POST /v1/task-records` says so, and `tasks.plan` returns a result
telling the main path to handle the request as an ordinary turn. This is a
structural consistency check in code, the same way `decisions` computes
`Readiness`, not a second model's classification. Single action is the
default for most requests (brief §4), and most of them never reach
`tasks.plan` at all. J's spec owns the general rule for when a record
exists. O is the one caller that applies it.

### 5. Authoring-time escalation triggers

The planner writes triggers onto the task, and J stores them. Each kind has
exactly one evaluator:

| kind | evaluated by | fires when |
|---|---|---|
| `ambiguous_instruction` | O, before dispatch | §4; produces questions, never a task |
| `reminders_exhausted_critical` | K | a participant's reminders run out on a critical criterion |
| `deadline_passed` | K | the task deadline passes while still awaiting inputs |
| `step_timeout`, `retries_exhausted` | K | as K defines them |
| `replies_conflict_material` | O's fan-out handler, reading N's signals from the record when its own timer fires (N also claims it; see the note below the table) | two participants' extracted values for a `Material` criterion differ after code normalization |
| `reply_declined` | O's fan-out handler, reading the record when its own timer fires | a participant declines or delegates on a critical criterion |
| `critic_contradicted` | L's verifier | the critic marks any criterion contradicted |
| `sensitivity_match` | O at plan time; L's verifier reads it from the record before routing to delivery | content matches a sensitivity rule; the rule set is empty until the owner decides tiers |
| `no_asks_sent` | O | every ask envelope was denied or expired |

Every one of them escalates through `Clock.Escalate`, K's single sink, so a
task reaches the CEO in exactly one way, with one notification per
`task:trigger`. That call is a synchronous write into the record made by
the evaluator itself (K §5), not a hand-off to K's loop. Kind names are J's
closed `TriggerKind` set.

**Unresolved between O and N (settle at O's Plan stage).** N's spec (§5)
also evaluates `replies_conflict_material`, with a different interim rule
(escalate every detected conflict on any requested field), and N raises
`reply_unmatched` and `sensitivity_match` from single replies. This table's
"exactly one evaluator" rule needs one owner per kind. J's `Criterion` also
has `Critical` but no `Material` field, so O's `Material` mark has nowhere
to be stored yet; J gains it (or the rule changes) at the same point.

`replies_conflict_material`'s comparison is deliberately mechanical and conservative:
it escalates on any difference on a `Material` criterion. What *should*
count as a material conflict is an open owner question, and the rule is
labelled as interim until the owner decides.

### 6. The fan-out archetype end to end: one record, independent writers

A new package `internal/tasks/fanout` (placed under J's package; re-check
J's layout at build time) implements K's `Handler`, the ask staging and the
ask observer. The list below follows the task record through its life and
names **who writes each part and what triggers that write**. It is not a
call chain. No item calls the next one. Each writer reads the record when
its own trigger fires (a CEO request, an executed envelope, an arriving
reply, a K timer, or L's loop finding a state it owns), writes under J's
version compare-and-swap, and stops.

| Writer | Triggered by | Writes |
|---|---|---|
| Planner + create (O) | the CEO's request | the record, criteria, triggers, the initial timers (item 1) |
| Schema designer (L §3.0), spawned by O | record creation | `OutputSchema`, once, concurrently with items 2–5 |
| Ask observer (O) | an envelope executing | participant `asked`, N's ask ledger, `created → dispatched` |
| Reply ingestion (N) | a reply arriving | participant status and criterion evidence, at once |
| Fan-out handler (O, on K) | its own K timers | reconciliation, reminder bookkeeping, `awaiting_inputs`, the proceed move, `deadline_passed` |
| L's drafter / verifier | finding `synthesizing` / `verifying` | the draft, verdicts, the move out of `verifying` |
| The CEO | `water tasks proceed` / `abandon` | the CEO-only moves |

1. **Create.** In one transaction: the J task in `created` (goal, deadline,
   criteria, triggers, the full validated plan as archetype state, the
   plan hash, and `untrusted` if applicable), and per-participant status
   `pending` (J's `ParticipantStatus`; the ask is staged but not sent). J's
   audit trail gets a `planned` entry. In the same transaction, through
   K's timer-write path (which keeps K's `next_action_at` invariant), the
   task's own timers are written: one `deadline`, and one recurring
   `check_inputs` `wait` timer. That second timer is how the fan-out handler
   re-reads the record on its own cadence (`tasks.fanout.check_seconds`,
   proposed at the Plan stage, provisional). Once this transaction commits,
   the planner is done. It hands the task to no one.

   **Output schema, in parallel.** Right after the record commits, O
   spawns L's schema designer (L §3.0) as its own invocation, and does not
   wait for it. It first tries L's `SchemaLookup` (Q fills it; until then
   it is L's `NoSchemas`), then designs one with a cold call if nothing
   matches. It reads the goal and the criteria and writes `OutputSchema`
   into the record through J's `SetOutputSchema`. It runs **concurrently
   with the fan-out/collect phase** (items 2–7), never after it. If it
   fails, the drafter uses the fallback schema later and records a gap (L
   §3.0).
2. **Stage asks.** One `approvals.Queue.Propose` per participant:
   `Action: gmail.send_message`, `Recipient: <email>`, `Payload: {to,
   subject, body}`, `Origin: p1` ("a task the CEO approved or scheduled",
   `internal/gate/gate.go`), `EvidenceRefs: ["task:<id>",
   "task:<id>:ask:<person_id>"]`, and an explicit `ExpiresAt` from
   `tasks.fanout.ask_ttl_seconds` (required config, no default: see Open
   design points). The subject carries a short code-generated task tag, so
   N can correlate a reply even without the Gmail thread (finding 5).
   Staging uses the same two-phase "record intent, propose, record
   envelope id" pattern as K's reminders, so a crash cannot produce two
   envelopes for one ask.
3. **Observe execution (ask observer).** A narrow observer is added to
   `decideAndExecute`: after an envelope executes, observers are given the
   envelope and its `Output`. The fan-out observer matches `task:` evidence
   refs and writes, into the record: the sent message id and `thread_id` on
   the participant, the outbound-ask ledger row (through N's
   `RecordOutboundAsk` write helper, N §2), the participant's status
   `asked`, and, on the first `asked`, the move `created → dispatched`. It
   notifies no one and schedules nothing. The observer runs after the
   gate's claim, can't veto or alter execution, and a failure in it is
   logged and never reported as an execution failure (the same "never read
   as not executed" rule the function already follows). Denied and expired
   envelopes are picked up by pull, not push: whenever the fan-out
   handler's `check_inputs` timer fires, it reconciles `pending`
   participants against `approvals.Get` and marks them `not_sent` (a
   terminal status in J's set).
4. **Awaiting inputs (fan-out handler, on its own timer).** When a
   `check_inputs` firing reads that every ask is `asked` or `not_sent`, the
   handler moves the task to `awaiting_inputs` and schedules one `reminder`
   timer per `asked` participant. If every ask is `not_sent`, it fires
   `no_asks_sent`.
5. **Replies (N, the instant a reply arrives).** N writes each signal
   straight into the record itself, under J's version compare-and-swap (N
   §5, J's Blocks list and K's downstream notes all assign this to N):
   participant status `replied`, `declined`, `deferred` or `delegated`,
   and the verified fields attached as evidence refs to the criteria they
   are bound to. This is **not** a round trip to the orchestrator. Nothing
   reacts to N's write as such, and N neither calls nor wakes the handler.
   The next time any of the handler's own timers fires on that task, it
   reads the record as it then stands. A `reminder` timer whose participant
   is now terminal does nothing and cancels that participant's remaining
   reminders. A `check_inputs` firing evaluates `reply_declined` (and
   `replies_conflict_material`, subject to the O/N note in §5) over
   whatever signals the record holds. A signal never authorizes anything
   outward. A delegation ("ask Priya instead") does **not** stage a new
   ask automatically. It is surfaced to the CEO as a proposed plan edit.
6. **Reminders.** K owns them entirely. O supplies only the
   `OutwardStep` payload (the participant's planning-time `Reminder` text,
   same subject tag) for K's `stage_envelope` policy. Under K's default
   `notify_ceo` policy, nothing is sent.
7. **When to proceed.** This is J's `Structural` read over the record, a
   question any caller can ask at any moment and not a stage that runs
   after reply ingestion. The fan-out handler asks it whenever its own
   `check_inputs` timer fires. When every participant is terminal by J's
   `ParticipantStatus.Terminal()` (`replied`, `declined`, `delegated`,
   `not_sent`, or `no_response` once reminders run out), the handler moves
   the task to `synthesizing`. That is a structural rule, not a judgment of
   completion or quality. K never makes that judgment (K's Principle). On
   the deadline with participants still pending, O **does not proceed on a
   partial set**. The quorum rule is an open owner question.
   `deadline_passed` escalates, and the CEO may run `water tasks proceed
   <id>`, which moves the task to `synthesizing` with each missing
   participant recorded as an explicit gap.
8. **Synthesize and verify (L, observing the record).** O hands L nothing.
   L's drafter finds the task in `synthesizing` on its own loop (L §3.8).
   It reads from the record the goal, the criteria, the `OutputSchema` (or
   the fallback), and N's quoted fields through the `reply` resolver. It
   runs the plan's `Context` reads at that point, at P1, through
   `gate.Invoke`, then drafts and writes the draft back, moving the task
   to `verifying`. L's verifier finds it in `verifying`, runs the critic as
   a separate invocation over the draft it reads back from the record, and
   writes the verdicts. There are at most 1–2 refinement passes, each one a
   trip through the record's `verifying → synthesizing` edge.
9. **Deliver or escalate (L's verifier, one code-only rule over the
   record, L §3.8).**
   - any `contradicted`: the verifier fires `critic_contradicted`, and
     the task goes to `escalated`;
   - `replies_conflict_material` or `sensitivity_match` recorded as fired
     on the task: `awaiting_ceo`;
   - otherwise: `delivered`, with every `unmet` criterion and missing
     participant listed as an explicit gap rather than padded over.

   **Delivery is to the CEO only**: a `task` notification through
   needsyou, written from the record when it reaches `delivered` (whether
   the verifier's delivering write inserts it through K's single
   task-notification path, or needsyou's own tick notices the `delivered`
   record, is settled at O's Plan stage; overview §3.2), and the report in
   `water tasks show <id>` and `GET /v1/task-records/{id}`. No
   `Result` is returned to O or anyone else. Sending the report on to
   anyone else is a separate, later CEO action through an ordinary
   envelope. O stages nothing outward after the asks.
10. **Late replies.** A reply that arrives after `delivered` is written by
    N into the record and the audit trail, and raises one notification. As
    an interim default only, O neither amends nor re-sends anything. The
    real late-reply policy is an open owner question.

### 7. Config (`tasks.planner.*`, `tasks.fanout.*`, all new keys)

`tasks.planner.enabled` (default false; `POST /v1/task-records` and `tasks.plan`
answer "disabled" until it is on), `tasks.planner.timeout_seconds`,
`tasks.planner.max_judgment_points` (proposed 2, provisional),
`tasks.fanout.max_participants` (bounded above by the send rate cap),
`tasks.fanout.check_seconds` (the cadence of the handler's own
`check_inputs` timer, §6; a mechanical default proposed at the Plan stage,
provisional, not owner policy), and `tasks.fanout.ask_ttl_seconds`, which
has **no default**: config loading fails if the planner is enabled without
it.

### 8. Surfaces

`POST /v1/task-records`, `GET /v1/task-records/{id}` (J's read endpoint gains the plan,
participants, gaps and report), `water tasks new`, `water tasks proceed`,
`water tasks abandon`. The plan read-back is rendered in code, the same
"code builds the text" posture as `approvals.ReadBack`: each participant,
their ask, the criteria they serve, the deadline, the triggers, and the
`untrusted` flag.

## Do not build in O

- The monitor and pipeline archetypes (P).
- Any procedural-memory matching or promotion. O ships only the
  `ProcedureLookup` call site with `NoProcedures`, and wires L's
  `SchemaLookup` with L's `NoSchemas` (Q fills both).
- Any change to K's clock mechanics, L's drafter, verifier, critic or
  schema designer, or N's extraction. O registers the handler and spawns
  the planning and schema-designer invocations. At runtime it calls none
  of K, L or N to move a task along. They read the record.
- Autonomous sending of any kind, standing grants, batch approval of the
  asks, sending the report onward, or nudging anyone in the CEO's name.
- A `thread_id` argument on `gmail.send_message` (finding 5). That is a
  connector change and is recorded as a gap.
- Twinlink or Slack fan-out.
- Any new dependency, including a workflow engine.

## Open design points (owner decisions; do not resolve in the build)

These are the global open questions as they touch O. The interim behavior
named for each is the safe placeholder O ships, not a decision.

- **Quorum.** Whether "4 of 5 replied" is enough to proceed. Interim: never
  proceed on a partial set without `water tasks proceed`.
- **Material conflict.** What counts as a material conflict between
  replies, warranting escalation rather than neutral reporting. Interim:
  any difference on a criterion the planner marked `Material` escalates.
- **Escalation target.** The source paste was garbled at this point. The
  plausible readings are: (a) the twin may nudge a non-responder *in the
  CEO's name*, (b) the twin may escalate to a non-responder's *manager*,
  or (c) the twin only ever escalates *to the CEO*. O, like K, implements
  (c) only. Asks go out from the agent alias, as `gmail.send_message`
  always does.
- **Late-reply policy** after delivery: amend, send a delta, or hold for
  the next cycle. Interim: record and notify, nothing else.
- **Reminder count and cadence**, owned by K's config. O only supplies
  reminder text.
- **Sensitivity tiers.** Which outputs always require CEO review regardless
  of the gate. `sensitivity_match` exists with an empty rule set. In O every
  report is delivered only to the CEO anyway.
- **Confidence thresholds.** O uses none. The planner's archetype choice and
  questions are not scored, and any number added later is provisional
  until checked against real CEO accept/edit/reject outcomes.
- **Critic architecture**, owned by L and behind L's interface.
- **Procedural-memory "same shape"**, owned by Q. O's hook always misses.
- **O-specific:**
  - The level of `tasks.plan` (proposed D; A would make every planning
    handoff from chat an approval on its own, in addition to the asks).
  - `tasks.fanout.ask_ttl_seconds`, i.e. how long an unapproved ask may
    wait before silence counts as a no.
  - The judgment-point cap per plan (proposed 2).
  - Whether the CEO should see and confirm the whole plan before any ask
    envelope is staged, or whether approving the ask envelopes is itself
    the confirmation (as proposed here).

## Notes on the source brief

- Brief §4 says the planner should scope context "(do not assume a skill's
  default bundle covers it)", while brief §2 says a skill has **no** default
  bundle. This reads as a leftover from the framing that §2 corrected,
  rather than as paste corruption. O follows §2: skills are descriptions
  only, and the planner scopes context per task.
- Brief §4 calls the planner "the model, doing a distinct reasoning
  part". "Part" may be a corrupted "pass". **Possibly corrupted in the
  source paste.** The meaning is not load-bearing for this spec, which
  treats it as a separate planning pass either way.

## Acceptance criteria / tests

All model calls use `backend.Fake`. Time comes from an injected `Now` and
K's `Tick(ctx, now)`. No test sleeps.

- **One cold call.** `Planner.Plan` makes exactly one `backend.Run` per
  round through a plain `backend.Backend`, charges the usage cap once, and
  never touches the warm session.
- **Code owns identity.** A fake reply that writes its own task id,
  provenance or participant email has them overwritten. Emails always come
  from the roster.
- **Validation rejects, creates nothing.** A table test covers each rule in
  §4 (monitor/pipeline archetype, empty criteria, an unknown or level-B
  function, a context source above R, an outward step below A, too many
  asks for the rate cap, a past deadline, a cyclic skeleton, a missing
  anchor, too many judgment points, an unknown trigger kind, a count
  trigger on a non-critical criterion). For each: the store holds no task,
  and the approvals queue holds no envelope.
- **Ask before dispatch.** A goal naming "Sam" with two roster Sams, and a
  fake reply that sets `Questions`, each return `Result{Questions}` with no
  task and no envelope. Re-submitting with `answers` produces a plan.
- **Single action.** A fake plan whose authored shape has nothing to track
  (no participants, nothing to wait on) fails J's `NeedsRecord`, yields
  `SingleAction`, and creates no task record. No classifier call is made
  anywhere on the path: the only model call is the planning pass itself.
- **Procedural hook, concurrent with the skills lookup.** `NoProcedures`
  is called exactly once per round, before the model call. A test with a
  blocking fake skills lookup and a blocking fake `ProcedureLookup` shows
  both have started before either is released, so neither waits on the
  other. `Match` receives the raw `Request`, never the built `PlanEnv`. A
  stub that returns a match is observable in the plan's provenance (the
  shape Q will fill).
- **Schema in parallel with the fan-out.** Creating a task spawns the
  schema designer once. A fake designer that blocks does not delay ask
  staging, and a task whose designer never returns still drafts (with the
  fallback schema and a gap) once it reaches `synthesizing`.
- **Asks are envelopes.** A validated 3-person plan creates one task and
  exactly three pending `gmail.send_message` envelopes with `Origin: p1`,
  the task evidence refs, the explicit `ExpiresAt`, and the task tag in the
  subject. **Zero** connector calls happen until each is decided yes. A
  simulated crash mid-staging followed by a restart yields exactly three.
- **Observer.** Approving an ask records its message id and thread id on
  the participant and moves the task to `dispatched`. An observer error does
  not change `decideAndExecute`'s reported outcome. An expired ask is
  reconciled to `not_sent` on the next tick, and all-expired fires
  `no_asks_sent` once.
- **Replies drive state through the record.** N-shaped signals written
  into the record mark participants terminal. Nothing happens to the
  timers until the handler's own next timer firing, which reads the record
  and cancels those participants' reminders. A decline on a critical
  criterion fires `reply_declined` on the next `check_inputs` firing. Two
  differing values on a `Material` criterion fire
  `replies_conflict_material` and the task ends `awaiting_ceo`. A
  delegation stages nothing. No code path from N's ingestion reaches the
  fan-out handler or the clock directly.
- **No partial proceed.** Past the deadline with one participant pending,
  the task escalates (`deadline_passed`) and does not synthesize.
  `water tasks proceed` then synthesizes, with that participant as a gap.
- **Delivery.** All criteria met yields `delivered`, one `task`
  notification, and a report listing sources. An `unmet` criterion appears
  as a gap. A `contradicted` criterion yields `escalated`. No envelope is
  staged after the asks, in any branch.
- **Default reminder policy sends nothing.** Under K's `notify_ceo`, a
  non-responder produces CEO notifications and zero additional envelopes.
- **Config.** Enabling the planner without `ask_ttl_seconds` fails
  `config.Load`, and `tasks.planner.*` / `tasks.fanout.*` keys are
  recognized, not silently ignored.
- **Scenario test** (`internal/gateway/scenario_fanout_test.go`, in the
  style of `scenario_s13_test.go`: `newHarness`, the real HTTP surface,
  the real gate/approvals/store wiring, a seeded roster, fake Gmail,
  `backend.Fake`). The CEO posts "ask Dana, Theo and Priya for their Q3
  hiring plans by Friday and summarize" → questions come back first when
  the goal is ambiguous → answered → three envelopes → two approved, one
  left to expire → two fake replies ingested through N's real path (one
  deferral) → K ticks to the deadline → escalation → `water tasks proceed`
  equivalent → L's drafter and verifier ticks draft and judge from the
  record, and the critic (fake) returns met/unmet → the report is
  delivered to the CEO with the expired ask and the deferral listed as
  gaps. Somewhere in the middle, the daemon's store is closed and reopened
  with fresh K, N and L instances, and the run completes from the record
  alone. Assert the audit log verifies at the end, and assert that the
  only executed outward calls were the two approved asks.
- **Invariants.** A guard test checks that `internal/planner` imports none
  of `approvals`, `gateway` or the connectors (it cannot stage or send), and
  that `go.mod` gains no requirement in this slice.
- `go vet ./...`, `go test -count=1 ./...` and
  `CGO_ENABLED=0 go build ./cmd/water` pass, as always.

## Invariant checks (`docs/WORKFLOW.md`'s list, for what O touches)

- No connector function runs without a gate permit. Every ask and any
  reminder is a level-A envelope that executes only through
  `decideAndExecute`. The new observer is read-after-the-fact.
- Untrusted content never authorizes an action: replies (N) update state
  only, and a plan from a tainted session is flagged on every envelope it
  stages.
- P2 never plans and never reaches level A. Planning is P0 and staging is
  P1.
- No metered call and no new dependency. The static build still works.
- The audit log still verifies after the scenario test.

## Task list (for the Plan stage to confirm)

1. `internal/planner`: the `Plan` types, `Validate` with every table case,
   and a roster view that reports zero/one/many matches.
2. `Planner.Plan`: the concurrent `PlanEnv`/`ProcedureLookup` queries, the
   cold call, code-owned identity, question merging, the `NeedsRecord`
   single-action result, and the `ProcedureLookup` hook with
   `NoProcedures`.
3. Config keys and the fail-loudly rules.
4. Create (with the initial `deadline` and `check_inputs` timers), the
   schema-designer spawn, two-phase ask staging, and the pull
   reconciliation for denied/expired asks.
5. The `decideAndExecute` observer, and the fan-out observer.
6. The K handler, reading the record on its own timers: reminder
   bookkeeping against the participant statuses N wrote,
   `replies_conflict_material` and `reply_declined`, the deadline, the
   structural proceed rule and reminder payloads.
7. The delivery notification when the record reaches `delivered`, and
   end-to-end wiring of L's drafter/verifier loops for fan-out tasks.
8. Surfaces: `POST /v1/task-records`, `tasks.plan` (connector plus grant plus H's
   `delegation` skill), `water tasks new|proceed|abandon`, and the
   code-rendered plan read-back.
9. The scenario test.
10. Docs: `docs/architecture.md`, `docs/known-gaps.md` (no in-thread reply,
    no batch approval of asks), and the `docs/EVOLUTION_PLAN.md` log entry.

One task per commit, with the gates before each.

## Dependencies

- **Hard: H.** The planner's prompt uses H's skill descriptions, and
  `tasks.plan` joins H's `delegation` skill.
- **Hard: K.** It provides the handler interface, the timers, the reminder
  policy and `Clock.Escalate`. O registers the first real handler.
- **Hard: L.** Its drafter and verifier (synthesis, the structural and
  judgment checks, the critic) and its schema designer must exist, and it
  defines the reflection-point shape the plan's steps use. This is a build
  dependency. At runtime O calls none of L's task-path pieces. They pick
  tasks up from the record.
- **Hard: N.** It provides the reply signals and correlation. O adds the
  subject task tag that N's correlation can use.
- **Transitively: J** (through K, L and N), which provides the task record,
  transitions and audit trail. **F/G** (through H). **I** (through L). The
  plan's context sources may name `facts.get` once I has granted it, but O
  does not require it.
- **Downstream:** P adds monitor and pipeline as selectable archetypes to
  the same planner and validator. Q fills `ProcedureLookup` and
  `SchemaLookup`, and promotes
  completed fan-out tasks, including their recorded deviations from the
  plan, into procedure memory.
