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

## Carried over from A-series slices (still true)

- Long-term memory (`internal/memory`) is not wired into the runtime, the
  morning brief's signals, or (once built) the decision registry — all read
  the store directly.
- Hosting the daemon off the laptop (tracked in `docs/EVOLUTION_PLAN.md`'s
  log) is unscheduled; `water daemon` currently only runs while the Mac is
  awake.
- Gmail push (Pub/Sub) was explicitly declined in favor of fast polling
  (A4, 2026-09-24); revisit only if polling latency becomes a real problem.
