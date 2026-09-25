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

## Carried over from A-series slices (still true)

- Long-term memory (`internal/memory`) is not wired into the runtime, the
  morning brief's signals, or (once built) the decision registry — all read
  the store directly.
- Hosting the daemon off the laptop (tracked in `docs/EVOLUTION_PLAN.md`'s
  log) is unscheduled; `water daemon` currently only runs while the Mac is
  awake.
- Gmail push (Pub/Sub) was explicitly declined in favor of fast polling
  (A4, 2026-09-24); revisit only if polling latency becomes a real problem.
