> **Status (2026-09-25): planning only. No code is authorized.** This pass
> wrote this overview and eleven sub-slice specs, `docs/slices/F.md`
> through `docs/slices/Q.md` (M is skipped because Slice M already exists).
> It changed no `.go` or `.swift` file and added no dependency. Each
> sub-slice is built in its own session, following `docs/WORKFLOW.md`'s
> Explore → Plan → Approve → Implement loop. None may start until the owner
> has approved its spec and every slice it depends on (§2) is verified.
>
> **Owner amendment (2026-09-25), also planning only.** The runtime
> execution model in these specs was drawn, and partly written, as a
> sequential pipeline: planner, then orchestrator, then reflection points,
> then composer, each handing off to the next. That was wrong. It is
> replaced throughout by a **shared-state model**: one durable record (the
> task record plus memory) that every component reads and writes
> independently, with no component handing off to another directly (§2.1).
> The build order in §1 and §2 is unchanged. See the matching owner-amendment
> entry in `docs/EVOLUTION_PLAN.md`'s log.

# Intelligence-layer planning: the F–Q sequence, the dependency graph, and the owner's open questions

This is the companion to the eleven sub-slice specs. It plays the same role
for them that `docs/slice-c-planning.md` plays for `docs/slices/C.md`: it
records the plan and the decisions, and it is not a build spec. It holds
five things:
- the order to build the sub-slices in;
- the dependency graph between them;
- the cross-slice mismatches this pass fixed, and the ones left for a Plan
  stage;
- the owner's open questions, copied verbatim from the design brief and
  left unresolved;
- the phrases in the brief that may have been corrupted when it was
  pasted.

**What the redesign is.** It extends the CEO twin's intelligence past its
current limits. Today:
- the decisions engine lets the model phrase prose, and nothing more;
- `internal/memory` is a council-era package that nothing imports;
- no work can outlive a single turn, whether multi-step or waiting on
  someone.

The brief's decisions are recorded in each slice as decided, not open:
- memory is structured, append-only and never stores source content;
- a skill is a name, a responsibility and a function list, never a context
  bundle;
- the analysis lane is a second output class next to the strict lane, not a
  loosening of it;
- the four archetypes are a fixed vocabulary;
- work splits into three roles: planner, orchestrator (the clock) and
  composer. Per the owner's amendment, they never hand off to one another:
  each reads and writes the one durable task record plus memory (§2.1);
- reflection points split into structural and judgment checks, and a
  judgment is never made in the same context as the draft. Structural
  checks are reads any caller makes at any moment, not a pipeline stage;
- there are two clocks: time belongs to code, content to the quality gate;
- promotion reuses Slice R's loop and does not add a second one.

**Letters.** A1–A4, B, C, D, E, M, R and V are taken (see the
"Slices and acceptance tests" list and the log in
`docs/EVOLUTION_PLAN.md`). F is the next free letter. The sequence runs F
through Q and skips M.

## 1. The sequence

Build in letter order. Each slice's dependencies come earlier in the list.
Where two slices are independent, as §2 notes, a session may take them in
either order, but CLAUDE.md's one-slice-per-session rule still applies.

**F: memory store** (`docs/slices/F.md`). F rebuilds `internal/memory` as a
structured, append-only fact store:
- the brief's seven record types;
- provenance that cites an `audit.Entry.Seq`, with `ApprovedBy` required
  unless the CEO stated the fact;
- time bounds, confidence, and a `Supersedes` link, so a correction
  invalidates the old record and never overwrites it;
- a never-store validator enforced in code on write and on read;
- memory scoped by twin id.

It is deliberately left unwired. *Depends on:* nothing in the redesign.

**G: memory access paths** (`docs/slices/G.md`). G wires F into the running
daemon:
- a deterministic core-memory block in `runtime.RoleSystem`, shared
  byte-for-byte with the prewarmer;
- `memory.search` (level R), plus `memory.propose` and `memory.invalidate`,
  which only create pending proposals;
- a CEO approval path through new `/v1/memory/*` endpoints that write their
  audit record first and bind `AuditSeq`;
- a replacement `water memory` CLI;
- the `memory.*` config keys re-admitted;
- the never-store boundary stated up front in `role.md` and the core
  block, as the second enforcement layer next to F's validator;
- the three write triggers: a CEO statement written live, a pattern only
  through a periodic consolidation pass, and a procedure only when its task
  is terminal (Q). Nothing is tied to "session end".

*Depends on:* F.

**H: skills** (`docs/slices/H.md`). H adds nine `twins/ceo/skills/*.yaml`
files. The schema holds exactly `id`, `title`, `responsibility` and
`functions`, and any other key fails the load. That makes "no fixed context
bundle" a property the schema enforces.
- The files are validated at startup against the manifest and the
  `quick.*` set.
- They are rendered once into a `## Skills` system-prompt block, so they
  cause no warm-session restart.
- A skill never widens access.
- It is mostly organizational and low-risk.

*Depends on:* G.

**I: the decisions analysis lane** (`docs/slices/I.md`). I adds a facts
ledger (`internal/facts`, `facts.get`/`facts.catalog` at level R, taint
carried forward from `company_finance`) and an advisory-only `Analysis`
type. That type is labeled as interpretation and structurally unable to
carry a staged action or become an envelope, which guard tests enforce.
The strict lane is unchanged, byte for byte. *Depends on:* H.
*Precondition:* the untracked `company_finance` connector, migration `0012`
and `store/finance_records.go` must be committed first.

**J: durable task record** (`docs/slices/J.md`). J adds `internal/tasks`:
- the four archetypes (`single_action` never creates a row);
- typed archetype state;
- acceptance criteria and stored escalation triggers, frozen after
  dispatch;
- a pure transition table over the brief's states;
- an audited single-writer `Service` with version compare-and-swap;
- the `task_records`/`task_criteria`/`task_triggers`/`task_events` tables;
- read-only `water tasks list/show` over `/v1/task-records`;
- the record as the shared state every task component reads and writes
  (§2.1), the pure `Structural` read they all share, and a set-once
  `OutputSchema`.

*Depends on:* nothing in the redesign.

**K: the orchestration clock** (`docs/slices/K.md`). K adds
`internal/taskclock`, one more independent ticker in the
`sync.Refresher`/`needsyou` shape:
- a store-backed `task_timers` table, durable across restarts, with
  overdue timers coalesced rather than replayed;
- retries with backoff, for R/D steps only;
- step timeouts;
- a reminder mechanism whose default policy notifies the CEO and sends
  nothing;
- time- and count-based escalation triggers;
- the single escalation sink, `Clock.Escalate`, a synchronous record
  write rather than a hand-off to the clock.

It makes no model call and adds no workflow-engine dependency. It owns
time only and never judges completion or quality. It has to survive
multi-day waits and daemon restarts and act on partial, asynchronous
replies. That is a different problem from one background job reporting
back into a live session. *Depends on:* J.

**L: composer and reflection points** (`docs/slices/L.md`). L adds
`internal/compose`, one composer for both task and standalone requests,
built as independent pieces rather than a stage-by-stage pipeline:
- **structural checks**: pure code, and reads rather than a stage. Any
  caller evaluates them at any moment;
- a **schema designer**: a separate sub-agent invocation that retrieves or
  designs the output's structure in parallel with data gathering;
- a **drafter**: a sub-agent invocation that runs grounded extraction,
  with every quote verified as a verbatim substring of source text that
  code holds, then a lexical pass for candidate conflicts, then synthesis
  from verified claims only, slotted into the prepared schema;
- a **verifier**: runs a separate-context `ColdCritic` behind a `Critic`
  interface, which fails closed, and routes the task, with refinement
  capped at 0–2 passes and gaps built by code.

On the task path, the drafter and verifier each observe the task record
and never call each other. It also defines the `ReflectionPoint` shape.
*Depends on:* I and J (hard), K (soft, through an `Escalator` interface).

**N: human-reply ingestion** (`docs/slices/N.md`). N adds
`internal/replies`. A reply arriving in the agent's mailbox is:
- correlated to its ask by RFC `Message-ID`/`In-Reply-To` headers, never by
  Gmail thread id, subject line or model judgment;
- stripped of quoted history;
- read by one cold extraction call and verified field by field against
  verbatim quotes;
- written straight into the shared task record (J's participant status
  and criterion evidence) the instant it arrives. It is not a round trip
  to the orchestrator: K's timers read the record on their own schedule.

A reply is never an instruction. N hooks the existing agentmail Watcher
before triage and adds no poller. *Depends on:* J.
*Owner approvals before build:* granting `agentmail.get_message` at level
R, and a live-API check of how the ask's `Message-ID` is captured.

**O: planner plus fan-out end to end** (`docs/slices/O.md`). O adds
`internal/planner`: one cold planning call, a separate sub-agent
invocation, with code owning identities and email addresses. The main
model reaches it by deciding, in its live reasoning, to call `tasks.plan`.
No classifier tier decides that. The plan is validated in code before
anything is created. If the instruction is ambiguous, O asks before
dispatch and persists nothing. Single actions create no record. O also
adds:
- the authoring-time trigger table;
- the `ProcedureLookup` hook, shipped as `NoProcedures`, queried
  concurrently with the relevant-skills lookup and never in a chain;
- the schema-designer spawn at record creation, which runs in parallel
  with the fan-out;
- `internal/tasks/fanout`, the first real K handler. It stages one level-A
  envelope per ask, observes execution in `decideAndExecute`, reads the
  record on its own timers, and never proceeds on a partial set without
  the CEO. It hands nothing to L: L's drafter picks the task up from the
  record. Delivery goes to the CEO only.

*Depends on:* H, K, L and N.

**P: monitor and pipeline archetypes** (`docs/slices/P.md`). The monitor:
- watches a closed, typed predicate over a `facts.get` fact or a count from
  an R-level list function;
- is evaluated by code, so a probe makes zero model calls;
- treats "unknown" as never "clear";
- fires once each time its threshold is newly crossed;
- must have a stop rule.

The pipeline:
- runs strictly sequential steps of four kinds (`read`, `draft`, `outward`,
  `compose`);
- passes refs between steps, never content;
- carries taint for the whole task.

Both extend O's planner and validator rather than forking them. Compose
steps and judgment points are picked up from the record by L's drafter
and verifier, never called by P's handler. *Depends on:* O and I.

**Q: procedural memory and promotion** (`docs/slices/Q.md`). Q mirrors
Slice R's five-stage promotion loop for workflow plans:
1. candidates are grouped from closed, delivered tasks by a strict
   code-computed fingerprint;
2. one cold call drafts the procedure;
3. validation reuses O's plan validator and rejects any skeleton that
   claims more than actually ran;
4. the owner approves with a default-refuse y/N, which writes a learned
   file plus an F `procedure` record;
5. reversible auto-demotion.

Q also fills O's `ProcedureLookup` hook and L's `SchemaLookup` hook with
never-matching retrievers until the owner defines "same shape". Only
terminal tasks ever contribute. *Depends on:* F, G and O.

## 2. The dependency graph

There are two independent tracks, which meet at L.

- **Memory and capability track: F → G → H → I.** Each is a hard
  dependency of the next. F has no dependency inside the redesign.
- **Task track: J → K, and J → N.** J has no dependency inside the
  redesign. K and N each depend only on J and are independent of each
  other: N reaches K through an `Escalator` interface that K's
  `Clock.Escalate` satisfies at wiring time. J and F are independent of
  each other, except that both may need a migration number, which is
  chosen at commit time.
- **The join: L** needs I (the facts ledger, the shared number tokenizer,
  `analysis.Composer` for the labeled interpretation section) and J (the
  task record and transition table). It uses K only through an interface.
- **O** needs H (skill descriptions, the `delegation` skill), K (the
  handler interface, timers, the escalation sink), L (synthesis and the
  critic) and N (reply signals and `RecordOutboundAsk`). It depends
  transitively on everything before it except that I is optional: a plan
  may name `facts.get` but does not require it.
- **P** needs O and I. **Q** needs F, G and O. Nothing depends on P or Q,
  and Q uses P's archetypes only when P exists.

The critical path is F → G → H → I → L → O → Q. J, K and N can go
anywhere before O once their own dependency (J) is met. The letter order is
one valid topological order. It front-loads memory because the brief calls
memory foundational.

Other preconditions:
- **Before I (and so before P):** commit the `company_finance` connector.
- **Before N:** the `agentmail.get_message` grant, and the live `Message-ID`
  check.
- **Migration numbers:** F (if SQLite), G (if SQLite), J, K, N and Q may
  each add a migration. Every spec leaves the number to be chosen at commit
  time, because concurrent sessions collided on migration numbers twice
  during Slice R.

### 2.1 Build order is not runtime order: the shared-state execution model (owner amendment, 2026-09-25)

The graph above is a **build-order** constraint. You cannot build the
composer before the task record exists, and the amendment does not touch
that. What it corrects is the **runtime execution model**: how the
planner, the orchestrator (K), reply ingestion (N), the structural checks,
the judgment critic and the composer interact once all the code exists and
a real task is running. The earlier drafts drew that as a sequential
hand-off pipeline. It is instead:

- **One durable shared record.** The task record (J's tables, K's timers,
  N's ask ledger and reply signals, the composed draft and verdicts) plus
  memory (F/G/Q) is the only state the components share. Every component
  reads it and writes it **independently**, under J's version
  compare-and-swap. **No component hands off to another directly.** None
  calls another to move a task along, waits on another's return value, or
  is "next" after another. J's state sequence is the order the record
  passes through, not a chain of calls.
- **Who writes, and on what trigger** (fan-out, O §6):
  - the planner writes the record once, then stops;
  - the ask observer writes when an envelope executes;
  - N writes a reply's structured signal the instant it arrives;
  - K's timers fire on their own schedule and read the record as it stands;
  - L's drafter and verifier each pick up the state they own
    (`synthesizing`, `verifying`) from the record on their own loops.
- **Structural checks are reads, not a stage.** "Has everyone replied or
  timed out?" is J's pure `Structural` read. Any caller asks it at the
  moment it needs to: the clock deciding whether to proceed, the verifier
  checking readiness, `water tasks show`. None of them waits for another
  to have "run the check".
- **Reply ingestion is not a round trip.** N's write is not a control-flow
  step back to the orchestrator. K is never told. Its timer observes the
  same record independently.
- **Classification is not a tier.** Recognizing that a request needs the
  task machinery at all is the main model's live reasoning, the same
  reasoning that decides any tool call (`tasks.plan`). There is no
  dedicated classifier pass or model, which would repeat the Tier 1
  mistake Slice R retired (J §1, O Principle).
- **The two planning-time lookups run concurrently.** The relevant-skills
  lookup and the procedural-memory lookup (retrieve, or else generate) are
  independent queries. They start together and are never chained (O §2).
- **Separate sub-agent invocations are a design requirement.** The
  planning pass, the report's structural schema, the draft and the
  judgment critic are each spawned as their own invocation: a cold call in
  a fresh context that returns a structured summary, never its transcript.
  The main agent does not do them all itself in one serial chain. This
  mirrors this project's own coding-agent precedent (O Principle, L
  Principle).
- **The output schema is designed in parallel with data gathering.** A
  report's structure is itself a retrievable procedure. It is retrieved or
  designed at record creation, alongside the fan-out/collect phase, and
  written once. Content is slotted into it once content exists (J §2, L
  §3.0, O §6).
- **The orchestrator owns time only.** K never judges completion or
  quality. That verdict is L's verifier's, written into the record. K's
  problem also differs from one background job running next to one live
  session: it must survive multi-day waits and daemon restarts, and act on
  partial, asynchronous human replies, with nothing to "report back" into
  (K Principle).
- **Memory has the same shape.** Never-store is enforced twice: F's
  validator at the write path, and the boundary stated up front in
  `role.md` and the core block (F §3, G §3a). Memory is written on three
  triggers, none of them "session end": a CEO statement live, a pattern
  through a periodic consolidation pass, and a procedure once its task is
  terminal (G §3b, Q).

None of this resolves any open question in §4. It only fixes the
execution-model framing around them.

## 3. Cross-slice consistency

The eleven specs were drafted in parallel. This pass read them against each
other.

### 3.1 Fixed in this pass

- **Trigger kinds.** J's closed `TriggerKind` set and `task_triggers`
  `CHECK` held only the brief's five kinds. K, N, O and P used names outside
  it (`deadline_passed`, `step_timeout`, `retries_exhausted`,
  `reply_unmatched`, `reply_declined`, `no_asks_sent`, `probe_unavailable`)
  or variants of J's own names (`reminders_exhausted`, `reply_conflict`,
  `reply_sensitive`).
  - J's set is now the union.
  - K, N and O use J's canonical names: `reminders_exhausted_critical`,
    `replies_conflict_material` and `sensitivity_match`.
  - P's reconciliation bullet now records that the inconsistency it found
    has been reconciled at spec level.
- **Participant statuses.** O used `staged`, `sent` and `not_sent`, and N
  wrote its own disposition names straight into J's archetype state.
  - O now uses J's `pending`/`asked`, and J's set gains `not_sent`
    (terminal) for denied or expired asks.
  - O's proceed rule defers to `ParticipantStatus.Terminal()`.
  - N now maps its dispositions onto J's statuses explicitly (see §3.2 for
    the two it cannot map).
- **Terminal states.** K listed `escalated` as terminal. J makes only
  `delivered` and `abandoned` terminal and treats `escalated`/`awaiting_ceo`
  as parked. K now says so, which also covers P's "an escalated monitor
  must not probe" point.
- **The `version` column.** K said it might add a `version` column. J
  already specifies `task_records.version`, and K now says so.
- **Contradicted criteria.** L said J had no `contradicted` value. J
  carries it as `FlagReason: contradicted` on an `unmet_flagged` criterion,
  and L's write-back mapping now uses it.
- **HTTP routes.** O used `POST /v1/tasks` and `GET /v1/tasks/<id>`, which
  collide with the existing turn-cancel route. J namespaces them as
  `/v1/task-records`, and O now does too.
- **Who applies a reply signal.** O said its handler applies reply signals
  "(not N)". J, K and N all assign that write to N. O now has N apply the
  signal. Under the owner's shared-state amendment (§2.1), nothing "reacts
  to the bumped version": the handler reads N's write the next time one of
  its own timers fires.
- **Skill ids.** I named H's skills `analysis/modeling` and
  `decision-support`. H's ids are `analysis` and `decision_support`.
  H also credited J and K with adding task tools to `delegation`, and I
  with extending `monitoring`. Neither is true in those specs. Only O adds
  one (`tasks.plan`).
- **System-prompt block order.** H put the style block before G's core
  memory. G places the core block after `role.md` and before `## Style`.
  H now follows G: `role.md`, core memory, style, skills.
- **Monitor package path.** P put the monitor under
  `internal/taskflow/monitor.go`. O's handler lives at
  `internal/tasks/fanout`, so P now proposes `internal/tasks/monitor`.
- **Ask-before-dispatch.** P said an ambiguous monitor instruction
  produces a task in `awaiting_ceo`. O, whose mechanism P extends,
  persists nothing on ask-before-dispatch. P now follows O.
- **Q against O.**
  - Q called O "not written yet", but O was drafted alongside it.
  - Q named its hook `Retriever`/`NoMatch`, where O's names are
    `ProcedureLookup`/`NoProcedures`.
  - Q now points at O's signature and says O's wins.
- **Correlation by thread id.** J's `ThreadRef` comment said it was for
  N's correlation. N correlates by RFC headers, never by Gmail thread id,
  and J's comment now says so.

### 3.2 Left for a Plan stage (not resolved here, because each is a design choice)

- **Who evaluates `replies_conflict_material`, and under which interim
  rule.**
  - N escalates every exact disagreement on any requested field.
  - O's handler escalates only on a criterion the planner marked
    `Material`.
  - J's `Criterion` has `Critical` but no `Material` field.

  O's Plan stage picks one evaluator and one interim rule. Both are
  interim answers to the owner's open "material conflict" question below.
- **N's `partial` and `unclear` dispositions** have no J
  `ParticipantStatus`. J adds them, deciding whether each is `Terminal()`,
  or N maps them, at N's Plan stage.
- **The requested-fields list per ask and the field-to-criterion binding.**
  N requires J to carry them and O to author them. O's `Participant` now
  notes this, but the shape is not designed. It is settled with J, N and O
  before N starts.
- **J's `created → awaiting_ceo` edge** (ask-before-dispatch) is unused,
  because O and P persist nothing on a question. Keep or drop it at J's
  Approve.
- **K's step execution.** K's `Handler.OnTimer` is pure, and its `Outcome`
  has no step-execution field, but monitor probes and pipeline steps need a
  `gate.Invoke`. P proposes a minimal `Executor` seam, plus a
  non-escalating `Clock.Notify` for breaches. This is confirmed against the
  built K. Under the amendment the seam covers mechanical gate calls only,
  never compose or judgment steps, which L's loops pick up from the record.
- **Criterion identity.** J keys criteria by `Ord int`, while L and O use
  string ids. The mapping is settled at L's and O's Plan stages.
- **Where the composed output lives.** The shared-state amendment settles
  that the in-progress draft, its claim references and the critic's reasons
  must be in the durable task record, because the verifier reads what the
  drafter wrote and a restart can fall between the two. The storage is
  still open: a J column, a J-adjacent table, or a V thread for the
  delivered copy. This is settled between L and J.
- **New Plan-stage details from the amendment** (mechanics, not owner
  policy):
  - the cadence of the fan-out handler's `check_inputs` timer
    (`tasks.fanout.check_seconds`);
  - whether L's drafter and verifier are one loop or two, and their
    polling interval;
  - which writer inserts the delivery notification: the verifier's
    delivering write, through K's notification path, or needsyou's own
    tick noticing a `delivered` record;
  - how P's monitor close writes its code-built summary and makes two
    transitions in one K `Outcome`;
  - the consolidation pass's observation sources, and whether its
    `pattern` statement text is code-built or drafted by one cold call
    (G §3b).
- **The `monitoring` skill's function list** is not named by P, which is
  where H's table says it gets filled.
- **Q's requests of other slices:**
  - O persists the authored skeleton and records `from_procedure`;
  - J's `Service` gains an `OnTerminal` observer;
  - G's `memory.propose` refuses `procedure` records.
  
  Each is confirmed at Approve.

### 3.3 Repo facts every slice relies on (found while drafting; re-verify at build time)

- **`compute_runway` is gone.** `internal/decisions/budget_request.go` is
  deleted in the working tree, replaced by `company_finance`. The only
  registered `internal://` compute left is the demo twin's
  `compute_deal_health`.
- **`gsheets.cashPosition` stores an empty cell as a real `0`.** I records
  this in `docs/known-gaps.md` at build time and flags such facts as
  `zero_may_mean_missing`. It is not fixed silently.
- **Retired config prefixes are ignored without an error.**
  `internal/config`'s `retiredPrefixes` silently ignores `orchestration.`,
  `skills.` and `memory.`.
  - G re-admits `memory.` deliberately.
  - K, N, O and P put their keys under `tasks.*`.
  - H adds no config keys.
- **The name "task" is already taken** by in-flight turns (`d.tasks`,
  `POST /v1/tasks/{id}/cancel`, `thread_messages.task_id`). J namespaces
  around it.
- **The old council's `internal/orchestrator` was deliberately deleted.**
  K is `internal/taskclock` and must not revive the old name.
  `docs/CONTEXT.md`'s tree also lists an `internal/scheduler/` that does
  not exist.
- **`approvals.DefaultTTL` is 15 minutes**, and expiry counts as a no.
  Every staged ask, reminder or reaction sets `ExpiresAt` explicitly.

## 4. Open questions for the owner

Copied verbatim from the design brief and **not resolved**. No slice picks a
default for any of them. Where a slice has to ship some behavior before the
owner decides, it ships the conservative interim named in that slice's own
open-points section and labels it as interim: escalate to the CEO, send
nothing, never proceed on a partial set. The brief's heading and list:

> ## Open questions — surface these explicitly in the plan; do not resolve them with invented defaults
>
> - Reminder count/cadence per participant, and whether it should vary by
>   seniority or relationship.
> - Quorum rules: when is "4 of 5 replied" enough to proceed vs. wait for
>   the fifth.
> - What counts as a material conflict between replies, warranting
>   escalation vs. neutral reporting.
> - Whether the twin may nudge in the CEO's name, escalate to a
>   non-responder's manager, or only ever escalate to the CEO itself
>   (exact wording garbled in the source paste — surface all three
>   possibilities as the open question, don't silently pick one).
> - Late-reply policy after a report is already delivered: amend, send a
>   delta, or hold for next cycle.
> - Confidence thresholds for any of the above — no calibrated signal for
>   this exists off the shelf; treat any number as provisional until
>   checked against real CEO accept/edit/reject outcomes.
> - Critic architecture: same model with fresh context, vs. a genuinely
>   different model, vs. a rubric-scored judge — no benchmark exists yet
>   for Water's specific task shape.
> - Sensitivity tiers: which outputs always require CEO review before
>   sending regardless of what the gate concludes.
> - What "same shape" means for procedural-memory retrieval (§4's Planner
>   step) precisely enough to avoid both over-matching (reusing a plan
>   that doesn't actually fit) and under-matching (re-planning genuine
>   repeats).

On the escalation-target item in particular, all three readings stay open:
1. the twin nudges a non-responder in the CEO's name;
2. the twin escalates to the non-responder's manager;
3. the twin only ever escalates to the CEO.

K, N, O and P each build only reading 3, and each labels it as a safe
interim, not an answer.

Each sub-slice spec also lists its own narrower owner decisions (its "Open
for the owner" or "Open design points" section). The ones most likely to
block a build:
- **F:** storage backend, markdown or SQLite. SQLite means amending
  `docs/CONTEXT.md` principle 6.
- **F:** the names of the sensitivity tiers.
- **G:** the gate level for `memory.propose`/`memory.invalidate`.
- **G:** which records are core.
- **H:** description-only skills versus per-turn tool scoping.
- **I:** the metric catalog beyond `cash.*`, and any period history.
- **J:** keeping the word "task" (namespaced) or renaming the concept, and
  what `links.go`'s undefined `"job"` node type was meant to be.
- **K:** the reminder-envelope TTL.
- **N:** the `agentmail.get_message` grant.
- **N:** a subject-line task tag as a fallback correlation rule.
- **O:** the level of `tasks.plan`.
- **O:** the ask TTL.
- **O:** whether the CEO confirms the whole plan before any ask is staged.
- **P:** whether monitors may run indefinitely.
- **P:** P1 versus P2 plus `auto_allowlist` for background probes.
- **Q:** the promotion and demotion thresholds, which are R's numbers
  carried over provisionally.
- **Q:** whether a CEO may explicitly promote a single task.

## 5. Possibly corrupted in the source paste: for a human to double-check

The owner's brief was pasted through a tool that dropped letters and
syllables. The clear cases were reconstructed before drafting. The drafting
agents flagged these as still uncertain, and none of them was silently
repaired:

1. **The escalation-target open question** (§4 above). The garbled clause
   is kept as three readings and not resolved.
2. **"## 4. Task/workflow arch"** probably lost the end of "architecture"
   (flagged by P). Nothing depends on it.
3. **"Planner: the model, doing a distinct reasoning part"**: "part" may be
   a corrupted "pass" (flagged by O and P). Both specs treat it as a
   separate planning pass either way.
4. **§8 "(§4's memory taxonomy already has this slot)"**: the memory
   taxonomy with the `procedure` type is in §1, not §4 (flagged by Q). This
   is either corruption or mis-numbering. Q reads it as §1.
5. **§4 Planner bullet, "this exact matching criterion is an open question,
   see §6"**: §6 is completion detection and escalation. The matching
   question is in the unnumbered open-questions list (flagged by Q). This
   is either corruption or mis-numbering.

Also noted, but not treated as corruption:
- **§4 against §2 on skills.** §4 says "do not assume a skill's default
  bundle covers it", while §2 says a skill has no default bundle. It reads
  as a leftover from the framing §2 corrected. O follows §2.
- **The "94% of wrong answers" figure in §5** is carried as the brief's
  own claim. It was not re-verified in this pass, and L's design depends
  only on the decided rule that a judgment is never made in the same
  context as the draft, not on the number.
- **"Sensitivity" (§1)** is named as a field with no levels. F ships a
  provisional placeholder set (`normal | sensitive | restricted`) until the
  owner names the tiers.
- **"Government ID numbers" (§1)** names no jurisdictions. F covers US SSN
  only, pending the owner.
