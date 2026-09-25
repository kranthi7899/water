# Known gaps

Deliberate deferrals for the single-twin CEO daemon, recorded so they aren't
re-litigated or mistaken for oversights. Context: `docs/CONTEXT.md`,
`docs/EVOLUTION_PLAN.md`. The persona/council-era gaps this file used to
track (Design accessibility, COO sequencing lessons) applied to the deleted
four-role council and are archived at
`docs/archive/known-gaps-persona-council-era.md`.

## Slice C (decisions): deferred to a later pass

From the 2026-09-24 amendment (`docs/slices/C.md`), deliberately not built
in C-base:

- **Proposing new playbooks from repeated cases.** No mechanism watches for
  a decision type recurring often enough to be worth a dedicated registry
  entry; every unmatched case runs through `generic` every time, however
  often it repeats.
- **Standing grants.** Every staged action still needs its own approval; C
  does not add a way to pre-approve a class of future actions (e.g. "always
  approve budget requests under $2,000"). The P2 gate rule stays strict (see
  `docs/slice-c-planning.md`) until a scenario proves the need.
- **Probabilities on options.** Decision cards state options, consequences
  and gaps in prose; they do not assign or display numeric likelihoods.
- **A dedicated classifier model.** `Classify(item)` (the triage interface
  in `docs/slices/C.md`) is model-based for now. A faster/cheaper classifier
  can replace it later behind the same interface without touching the
  registry, cards, or gate wiring.

## Slice M (live-meeting assistance): consent handling deferred

`docs/slices/M.md` builds a prototype with no real third parties in the
meetings it will be tested against, so it deliberately does not build any
consent flow: no participant notification, no recording-consent capture, no
region-specific two-party-consent logic, no way for a non-CEO attendee to
object or opt out. It keeps only the cheap, structural safeguards: audio
never touches disk or the daemon, only timestamped transcript text crosses
the local API; the client shows a visible "listening" indicator; sessions
start and stop by explicit hotkey, never automatically from calendar
presence alone.

**This must be revisited before Slice M is ever used with a real external
participant.** Recording a meeting that includes people outside the CEO's
own company raises real consent obligations (varying by jurisdiction) that
a prototype note cannot discharge. Do not point Slice M at a live call with
outside participants until this gap is closed.

## Slice R: sous-chef import-denylist test only scans `internal/nervous/reflex`

Design §1(a) (`docs/slices/R.md`) describes the import/mutating-selector
denylist test as covering every sous package (`reflex`, `propose`, `tmpl`,
`slots`, `intents`, `render`, `speak`, `turn`). The actual test built
(`internal/nervous/reflex/imports_test.go`, an earlier task) only globs
`*.go` in its own directory (`reflex`), so it enforces the denylist for
`reflex` alone, not the other seven packages named in the design — found
while building R-20's `internal/nervous/propose` package, which was written
by hand to respect the same rule (no `os/exec`/`net`/`gate`/`connectors`/
`vault` import, no `Propose`/`Upsert`/etc. call) but is not mechanically
checked for it. Not fixed here: broadening the test's file glob to cover
every sous package's directory is a small, independent change that belongs
to whichever task next touches this test, not silently folded into R-20.

## Slice R: `approvals.respond`'s single-pending render has no real read-back text

`bindPendingHandler` (`internal/nervous/reflex/handlers.go`, R-8) returns
`Result{Kind: "decision", ApprovalID: e.ID}` for exactly one pending
envelope, with no `Text` and no `Interpretation`. `style.yaml`'s
`approvals.respond` entry has a `header` ("Approval:") and only an `empty`
template ("Nothing is waiting for approval."), no `item`. Since the Result
has no `Text` and no `Items`, `render.Style.Render` always falls through to
the `empty` template — so on any channel, with the flag off, a bare "yes"
against one pending envelope currently renders as "Approval:\nNothing is
waiting for approval.", which reads as if nothing were pending even though
something is. Found while building R-21 (voice approval binding), which
requires this pre-R-21 behavior to stay byte-identical when
`router.voice_approve.enabled` is off, so it was not fixed here. R-21's own
voice-approval-enabled path does not have this problem (it renders
`approvals.ReadBack(envelope)` directly instead of going through this
Result/template path at all). Fixing the general case means either giving
`bindPendingHandler` a real summary `Text` or adding an `item`/better
`empty` template that reflects a bound envelope — a small, independent
change for whichever task next touches this handler's rendering.

## Slice R: an inactive/disabled intent never actually escalates with `intent_inactive:<id>`

Design §5.4 (and `internal/nervous/report.go`'s own
`InactiveIntentNearMiss` field, which aggregates
`escalation_reason == "intent_inactive:<id>"` rows) documents that an
utterance matching only a `Shadow()` intent (inactive, or — since R-23 —
disabled) should escalate with that specific reason, so `water route
report`/`GET /v1/router` can show "the CEO tried this N times." Found
while building R-23: `matchOnly` (`internal/nervous/tier0.go`) only ever
iterates `reg.Candidates()`, never `reg.Shadow()`, so a disabled or
inactive intent's own templates are never tried at all — the utterance
just falls through to `"no_match"` (or, for R-23's own auto-demoted
learned intents, to the main path) like any other unrecognized turn, and
`report.go`'s `intent_inactive:` aggregation has had nothing to ever
find. R-23's own `TestLearnedAutoDemoted` only asserts that a demoted
learned intent's next matching turn escalates to the main path (`Owner !=
"quick"`), which holds regardless of this gap, so it does not block that
task. Pre-existing since at least R-6/R-20 (inactive write intents), not
introduced by R-23. Fixing it means giving `matchOnly` (or a caller of it)
a second, Shadow()-only pass that runs only when the Candidates() pass
found nothing, producing `"intent_inactive:<id>"` instead of `"no_match"`
when exactly one Shadow() intent's templates match.

## Slice R: `runTier0Match`/`TryTier1`'s handler-run previously mislabeled the answered intent

Found and fixed while building R-23 (not left as a gap): every reflex
handler in `internal/nervous/reflex/handlers.go` hardcodes its own
`Result.Intent` (e.g. `store.calendar_events`'s handler always reports
`"schedule.on_date"`), which was harmless before learned intents existed
because exactly one embedded intent ever targeted a given function. Once a
learned intent can wrap the very same handler, this hardcoded value
silently misattributed the answer to the embedded intent instead — wrong
`route_log.intent`/`intent_origin` (breaking the auto-demotion hook, which
keys off intent id and origin) and wrong style-rendered response text (the
embedded intent's phrasing instead of the learned one's). Fixed by having
`runTier0Match` (`tier0.go`) and `TryTier1` (`tier1.go`) overwrite
`result.Intent` with the registry's own matched intent id right after the
handler returns, restoring the invariant `internal/nervous/actions.go`'s
write-intent path already upheld correctly. Regression test:
`TestAnswerQuickRecordsIntentOrigin` (`internal/nervous/promotion_test.go`).

## Slice R: `TestQuickInvokeTaintedResultEscalatesSession` is time-of-day flaky

Found while building R-22 (pre-existing: reproduces identically on
R-21's tick commit, `29dc2b9`, before this task's changes). The test
(`internal/gateway/quick_test.go`) upserts an event at `time.Now().Add(time.Hour)`
and expects `quick.calendar`'s default "when" range to include it and mark
the session tainted. Whenever the test runs late enough in the local day
that `+1h` crosses local midnight, the event falls outside `quick.calendar`'s
default range and the assertion fails with "session taint after = clean,
want tainted". Not fixed here: root cause is in the fixture's fixed
`+1h` offset versus `store.calendar_events`'s actual default window, not
anything R-22 touches (`internal/nervous/promote`, `mainpath.go`'s
BeginMain/EndMain wiring, or the new `/v1/route/candidates` endpoint).
Whoever next touches `quick_test.go` or the `store.calendar_events`
handler should pin the fixture's event time to a fixed, mid-day instant
(the way `internal/nervous/promote/candidates_test.go`'s `day1`/`day2`
fixtures do) rather than an offset from `time.Now()`.

## Slice R: deliberate design choices, recorded so they aren't re-litigated

- **Store reads bypass the gate, by decision, not oversight.** Design §1 of
  `docs/slices/R.md` (owner-confirmed) has the sous chef (Tier 0/Tier 1) and
  quick tools read the twin's local store through a direct, read-only view
  (`store.OpenReadOnly`, `mode=ro` + `query_only(1)`) — never through
  `internal/gate`, and the gate gains no `store` pseudo-connector. The
  reasoning: every record in the store already passed `Gate.Invoke` (permit,
  rate cap, hash-chained audit) when it was synced in, so a store *read* is
  not an outward action under CONTEXT.md's principle 2, and routing it
  through the gate's per-call machinery (canonical hashing, fsynced audit
  appends, permit minting, the post-execute `Normalize`/`Upsert` step) would
  add latency and fill the audit log with no upstream quota to protect. The
  "one action path" invariant is instead enforced by construction: an
  import-restriction test bars the sous packages from importing
  `os/exec`/`net`/`net/http`/`internal/backend`/`internal/gate`/
  `internal/connectors`/`internal/vault`, a selector test bars mutating
  calls (`Decide`, `Propose`, `Upsert`, `InsertRoute`, etc.), and reserved
  connector names make a `quick__*` tool name collision impossible. See
  `docs/architecture.md`'s router section for the enforcement mechanism.
  Note: the import-denylist test (`internal/nervous/reflex/imports_test.go`)
  currently only globs its own directory, not the other seven sous packages
  the design names — see the existing "sous-chef import-denylist test only
  scans `internal/nervous/reflex`" entry below.

- **Unread-mail sync doesn't exist yet.** `mail.unread_count` (the
  `mail.unread_count` read intent) does not report a real unread count —
  Gmail's unread flag is never synced into the local store. It instead
  reports today's received-message count as a labeled substitute, stated
  honestly in the response text rather than presented as if it were an
  unread count. Building the real thing needs an edit to `gmail.Normalize`,
  which Slice R does not own (see the write-function session's territory).

- **`schedule.free_time`'s workday window (09:00–18:00) is a hardcoded
  handler constant**, not a per-CEO-configurable setting. `free_slots` is
  also `ClassCompute`, not `ClassLookup`, so it is deliberately not exposed
  as a `quick.*` tool to the main agent either (R-8) — exposing a derived
  judgment call, not a raw fetch, would blur "the model gets lookups, not
  computed judgments."

- **Tier 3 / background strong-model routing is deferred.** `TierStrong` is
  defined in `twin.yaml`'s schema and `Manifest.ModelFor` but stays fully
  unwired — the main path always runs the manifest's `models.fast` model
  over the warm session. Design §15 folds "Tier 3" conceptually into the
  main path rather than building a separate tier: no per-turn model switch
  exists, because switching would restart the warm session. The owner can
  set `models.fast` to a stronger model in `twin.yaml` directly; that's a
  manifest change outside this slice, not a code change.

- **Project-entity resolution is unbuilt.** `slots.Type` reserves a
  `project` slot type (see the type list in `internal/nervous/slots`), but
  no resolver exists for it and nothing in the CEO's intent files uses it.

- **The `Decider` interface has only a null implementation.** No real
  adapter (Jev or otherwise) exists behind `internal/decider`; `New("none")`
  is the only accepted provider, and `decider.provider` rejects any other
  value. Per the plan, the conditions for building a real adapter are:
  triage p95 latency over 3s, triage precision below 80%, or classification
  eating over 25% of the model-call window. None of these are instrumented
  as an alert yet — they'd need to be read off `route_log`/eval data by
  hand until a later slice wires a check.

- **FunctionGemma / Tier 1 ships disabled by default and stays that way**
  until a live eval passes its thresholds (FA ≤ 1%, Wilson 95% upper bound
  ≤ 2%, n ≥ 200, warm p95 ≤ 400ms, matching model+registry hash — Design
  §10). The eval mechanism itself (`water route eval --tier1`,
  `internal/nervous/eval/live.go`) was built and unit-tested in R-18, but
  was never run against the real `functiongemma-270m-it` model and
  `llama-server` sidecar during this build — the implementation sandbox had
  no network access to pull the ~280MB GGUF and no `llama-server` installed.
  `TestTier1Live` verifiably skips cleanly without the env var or the model
  file; it has not been exercised end to end. The GGUF pin (repo, revision,
  sha256) is recorded in `docs/functiongemma.md` and `internal/nervous/
  sidecar/model.go`, verified live against the Hugging Face API in R-17.

- **`router.tier0.timeout_ms`, `router.tier1.timeout_ms` and
  `router.quick_tools.enabled` are inert config.** All three round-trip
  correctly through `water config set/get` and default correctly (R-25),
  but have no live consumer: `nervous.Config` has no per-tier context-
  deadline field (Tier 0 does no I/O to bound; Tier 1's request timeout is
  the package constant `t1.requestTimeout`, not derived from any config
  value), and `router.quick_tools.enabled`'s only real gate point,
  `gateway.Daemon.TwinToolPolicy`'s `Quick` field, doesn't read it either —
  quick tools are always exposed unconditionally once
  `Nervous.QuickFunctions()` is non-empty. See the comment above
  `buildNervousConfig` in `internal/cli/cmd_daemon.go`.

- **`intent_inactive:<id>` escalation reasons are designed but never
  produced.** Design §5.4 and `report.go`'s `InactiveIntentNearMiss`
  field both assume an utterance matching only an inactive or disabled
  (`Shadow()`) intent escalates with `escalation_reason =
  "intent_inactive:<id>"`, so `water route report`/`GET /v1/router` can
  surface "the CEO tried this N times." In the shipped code,
  `matchOnly` (`internal/nervous/tier0.go`) only ever iterates
  `Registry.Candidates()`, never `Registry.Shadow()`, so a Shadow-only match
  never happens — the turn just falls through to `"no_match"` (or, for a
  demoted learned intent, straight to the main path) like any other
  unrecognized utterance. `report.go`'s aggregation has had nothing to ever
  find. Pre-existing since R-6/R-20 (inactive write intents); found and
  left open during R-23. Fixing it needs a second, `Shadow()`-only match
  pass in `matchOnly` (or a caller of it), run only when the `Candidates()`
  pass finds nothing.

- **The `handoff` event kind is decoded by clients but never emitted
  server-side.** Both the CLI (`water ask`) and the Swift client (R-27,
  `TurnEvent.Kind.handoff`) know how to decode a `{"kind":"handoff",
  "text":"..."}` NDJSON line, forward-compatibly. But
  `internal/nervous/nervous.go`'s `emitHandoff` — confirmed still true as of
  this task — says outright in its own doc comment that `runtime.EventKind`
  has no dedicated `"handoff"` kind yet: on any non-voice channel, the
  handoff acknowledgement still sends a **zero-text `ack`** event, not a
  `handoff` event. This is worth flagging prominently: the underlying
  timing/escalation logic (ack within `router.ack_ms`, 250ms default) is
  fully built and tested, but on text/CLI channels it produces no visible
  acknowledgement text at all today — only the voice channel gets a spoken
  handoff phrase (as a `sentence` event using a `style.voice.handoff`
  phrase). Making the CLI/text-bar channels show something equivalent needs
  a real `EventHandoff` kind added to `internal/runtime/runtime.go` and a
  send site in `emitHandoff` to use it instead of the zero-text `ack`.

- **`gmail.draft_message`'s level-D (not A) write-intent path** — this was
  a real ambiguity the original Slice R plan didn't anticipate (Risk item
  24): the concurrent write-function session granted `gmail.draft_message`
  at level D, not A like the other three write functions, and `checkAction`
  as originally speced treated any level other than A as a hard load error.
  Resolved in R-20 by extending `checkAction`/`Intent.RequiresApproval`:
  level D loads active with `RequiresApproval: false` and is delivered
  directly as the answer (no envelope, no gate call, no `approval_required`
  event), since D already means "nothing leaves" and the proposal itself is
  the drafted artifact. No residual gap here, but worth keeping in mind: a
  future write-function addition that lands at a level other than A or D
  will hit the same "not A, not D" question and needs the same kind of
  explicit resolution, not a silent assumption.

- **Barge-in (interrupting an in-progress reply) is deferred**, and the
  Swift client still cancels the entire in-flight turn when the user starts
  a new one, rather than merging into a barge-in interaction. The daemon
  itself supports concurrent turns (proven by tests, and usable from the
  CLI or a second client today) — changing the Swift client's behavior to
  make use of that is a follow-up owner decision (Risk item 16), not
  something this slice needed to resolve.

- **`route_log.confirmed` is reserved, unused.** No `water route export`
  command and no fine-tuning recipe exist yet; the column is reserved for
  a later labeling/export slice that would need it (Risk item 23's "learned
  intent" loop stops at promotion/demotion, not label export).

## Slice R: R-12's checklist box was never ticked — flagged, not silently fixed

`docs/slices/R.md`'s task list has every task from R-1 through R-27 marked
`[x]` **except R-12** ("Runtime split, main path, front door"), which was
still `[ ]` at the time this task (R-28) began. This is not a missing-work
gap: R-12's work commit (`cd2e5c5`, "R-12: Runtime split, main path, front
door") and a same-day follow-up fix (`24cfe33`, "R-12 fix: ModelTurn returns
its beginModelNoting error instead of emitting and bare-returning") both
exist on this branch, the files they produce
(`internal/nervous/{nervous.go,mainpath.go,ack.go}`) are present, and every
later task (R-13 through R-27) builds on and exercises them successfully —
the full test suite passes with R-12's code in place. What's missing is
specifically the "R-12: Tick R-12..." commit that every other task in this
build has as its second commit; it was never made. Per this task's own
instructions this is recorded here rather than silently checked off — see
`docs/EVOLUTION_PLAN.md`'s Slice R log entry for the same note, and the
owner should confirm R-12's work independently before ticking its box.

## Carried over from A-series slices (still true)

- Long-term memory (`internal/memory`) is not wired into the runtime, the
  morning brief's signals, or (once built) the decision registry — all read
  the store directly.
- Hosting the daemon off the laptop (tracked in `docs/EVOLUTION_PLAN.md`'s
  log) is unscheduled; `water daemon` currently only runs while the Mac is
  awake.
- Gmail push (Pub/Sub) was explicitly declined in favor of fast polling
  (A4, 2026-09-24); revisit only if polling latency becomes a real problem.
