# Slice Q: procedural memory and the promotion loop for workflow plans

**Planning only. No code is authorized yet.** This is the last sub-slice of
the intelligence-layer redesign (F through Q). The overview and sequencing
doc for that redesign lives alongside these specs and holds the full
open-questions list. This file is the spec to build Q against once the owner
approves it, following `docs/WORKFLOW.md`'s Explore → Plan → Approve →
Implement loop. Q depends on F (the `procedure` record type), G (the live,
audited memory write path and the `water memory` CLI) and O (the planner and
its procedural-memory hook). Through O it also depends on J, K, L, N and H.
Nothing in the redesign depends on Q.

Builds on everything through O, plus Slice R's growth loop
(`internal/nervous/promote`, `nervous.LearnedIntentShouldDemote`,
`store.SetIntentState`, `water intent draft|promote|demote|enable`). Before
Q, every task the planner authors starts from nothing, and whatever a
finished task taught the twin is lost when the task closes. Q adds the loop
that turns **what actually happened** in repeated completed tasks into an
approved, reusable `procedure` memory record, and the lookup that lets the
planner start from one instead of re-planning. It adds no new way to act.
Nothing in Q can send, post, call, delete or spend.

## Principle

**A promoted procedure changes how the twin plans, never what it is allowed
to do.** A procedure is a starting skeleton for O's planner. A task built
from one still passes O's full plan validation, still asks the CEO before
dispatch when the instruction is ambiguous, and still stages every outward
step as its own gate-approved envelope. Promotion is not a standing grant,
and a procedure cannot carry one (standing grants stay deferred, per
`docs/known-gaps.md`).

**Promote the trace, not the guess.** What gets promoted is what the
completed tasks actually did, read from J's record and event trail: steps
that ran, steps added or skipped, retries, participants appended by
delegation, criteria verdicts, triggers that fired. It is not the skeleton
O originally authored. Code checks this, not the drafting model (§4).

**Only terminal tasks count.** Whether a plan actually worked isn't known
until the task is over. So a task contributes to procedural memory only
once its record has reached a terminal state (`delivered`), never while it
is running or parked, and never at the end of a conversation or session
(the warm session has no such boundary). This is trigger (c) of the three
memory-write triggers the owner's amendment fixes (2026-09-25; G's
"When memory is written"). Q's qualification rule (§2) enforces it in
code.

**One promotion loop, not two.** Q reuses the exact five-stage shape of
Slice R's intent promotion loop (`docs/slices/R.md` §16): candidate
detection, a model-drafted proposal, validation, explicit owner approval,
and auto-demotion with manual demote/enable. Every stage below names the R
stage it mirrors. Where R's loop is specific to intents it is mirrored, not
called, and nothing in Q adds a second, differently shaped approval path.

## What exists today (read 2026-09-25)

Slice R's loop, read in full:

- **Candidates** (`promote/candidates.go`). `Candidates(ctx, store,
  intents.Shared, since, minRepeats, maxRows)` scans `route_log` through
  `store.QuickOnlyRoutes`, applies a row-level `qualifies` rule (owner
  main, quick-only, answered, no possible miss, eligible utterance, every
  tool `Learnable()`), groups by `tool_signature`, and keeps groups with at
  least `DefaultMinRepeats` (5) distinct turns on at least
  `MinCandidateDays` (2) distinct local days. The candidate id is the first
  12 hex characters of `sha256(signature)`, and `ValidCandidateID` checks
  that shape before any path join. Detection is read-only and works with
  the flag off.
- **Draft** (`promote/draft.go`). `Draft(ctx, backend.Backend, model, spec,
  cand, samples)` makes exactly one cold `backend.Run` with a fixed system
  prompt (`Role: "promote.draft"`). A plain `backend.Backend` has no warm
  session, so the warm session cannot be touched by construction. The reply
  is YAML with fences stripped (`stripFencing`). Code then overwrites `id`,
  `origin` and `provenance` (candidate id, turn ids, repeats, `drafted_at`,
  `draft_model`), so the model never claims its own identity or history.
  `WritePending` writes `$WATER_HOME/twins/<twin>/intents/pending/<candidate>.yaml`
  (0o700 dirs, 0o600 files).
- **Validate** (`promote/validate.go`). `ValidateLearned(fileBytes, reg,
  negatives, maxLearned)` reuses the real registry validation
  (`reg.ValidateAsOverlay`) and the real Tier 0 matcher (`nervous.DryMatch`)
  rather than reimplementing either. It hard-rejects non-read intents
  ("actions can never be promoted"), non-`Learnable()` functions, text
  slots, deny words, the active-learned cap (`DefaultMaxLearned`, 20),
  ambiguity against other intents, failing own tests, and false accepts
  against the held-out eval negatives.
- **Approve** (`internal/cli/cmd_intent.go` `intentPromoteCmd`). It refuses
  when `router.promotion.enabled` is off, rejects a malformed candidate id
  before any file I/O, shows the exact pending file the owner reviewed
  (never a fresh re-draft), asks a default-refuse `[y/N]` via
  `confirmYesNo`/`promptYesNo`, then calls `POST /v1/intents/promote`. The
  daemon re-reads the same file, re-validates it against the live registry,
  writes it to `intents/learned/` and reloads.
- **Demote.** `nervous.LearnedIntentShouldDemote(rows, minSamples,
  maxMissRatePct)` is a pure function: below `minSamples` it never demotes,
  otherwise it demotes above the miss-rate threshold with the reason
  `"auto: miss X% over N"`. `Nervous.maybeAutoDemote` calls it
  fire-and-forget after a possible miss is marked on a learned intent's row,
  over `store.IntentAnswered(id, 50)`, then writes
  `store.SetIntentState(id, disabled, reason, at)` and swaps in
  `registry().WithDisabled(...)` so the next turn is affected immediately.
  Errors are logged and never reach a turn. `intent_state`
  (migration `0010_router.sql`) is `{intent_id PK, disabled, reason, at}`,
  where a missing row means enabled. `water intent demote <id> [--reason]`
  and `water intent enable <id>` are the manual path, and they are not
  flag-gated.
- **Config**: `router.promotion.enabled` (false), `min_repeats` (5),
  `max_learned` (20), `demote_miss_rate_pct` (20), `demote_min_samples`
  (10).
- **Endpoints**: `GET /v1/route/candidates`, `POST /v1/intents/draft`,
  `/promote`, `/demote`, `/enable`, `/reload`, `GET /v1/intents`.

**Why Q cannot call it directly.** `promote` imports `nervous`, `intents`,
`reflex`, `tmpl` and `eval`. Its inputs are `route_log` rows and its output
is a Tier 0 intent file validated by the Tier 0 matcher. None of that
applies to a workflow plan. Q mirrors the stages and reuses only the small,
domain-free helpers (see §7).

From the sibling specs Q builds on:

- **F** gives `memory.Record` with `Type: procedure`, `Provenance{Trigger,
  SourceRef, AuditSeq, WrittenBy, ApprovedBy}` where
  `Trigger: approved_proposal` requires `ApprovedBy`, `Supersede` and
  `Invalidate` (permanent and never cleared), `ListByType`, `IsCurrent`, and
  `CheckNeverStore`. `SourceRef` must parse as `<scheme>:<id>`. F's record
  has a short `Statement` with a length cap and **no structured payload
  field**, which shapes §3's storage design.
- **G** gives the live write path bound to `audit.Log` and the replacement
  `water memory` CLI, and re-admits `memory.*` config keys.
- **J** gives the record Q reads: final `State`, `Archetype`, typed
  archetype state (`PipelineState.Steps` with `Status`/`Attempts`,
  `FanOutState.Participants` with `Status`/`DelegatedTo`), `Criteria` with
  `Status`/`FlagReason`, `Triggers` with `FiredAt`, `RefinementPasses`, and
  the append-only `task_events` trail with `audit_seq`. J's `task_events.detail`
  holds refs and statuses only, never content.
- **O** (drafted in parallel with this spec) defines the hook Q fills:
  `ProcedureLookup.Match(ctx, r planner.Request) (Procedure, bool, error)`,
  called once per planning round. It runs **concurrently with** the
  relevant-skills lookup and O's other `PlanEnv` reads, not after them,
  since neither depends on the other's result (owner amendment,
  2026-09-25; O §2). `NoProcedures` (always `ok=false`) is O's shipped
  default. §5 states what Q needs from that hook, so O's spec and this one
  agree at Approve.
- **L** defines a second hook Q fills: `SchemaLookup.Match(ctx, goal)
  (tasks.OutputSchema, bool, error)`, used by L's schema designer. A
  report's structural schema is itself a retrievable procedure (owner
  amendment, 2026-09-25; L §3.0), and it is retrieved at dispatch, in
  parallel with the fan-out/collect phase. L ships `NoSchemas`.

## Scope

### 1. The trace: what actually happened, computed in code

New package `internal/procedures`. Its first piece is a pure function over
a closed J task:

```go
// internal/procedures/trace.go — no I/O, no clock, no model
type Trace struct {
    TaskID      string
    Archetype   tasks.Archetype
    Outcome     Outcome        // §6's classification, from final state + fired triggers
    Authored    Skeleton       // the plan O authored, as persisted at creation (see below)
    Executed    Skeleton       // what ran: steps in completion order, with attempts
    Deviations  []Deviation    // Executed minus Authored, typed
    Functions   []string       // sorted, deduplicated manifest functions actually invoked
    Participants ParticipantShape // counts by terminal status; roster refs only
    Criteria    []CriterionOutcome // text + final status + flag reason; no evidence content
    FiredTriggers []tasks.TriggerKind
    FromProcedure string       // "procedure:<id>" when O built this task from one, else ""
    ClosedAt    time.Time
}

type DeviationKind string // step_added | step_skipped | step_retried |
                          // participant_added | criterion_unmet | refinement_pass
```

`BuildTrace(task, events)` reads only J's record and trail. It carries refs
and statuses, and never a reply body, quote, or evidence content, so it is
safe to hand to a model and passes F's `CheckNeverStore` by construction. A
test asserts that.

**The authored skeleton must be recoverable.** Deviations need a baseline.
J's `task_events.detail` for the `created` event holds refs only, and J
updates `archetype_state` in place, so the original skeleton is not
guaranteed to survive in J as written. Q therefore needs O to persist the
authored plan at creation, either in the `created` event's detail or as its
own ref (`plan:<id>`). Q states this as a requirement on O. If O's
implemented shape does not provide it, Q records the gap and falls back to
promoting `Executed` alone, with deviations empty. It does **not** guess a
baseline. This is flagged for Approve (open question 5).

### 2. Candidate detection (mirrors R stage 1, `promote.Candidates`)

```go
func Candidates(ctx context.Context, src TaskSource, fp Fingerprinter,
    since time.Time, minRepeats, minDays int) ([]Candidate, error)
```

**Row-level qualification**, like `promote.qualifies`, applied literally in
code. A closed task contributes only when all of these hold:

- its final state is `delivered`. `abandoned`, still-open and
  still-`escalated` tasks never contribute;
- every `Critical` criterion is `met`;
- no `critic_contradicted` or `sensitivity_match` trigger fired;
- it was not itself built from a procedure that has since been disabled or
  invalidated (a demoted procedure's instances must not re-promote it);
- its trace passes `CheckNeverStore`.

**Grouping** is by a `Fingerprint(Trace) string` computed through a
`Fingerprinter` interface. Q ships exactly one implementation,
`ExactStructural`: archetype, plus the sorted executed function set, plus
the participant count, plus the final criteria count, hashed. It is the
strictest structural key available from code alone. It deliberately
under-groups (two genuinely similar tasks that differ in one tool never
meet), which is the safe direction: at worst nothing is promoted. It does
**not** include "goal type", because deriving that needs a classification
Q does not want to invent. What "same shape" means is the owner's decision
(open question 1). The interface exists so that decision replaces the
fingerprint without touching the rest of the loop.

A group becomes a `Candidate` at `minRepeats` distinct tasks on `minDays`
distinct local days, the same two-threshold rule as R. The candidate id is
the first 12 hex characters of `sha256(fingerprint)`, with the same
`ValidCandidateID` shape check before any path join. `Candidate` carries up
to 10 sample task ids, as R's carries sample turn ids. Detection is
read-only and works with the flag off, like `water route candidates`.

**Thresholds.** Tasks are far rarer than turns, so R's 5 repeats on 2 days
may never trigger for a monthly fan-out. Q ships R's own values as the
provisional defaults (they are the values of the loop Q is reusing), labeled
provisional in code, and they have no effect while the loop's flag is off.
The real numbers are an owner decision (open question 2).

### 3. Draft (mirrors R stage 2, `promote.Draft`)

`water procedure draft <candidate>` → `POST /v1/procedures/draft`, a
CEO-initiated, P0, **cold** `backend.Run` with `Role: "procedures.draft"`
and a fixed system prompt. It never uses the warm session. The input is
code-built from the candidate's sample `Trace`s: the executed skeletons,
the deviations and how many of the samples each deviation appeared in, the
function set, participant shape, criteria outcomes and fired triggers.
Nothing in it is content.

The model returns one YAML **procedure file**:

```yaml
id: procedure.placeholder          # overwritten by code
applies_when: <one line: the situation this procedure is for>
archetype: fan_out                 # fan_out | monitor | pipeline; never single_action
steps:                             # the skeleton, in O's own step shape
  - {id: ask, function: gmail.send_message, reflection: none, outward: true}
  - {id: collect, function: "", reflection: structural}
  - {id: synthesize, function: "", reflection: judgment}
criteria:                          # templates the planner instantiates
  - {text: "...", critical: true}
triggers:                          # kinds only; params stay the planner's job
  - reminders_exhausted_critical
context_sources: [gdrive.search_files]
participants: {role: "project leads", count_hint: 5}   # see open question 6
output_schema:                     # optional; J's OutputSchema shape, as the samples used it
  kind: status_update
  sections: [{id: summary, title: Summary}, {id: by_lead, title: By lead}]
```

`output_schema` is taken from the schemas the sample tasks actually
recorded (J's `OutputSchema`), never invented, under the same
"promote the trace" rule. A procedure that carries one lets L's schema
designer retrieve it through `SchemaLookup` instead of designing one.

The shape reuses O's plan vocabulary exactly (archetype names, the step shape,
J's `Reflection` values, J's `TriggerKind` set). Q does not define a second
plan format. The field list above is illustrative until O's plan type exists.
The rule is that a procedure file **is** an O plan skeleton plus
`applies_when`, with nothing O would not accept.

Code then overwrites `id` (`procedure.<sanitized>_<6 hex>`, the same
collision-proof naming as `promote.sanitizeName`), `origin: learned` and
`provenance` (candidate id, sample task ids, repeats, days, `drafted_at`,
`draft_model`). The file goes to
`$WATER_HOME/twins/<twin>/procedures/pending/<candidate>.yaml` with R's
permissions. It is validated at once and the verdict is printed, exactly
like `water intent draft`.

### 4. Validation (mirrors R stage 3, `promote.ValidateLearned`)

`ValidateProcedure(fileBytes, deps)` reuses the real machinery, never a
parallel reimplementation, the same rule `ValidateLearned` follows with
`ValidateAsOverlay` and `DryMatch`. It hard-rejects when:

1. **O's own plan validator rejects the skeleton.** Q calls it directly: a
   legal archetype, non-empty criteria, only manifest functions, every
   outward step at level A, and J's `Task.Validate()` rules for the pipeline
   `DependsOn` graph.
2. **It claims more than happened.** Every step function must appear in the
   executed trace of at least one sample, and every step that ran in
   **every** sample must be present. A skeleton that drops a
   universally-run step, or adds one no sample ran, is a guess rather than a
   trace. This is Q's equivalent of R's "own tests pass" check.
3. **It tries to carry authority.** A step has any field that would
   pre-approve, batch, or skip an envelope. The schema has no such field,
   so this is a strict unknown-field rejection plus a guard test. A step
   naming a level-B function is also rejected.
4. **It removes a safety trigger.** A trigger kind that fired in any sample
   is missing from the procedure's `triggers`.
5. **It fails `CheckNeverStore`** on any text field. `applies_when`,
   criterion text and participant roles have F's length caps.
6. **The cap is reached.** Active (current, not disabled) procedures are at
   `max_active` (provisional 20, mirroring `DefaultMaxLearned`).
7. **Ambiguity or false accept**, through the live `Retriever` (§5). The
   sample tasks' goals must retrieve this procedure and no other, and a
   held-out set of negative goals must not retrieve it. While the retriever
   is `NoMatch` (§5), these two checks are vacuous. The validator reports
   them as **skipped, not passed**, so the owner sees they did not run.

### 5. Retrieval: filling O's planner hook

```go
// internal/procedures/retrieve.go
type Retriever interface {
    Match(ctx context.Context, req PlanRequest) (Match, bool, error)
}
type Match struct {
    ProcedureID string   // "procedure.<name>"
    RecordID    string   // the F memory record id
    Skeleton    Skeleton
    Why         string   // code-built, e.g. the matched fields; never model prose
}
```

`Retriever` is Q's name for the implementation behind O's
`ProcedureLookup` hook. O's signature (`Match(ctx, r planner.Request)
(Procedure, bool, error)`) wins, and the `PlanRequest`/`Match` shapes above
are adapted to it at Plan. `PlanRequest` is O's `Request` (the raw goal,
answers and time), not the built `PlanEnv`, so the lookup runs
concurrently with the skills lookup and depends on nothing it produces
(O §2). Q ships **`NoMatch`** (behaviorally the same as O's
`NoProcedures`) as the default and only production implementation: it
always returns `false`, so the planner always plans fresh. The same holds
for L's `SchemaLookup`. Until "same shape" is defined, Q's implementation
behind it also never matches, so every schema is designed fresh. That is the
conservative behavior the redesign decided on until the owner defines "same
shape" (open question 1). A second implementation lands only after that
decision, and it replaces `NoMatch` behind the interface without touching O.

**Candidate time and plan time see different facts.** A candidate is
grouped by what ran: executed functions, participant outcomes. At plan time
only the request exists: the goal text, named people, and perhaps a
provisional archetype. A matcher cannot fingerprint an execution that has
not happened. So the owner's "same shape" definition has to say which
plan-time facts count, and whether the planner first sketches a provisional
shape and then matches it. Q records this as part of open question 1 and
does not decide it.

**What a match does.** It seeds O's planner, and nothing is skipped: the
planner instantiates the skeleton (participants, parameters, trigger params,
concrete criteria), O's validator runs unchanged, ask-before-dispatch still
applies, and every outward step is still its own envelope. The task's
`created` event records `from_procedure: procedure:<id>` in its `detail`
(a ref, fitting J's rule), so demotion (§6) can find a procedure's
instances without any J schema change. Disabled or invalidated procedures
are never returned. `Match` filters on the live disabled set and on F's
`IsCurrent`.

### 6. Approval, storage and demotion (mirror R stages 4 and 5)

**Approval.** `water procedure promote <candidate-id>` mirrors
`intentPromoteCmd` line for line. It checks the flag
(`memory.procedures.promotion.enabled`, **off by default**; the key lives
under `memory.*`, which G re-admits), checks `ValidCandidateID` before any
file I/O, shows the exact pending file, asks a default-refuse `[y/N]`
through the existing `confirmYesNo`, and then calls
`POST /v1/procedures/promote`. The daemon re-reads the same file,
re-validates it against the live state, and only then:

1. appends a `KindPropose`-style audit entry (house audit-before-effect
   ordering) and takes its `Seq`;
2. writes the file to `procedures/learned/<procedure-id>.yaml`;
3. writes the F record through G's audited path: `Type: procedure`,
   `Statement` = `applies_when`, `SourceRef` =
   `procedure:<id>@<first 12 hex of sha256(file)>`, `Provenance{Trigger:
   approved_proposal, WrittenBy: "twin", ApprovedBy: "ceo", AuditSeq}`.

**Storage: the file is the body, the F record is the authority.** F's record
has no structured payload, and R's loop reviews and loads a raw file, so Q
keeps both. The learned file holds the skeleton. The F record is what makes
the procedure live, carries its provenance, and is what `memory.search` and
`water memory` see. A learned file with no current F record is ignored. A
file whose hash does not match the record's `SourceRef` fails loudly and is
skipped and reported, as R treats an invalid learned intent: owner-data
drift never blocks the daemon. If the owner picks F's SQLite backend, the
body could move into the database instead. That is open question 4, and
the file layout is the proposal.

**Re-promotion supersedes.** A candidate whose drafted procedure replaces
an existing active one with the same `applies_when` scope is promoted via
F's `Supersede`, so the old record is invalidated, not overwritten. Both
stay in `History`.

**Procedures only enter memory through this loop.** An F record of `Type:
procedure` must have a `procedure:` `SourceRef` whose file exists and whose
hash matches, and `Trigger: approved_proposal`. G's `memory.propose` cannot
create one. Q adds that check to the procedure write path. If G's
implemented `propose` accepts arbitrary types, Q adds the one-line rejection
there too, and says so in its commit.

**Demotion is reversible. Invalidation is permanent.** These are two
different acts, and Q keeps them apart:

- **Demote/enable** mirrors `intent_state`. It uses a new `procedure_state`
  table with the identical shape (`procedure_id PK, disabled, reason, at`),
  where a missing row means enabled, plus
  `store.SetProcedureState`/`ListProcedureStates`. The migration number is
  chosen at commit time and not reserved here, because concurrent sessions
  collided on migration numbers twice during Slice R. Q adds a new table
  rather than writing procedure ids into `intent_state`, so R's `water
  intent list` and registry `Disabled` set never see a non-intent id. The
  alternative (generalizing `intent_state` to a keyed kind) is open
  question 7.
- **Invalidate** is F's `Invalidate`, reached through G's `water memory
  invalidate` or `memory.invalidate`. It retires the procedure for good,
  and it is how the CEO says "that is simply no longer how we do this".

**Automatic demotion.** `ProcedureShouldDemote(outcomes []Outcome,
minSamples, maxBadRatePct int) (bool, string)` is a pure function with the
same contract as `LearnedIntentShouldDemote`: below `minSamples` it never
demotes, and above the rate it returns `"auto: bad X% over N"`. An
instance's `Outcome` is classified in code from J's final record. `good`
means `delivered` with every critical criterion met. `bad` means any of:
`abandoned` by the CEO, `critic_contradicted` fired, a critical criterion
`unmet_flagged`, or the CEO materially rewrote the seeded plan at
ask-before-dispatch. Whether each of those counts as bad is open
question 3, and the list is the proposal.

It runs the way `maybeAutoDemote` runs: fire-and-forget when a task with a
`from_procedure` ref reaches a terminal state, over that procedure's most
recent instances (a window of 50, as R). It logs errors and never surfaces
them to a turn, then calls `SetProcedureState(disabled)`, and `Match`
excludes the procedure from the very next plan. The trigger point is an
observer that `tasks.Service` calls after a terminal `Move`. J's service
gains a small `OnTerminal(func(tasks.Task))` registration in Q, since J has
no such hook today. `internal/tasks` must still import nothing from
`internal/procedures`, so the dependency points one way. This observer
runs only after the terminal state is committed. It reads that final
record, can't change it, and is not a step in any task's execution, so it
adds no hand-off to the shared-state model (overview §2.1).

**Manual.** `water procedure demote <id> [--reason]` and `water procedure
enable <id>` are not flag-gated, matching R. `water procedure list` shows
active, disabled (with reason), pending drafts and, with `--all`,
invalidated ones with their supersedes chain. `water procedure candidates
[--since] [--min] [--json]` works with the flag off. `water procedure show
<id>` renders the skeleton, provenance and instance outcomes in code. All
of these go through `newDaemonClient()`, as `water intent` does.

**Config** (all under `memory.procedures.`, following `internal/config`'s
existing `defaults()`/`apply()`/`Flat()` pattern): `promotion.enabled`
(false), `promotion.min_repeats` (5, provisional), `promotion.min_days`
(2, provisional), `promotion.max_active` (20, provisional),
`promotion.demote_bad_rate_pct` (20, provisional) and
`promotion.demote_min_samples` (10, provisional). The provisional values are
R's, copied because Q reuses R's loop. They are not calibrated for tasks.

### 7. What is reused, and how

| R piece | Q's use |
|---|---|
| `candidateID`, `ValidCandidateID`, `stripFencing`, `sanitizeName` | Small and domain-free, but they sit in a package that imports `nervous`. Q extracts them to a tiny shared package both loops import, **or** duplicates them deliberately, as `promote.skipSet`'s own comment already does for a three-line helper. The implementing session picks at Plan. It must not import `internal/nervous/promote` from `internal/procedures`. |
| `confirmYesNo` / `promptYesNo` | Called as-is (both are in `internal/cli`). |
| `LearnedIntentShouldDemote` | Mirrored as `ProcedureShouldDemote`. It is not generalized, because R's takes `[]store.RouteRow`. |
| `intent_state` / `SetIntentState` | Mirrored as `procedure_state` (§6). |
| Pending/learned dir layout, 0o700/0o600 | Mirrored under `twins/<twin>/procedures/`. |
| Endpoint and CLI shape | Mirrored: `/v1/procedures/{candidates,draft,promote,demote,enable}` and `GET /v1/procedures`. |

## Do not build in Q

- **Any change to authorization.** No standing grant, no batched envelope,
  and no procedure field that affects `gate.Invoke`. Promotion changes
  planning only.
- **A real "same shape" matcher.** Q ships `NoMatch` for retrieval and
  `ExactStructural` for grouping. Anything looser waits for the owner.
- **Promotion of anything but procedures.** Preferences, patterns and other
  F types are written through G's paths. H's skills are not promoted (H.md:
  "Q promotes procedures, not skills"). R's intent loop is untouched.
- **Learning from failures.** Abandoned or escalated tasks never become
  candidates. Whether a "what not to do" record is ever wanted is not in
  scope.
- **Embedding or semantic retrieval**, consistent with F.
- **Reply content in any trace, draft or record.** N's extracted fields and
  quotes stay in N's and L's own paths and never enter Q.
- A workflow-engine or any other new dependency.

## Tests

The implementing session runs `go vet ./...`, `go test -count=1 ./...` and
`CGO_ENABLED=0 go build ./cmd/water` before every commit, as always. This
planning doc runs none of them. Every model call in tests uses
`backend.Fake`.

- **Trace.** Fixture tasks for each persisted archetype, with added,
  skipped and retried steps, a delegation, and an unmet criterion, produce
  the expected `Executed` and typed `Deviations`. A trace built from a task
  whose trail cites a reply never contains the reply's text, and every
  trace passes `CheckNeverStore`.
- **Qualification and grouping.** Each disqualifier in §2 excludes a task
  (abandoned, escalated, critical criterion unmet, critic contradicted,
  sensitivity match, built from a disabled procedure). Groups below
  `minRepeats` or spanning fewer than `minDays` produce no candidate.
  Candidate ids are stable across runs and match `ValidCandidateID`.
  `Candidates` works with the flag off.
- **Draft is cold and code owns identity.** Against `backend.Fake`, `Draft`
  makes exactly one `Run` with `Role: "procedures.draft"`, and never touches
  a warm session (a plain `backend.Backend` is the only input). A fake reply
  that claims its own `id`/`provenance` has both overwritten. A fenced reply
  parses. The prompt contains no field from a reply body.
- **Validation, both directions.** Each §4 rejection has a failing fixture:
  O's validator failing, a step no sample ran, a universally-run step
  dropped, an unknown authority-shaped field, a level-B function, a dropped
  fired trigger, a never-store hit, and the cap. A skeleton that matches the
  samples' trace passes. With `NoMatch`, the ambiguity and false-accept
  checks report `skipped`, not `passed`.
- **Approval.** Promote refuses with the flag off, rejects a path-traversal
  candidate id before any I/O, promotes the on-disk pending file and not a
  re-draft, and refuses on anything but `y`/`yes`. On success exactly one
  audit entry, one learned file and one F record exist, and the record's
  `SourceRef` hash matches the file. An injected audit failure leaves no
  file and no record.
- **Only this loop writes procedures.** An F write of `Type: procedure` with
  any other trigger or `SourceRef` scheme is rejected. A learned file with
  no current record, or a hash mismatch, is skipped and reported and does
  not stop the daemon.
- **Re-promotion supersedes.** The old record is invalidated with
  `SupersededBy` set and still readable through `History`.
- **Retrieval.** `NoMatch` never matches. With a test `Retriever`, a
  disabled or invalidated procedure is never returned, and a matched plan
  still goes through O's validator and still stages every outward step as
  its own envelope (asserted through the approvals queue in the style of
  `scenario_s13_test.go`). The seeded task's `created` event carries
  `from_procedure`.
- **Demotion.** `ProcedureShouldDemote` is table-tested at and around
  `minSamples` and the rate threshold. A terminal instance crossing the
  threshold writes `procedure_state` and the next `Match` excludes the
  procedure. Manual demote/enable round-trip without the flag. Demotion
  never invalidates the F record, and invalidation is never undone by
  `enable`.
- **Import direction.** Guard tests: `internal/procedures` does not import
  `internal/nervous/promote`, and `internal/tasks` does not import
  `internal/procedures`.

## Dependencies

- **Depends on:** **F** (the `procedure` type, `Supersede`/`Invalidate`,
  `CheckNeverStore`), **G** (the audited write path, `water memory`, the
  re-admitted `memory.*` config prefix) and **O** (the planner hook, the
  plan validator, the plan skeleton shape, and persisting the authored
  skeleton at creation per §1). Through O it transitively needs J (record
  and trail), K, L, N and H.
- **Uses P when present, does not require it.** Q handles whatever
  archetypes exist. Before P lands, only `fan_out` tasks can qualify.
- **Blocks:** nothing in this redesign.
- **Cross-slice requirements Q raises:** O persists the authored skeleton
  (§1) and records `from_procedure` on seeded tasks (§5). J's `Service`
  gains an `OnTerminal` observer (§6). G's `memory.propose` cannot create
  `procedure` records (§6). Each is small, and each is confirmed against the
  sibling spec at Approve rather than assumed.
- **No new dependency.** Standard library, the existing `gopkg.in/yaml.v3`
  already used by `promote`, and the already-approved `modernc.org/sqlite`.

## Open for the owner (Q's slice only; the full list is in the overview doc)

These are left undecided on purpose. The implementation must not pick
defaults for them silently.

1. **What "same shape" means**, for both grouping (§2) and retrieval (§5).
   The brief names goal type, participants, tools required and outcome type,
   and says it must avoid both over-matching and under-matching. Q ships the
   strictest code-only grouping key and a never-matching retriever. The
   definition must also say which **plan-time** facts count, because
   execution facts do not exist yet when the planner looks up a procedure.
2. **Promotion thresholds**: repeats, distinct days and the lookback window.
   R's 5-on-2-days is carried over as provisional and may be too high for
   tasks that recur monthly.
3. **What counts as a bad outcome** for auto-demotion (§6's list), and the
   rate and minimum sample. With rare tasks, `min_samples` 10 may mean a
   procedure is never auto-demoted in practice. This is part of the global
   confidence-thresholds question, and needs real CEO accept/edit/reject
   outcomes.
4. **Where the procedure body lives**: a learned file plus the F record as
   authority (proposed), or inside F's store if F goes to SQLite.
5. **The authored-skeleton baseline** (§1). Is O's persisted plan the
   baseline for deviations, and if O does not persist one, is promoting the
   executed skeleton alone acceptable?
6. **Parameterization of participants.** Can a procedure name specific
   roster people (`person:<id>`), or only roles ("project leads")? Specific
   people reuse more exactly and go stale when the roster changes.
7. **`procedure_state` as its own table** (proposed) vs generalizing
   `intent_state` to cover both loops.
8. **Explicit single-task promotion.** Should "remember how we did this"
   from the CEO promote one task without meeting the repeat threshold?
   CONTEXT.md principle 6 allows memory from explicit CEO statements, but
   the brief's §8 decides on reusing R's candidate-detection shape, which is
   repeat-based. This is a real tension, not something Q resolves.

Source-brief notes. Two references in the redesign brief don't line up with
its own numbering. They may have been corrupted in the source paste, or may
just be mis-numbered. I have not guessed which:

- §8 says a workflow plan fits "§4's memory taxonomy", but the memory
  taxonomy (with its `procedure` type) is in **§1**. I read it as §1.
- §4's Planner bullet says the matching criterion "is an open question, see
  §6", but §6 is completion detection and escalation. The matching question
  is in the unnumbered open-questions list at the end.

Otherwise §8 reads cleanly, and I found no garbled phrase in the parts Q
covers.
