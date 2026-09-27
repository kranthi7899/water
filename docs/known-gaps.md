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

## Slice R: `runTier0Match`/`TryTier1` (tier1.go, since removed)'s handler-run previously mislabeled the answered intent

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
`runTier0Match` (`tier0.go`) and `TryTier1` (`tier1.go`, since removed) overwrite
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

## Slice R: FunctionGemma/Tier 1 fails its own eval gate — for real reasons now

**Resolved by retirement — Tier 1 was removed, see docs/EVOLUTION_PLAN.md.**

Measured live during Slice R's Phase 4 verification
(`docs/slices/R-verification.md`), across two live-eval runs. The first run
(before `internal/nervous/t1`'s parsing bug below was found and fixed)
showed 0% false accepts but 1313ms warm p95 — a pure-looking latency
failure. Investigating that latency surfaced a much more serious problem:
`parseResponse` only ever read `llama-server`'s structured `tool_calls`
field, which `llama-server` never populates for this model (it returns the
model's own raw `<start_function_call>call:NAME{args}<end_function_call>`
text in `message.content` instead), so every real answer was silently
discarded as `ErrTextReply` — **Tier 1 had never answered a single
utterance in its entire existence**, and the first run's "0% false
accepts" was vacuously true (0 accepts out of 0 real answers), not a
safety measurement. Fixed (commit `b246bd9`): a raw-format fallback
parser, plus a request-level `stop` sequence that also cut latency 7-10x
(the model was hallucinating fabricated extra calls out to `max_tokens`
without one). Re-run after the fix, against the same 410 cases:

```
9 false accepts (fa_rate 2.20%, wilson95 upper 4.12%), warm p95 558ms
hit_rate=0.464 intent_acc=0.940 escalation_rate=0.634 reasoning_answered=0
```

This is the trustworthy number, and it still correctly fails the gate
(false-accept rate ≤1%/Wilson≤2% and warm p95≤400ms both miss, though
latency is now much closer). `water daemon: tier1 not started: eval_failed`
confirmed live again post-fix, even with `router.tier1.enabled=true`. Not
investigated in this pass, worth a follow-up before ever setting
`router.tier1.enabled=true` in real use:
- **Hardware:** the Homebrew `llama.cpp` bottle used here is confirmed
  CPU-only (`otool -L` on `libggml-base.dylib` shows no `Metal.framework`
  linkage). A from-source build with Metal enabled would very likely close
  most or all of the remaining 558ms→400ms latency gap, but building from
  source is a different decision than "the Homebrew bottle" the owner
  approved.
- **Accuracy:** whether the 2.2% false-accept rate is close to this
  270M-parameter model's practical ceiling without fine-tuning (explicitly
  deferred by the plan), or whether prompt/grounding-check tuning in
  `internal/nervous/tier1.go` could bring it under 1% without a bigger
  model.

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

- **FunctionGemma / Tier 1 — retired.** See `docs/functiongemma.md` and
  `docs/EVOLUTION_PLAN.md`'s dated entry for this slice for the decision and
  full rationale.

- **`router.tier0.timeout_ms` and `router.quick_tools.enabled` are inert
  config.** Both round-trip correctly through `water config set/get` and
  default correctly (R-25), but have no live consumer: `nervous.Config` has
  no per-tier context-deadline field (Tier 0 does no I/O to bound), and
  `router.quick_tools.enabled`'s only real gate point,
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

## Slice V (V-1): FluidAudio's LuxTts resource bundle can't reach a signed Water.app

Added while adding the FluidAudio dependency (`docs/slices/V.md`, §1).
`clients/macos/Package.swift` now depends on FluidAudio 0.17.4, whose
`FluidAudio` target ships a resource bundle (`FluidAudio_FluidAudio.bundle`,
~1MB — a LuxTts G2P lexicon) built via SwiftPM's `.process(...)` resources.

The resource-bundle accessor SwiftPM generated for this package
(`resource_bundle_accessor.swift`, regenerated on every build under
`.build/.../FluidAudio.build/`) looks the bundle up only via
`Bundle.main.bundleURL.appendingPathComponent(...)`, then falls back to a
hardcoded absolute `.build` path from whichever machine built it. For an
app bundle, `Bundle.main.bundleURL` is the *top level* of `Water.app`
(confirmed empirically), not `Contents/` or `Contents/Resources/` — but
`codesign --verify --strict` (which `build.sh` requires to pass, and
already did before this change) rejects any content placed at that top
level, real file or symlink alike ("unsealed contents present in the
bundle root" — tried both, both failed identically). There is no placement
that satisfies both constraints at once.

`build.sh` copies the bundle into `Contents/Resources` (the standard,
codesign-safe location) rather than leaving it out or faking a fix, but
this does not make FluidAudio's own lookup find it in a real, distributed
`Water.app` — only on the machine that built it, via the fallback path.

**Not a blocker for the planned voice work**: grep confirms only
`Sources/FluidAudio/TTS/LuxTts/G2p/LuxTtsG2p.swift` touches `Bundle.module`;
`KokoroAneManager` (Kokoro TTS) and the Parakeet ASR path this project
actually plans to use do not reference it. Must be revisited (a custom
resource accessor, or a confirmed guarantee that LuxTts is never reachable
from Water's code) before any code path invokes LuxTts.

## Slice V (V-4): Kokoro model-download progress is phase-only, not byte-level

`KokoroSpeaker` (`clients/macos/Sources/Water/KokoroSpeaker.swift`) calls
`KokoroAneManager.initialize(preloadVoices:)` to download (first use only)
and load the 7 mlmodelcs + vocab + default voice pack. That method takes no
progress-handler parameter — it calls `store.loadIfNeeded()` and
`KokoroAneResourceDownloader` internally with no callback surfaced. Only
the lower-level `ModelHub.loadModels`/`.download` and
`KokoroAneResourceDownloader` functions take a `progressHandler`
(`Shared/Download/DownloadTypes.swift`'s `ProgressHandler` typealias).

Deliberately not reached around: hanging V-6's future download-progress UI
off those lower-level calls instead of `initialize()` would tie this
project to FluidAudio's internal APIs, which change often (5 releases in 3
days during this slice's planning pass, one a public-API rename) — a
maintenance liability for a UI nicety. V-6 (engine selection + the
one-time download-consent dialog, not yet built) is expected to show only
three discrete phase labels (downloading / compiling / ready) instead of a
byte-level progress bar, for this same reason.

**Revisit if FluidAudio ever adds a public progress-handler parameter to
`KokoroAneManager.initialize`** — at that point the phase-only UI can
become a real byte-level one with no internals dependency.

## Slice V (V-6): No settings UI to opt back into FluidAudio after declining

`EngineSelector` (`clients/macos/Sources/Water/EngineSelector.swift`) asks
once, ever, per install: declining the one-time consent dialog sets a
permanent `UserDefaults` flag (`fluidAudioDeclined`) and Water falls back
to Apple Speech for good — no re-prompt, matching the project's existing
"explain once" ethos (`AppDelegate.explainAccessibilityOnce`, which has the
same one-way limitation for the Accessibility permission notice). There is
no settings UI in this slice that would let someone who declined turn
FluidAudio back on later, short of clearing the flag by hand (`defaults
delete <bundle id> fluidAudioDeclined`) or reinstalling.

**Revisit when Water gets a settings window** — expose a toggle there that
clears `fluidAudioDeclined`, and, for symmetry, a "forget downloaded
models" action clearing `fluidAudioModelsReady` alongside whatever FluidAudio
itself offers for removing cached model files.

## Slice V (V-6): `isBnnsCrashProneOS` cannot be consulted from engine selection

`KokoroAneManager.isBnnsCrashProneOS`/`osAdvisory` (FluidAudio 0.17.4) are
plain `static func`s with no `public` modifier, so they are inaccessible
from `Water`/`WaterClientCore` — Swift's `internal` access level is
module-scoped, and the compiler rejects a call from either module.
`EngineSelector.choose`/`resolve` therefore has no way to defensively avoid
FluidAudio on an OS build known to be BNNS-crash-prone (macOS 26.4–26.5,
iOS 26.4+); `KokoroAneManager.initialize()` already detects this internally
and logs its own warning on an affected build, so nothing is silently
missed, but engine selection cannot act on it (e.g. falling back to Apple
Speech automatically on a flagged OS version). This dev machine (15.6.1) is
unaffected, so the gap has not been exercised. A one-line note recording
this is left on `EngineSelector.isAppleSilicon`'s doc comment.

**Revisit if a future FluidAudio release makes either function `public`** —
at that point `EngineSelector` can add a real OS-version check to its
selection logic instead of only leaving a comment.

## Slice V (V-voice): dependency-spike gap (V-1) still applies unchanged

The V-1 resource-bundle gap recorded above (FluidAudio's `LuxTts` G2P bundle
can't reach a signed `Water.app`) was re-checked after V-3 through V-8: the
voice pipeline built in this sub-slice (`AppleSpeechOutput`, `KokoroSpeaker`,
`ParakeetCapture`) only ever touches `KokoroAneManager` (Kokoro TTS) and
`SlidingWindowAsrManager` (Parakeet ASR) — grep still confirms only
`LuxTtsG2p.swift` references `Bundle.module`, and nothing built in V-voice
calls into `LuxTts`. The entry above is unchanged and still accurate; not
duplicated here.

## Slice F: never-store residual gaps (provisional detector)

The validator checks each field on its own, so content split across two
fields (half a card in the statement, half in a reason) passes. Encoded
content (base64/hex of a card or email body) is caught only if it trips the
entropy rule. Lower-case named transcripts (`dana: … / sam: …`) pass. A
number glued to an a–f-only prefix (`cc4111…`) passes. Refs can still carry
up to 256 bytes of space-free text. Ungluing letters from digits raised false
positives: a short letter-prefixed id with a 9-digit run (`PO123456789`)
flags as an SSN. All thresholds are provisional until checked against real
records.

## Slice F: the real `~/.water/water.db` is missing one trigger

The live database already had migration 14 applied before the review added
`memory_records_no_replace` to `0014_memory_records.sql`. The runner tracks
version numbers only, so the edit never reaches that database. Its
`memory_records` table has 0 rows. **Resolved 2026-09-25:** with the owner's
approval, the trigger's `CREATE TRIGGER` statement was run by hand against
the live database. All three triggers are now present. Still unexplained: which
newer binary applied migrations 13/14 to the real database. The running
daemon predates both, and no agent reported touching `~/.water`.

## Slice V-ui: open gaps

- Any thread turn that replays a twin reply taints the session (review fix).
  After that, S-level writes need an envelope for the rest of the session.
  A per-message taint column would be precise, but it needs a migration.
- Approval-anchored threads are always untrusted, because nothing on an
  envelope records whether its payload came from external content.
- A card can be staged again after its envelope executed, which queues a
  second envelope. It still needs its own yes, but it could mean a duplicate
  send. `already_staged` returns the existing envelope even when a different
  function was requested. After an approval edit, the card's `CardState`
  still points at the voided envelope.
- There is no way to un-dismiss a card.
- The UI can't tell a card is already staged until it stages it again, and it
  has no approval-edit UI (the endpoint exists but isn't allowlisted).
  `GET /v1/voice/profile` isn't allowlisted either.
- The page's `{type:"mic"}` message isn't tied to a user gesture or window
  focus, and approving from the page has no native confirmation. Both rely
  entirely on XSS-free rendering.
- The page can cancel any task through `/v1/tasks/{id}/cancel`, including
  other clients' turns.
- Thread posts accept any channel, including `voice`. The UI only sends
  `text-bar`.
- Nothing checks the Swift allowlist against `api.js` automatically. A new
  UI route means updating `api.js`, the Go test and
  `WorkspaceAllowlist.routes` by hand.
- The mic toggles rather than holds, and its reply goes to the text bar, not
  the open thread.
- Recap has no project guess, because no classifier is wired into the
  daemon. The running/failed recap state is in memory only.
- A message-anchored thread has no entry point in the UI yet.
- No 401 fallback to the CLI token in the scheme handler (only matters with
  an old daemon).
- Not verified live: needs a daemon rebuild and restart, plus
  `build.sh --install`.

## Slice W: open gaps (2026-09-26)

- **Parakeet vocabulary boosting is deferred.** It needs an extra CTC model
  download. "at the rate" can still be misheard, and the normalizer hint plus
  the spoken read-back cover it for now.
- **Roster emails are empty** (`people.yaml`), and role.md still has five
  `TODO(owner)` lines: what Renaissance sells and to whom, stage and funding,
  co-founders and investors, strategy and priorities, and the company email
  domain. The domain is also needed for `router.voice_approve.internal_domains`.
- **Research latency.** `research.web` adds 13–17.5 s, so a live answer takes
  about 20–25 s. It hasn't been measured on the live daemon. Research calls
  aren't counted in the usage limits.
- **Search-provider leak, narrowed but not closed.** The query guard refuses
  `@`, URLs, schemeless host paths and long tokens. A plain-words query can
  still carry data, for example "kranthi at gmail dot com".
- **The spoken confirm spells only the first recipient** ("and N others").
  A misheard cc on a valid domain could still go out by voice. Consider
  making multi-recipient sends tap-only.
- **Recipient checks cover gmail only**, not calendar attendees. Drafts check
  public providers only; company-domain near-misses are caught at envelope
  proposal once `internal_domains` is set.
- **`policy-*.events.jsonl` keeps full tool arguments**, including research
  queries and email bodies. General turns aren't redacted there.
- **Company answers from the state summary are classed "general"** (e.g.
  "calendar today" answered with no tool call). That's the safe direction,
  but G's memory writer will skip those turns.
- **The opt-in filler** (`router.voice_filler_ms` > 0) moves the globe out of
  "thinking" while it plays. The demo twin's default style would still say
  "One moment." if it's turned on.
- **Warnings aren't shown** on the globe itself; they appear only on the
  glass tab.
- **Eval weak spots (haiku):** it names research sources about half the time;
  1 of 3 runs answered "who's on Halcyon" from role.md's Environment list
  instead of Linear; 2 of 3 showed a spelled address on screen instead of
  saying it aloud.

## The needs-you ticker spends the CEO's gate rate caps (found live, 2026-09-25; fixed)

`needsyou.Service.Tick` runs `decisions.Trigger.Run` every tick. That
rebuilds every candidate card and fetches its evidence through the gate at
origin P1: a `gmail.list_messages` and a `gdrive.search_files` per card. At
the original 120 s interval, with 3 candidate cards, that was about 90 calls
an hour per function. It exhausted `gdrive.search_files` (60/h) and, together
with background mail polling, `gmail.list_messages` (120/h), so the CEO's own
questions were denied "rate cap reached". **Mitigated** by raising the
default `notify.interval_seconds` to 900 (about 12 calls/h per function),
pinned by `TestNotifyIntervalDefaultDoesNotStarveRateCaps`. **The proper fix
is still owed:** the ticker should reuse recently built cards (a short TTL
cache) instead of re-fetching every tick, and background work should never be
able to spend the budget interactive (P0) questions need.

**Fixed 2026-09-25 (uncommitted at time of writing), in two parts:**
- `decisions.Trigger` reuses its last pass of cards for `CardTTL` (new config
  key `decisions.card_ttl_seconds`, default 600) instead of rebuilding them.
  The needs-you ticker, `GET /v1/decisions`, the stage/dismiss/email lookups
  and the brief all share the daemon's one trigger, so evidence is fetched at
  most once per TTL. Card state is never cached: dismissed cards are filtered
  and staged state is read from the store on every request. A pass whose
  context ended mid-build is never cached, and racing callers share one build.
- The gate holds part of every rate cap back for P0. Any origin other than P0
  may use at most 75% of a function's cap (rounded down, at least 1), while P0
  may use all of it. The count is one shared window, so this only makes
  background calls stop earlier. The denial says so: "rate cap for X reached
  for background origin p1 (N of M per ...; the rest is reserved for P0)".

The 900 s `notify.interval_seconds` default stays as a second bound. Not
verified live yet: that needs a daemon rebuild and restart.

## An "isolated" `WATER_HOME` is not isolated from real accounts (found in V-verify, 2026-09-25)

On macOS `runDaemon` always uses the login Keychain, and `gcal`/`gmail` are
always the real connectors, even with `--demo`. So a test daemon in a temp
`WATER_HOME` still holds the owner's real Google credential, and approving a
write there would send real invites or mail. V-verify worked around this
with a harness daemon that uses an empty in-memory vault and recording fakes.
Fix before any future live write test: add a supported override (for
example a test-vault or fake-Google flag) that is never the default, or
reuse the harness pattern. Until then, never approve a write envelope on
any daemon started from this binary unless a real send is intended.

## Slice V revised brief: open gaps (2026-09-25)

- **On-device checks still owed by the owner** (checklist in
  `docs/slices/V-verification-revised.md`):
  - the HUD popping on screen for a tool turn and not for a Tier-0 answer;
  - the blob following the mic, pulsing while thinking, and moving with
    Kokoro;
  - the non-activating HUD's first-click behaviour (could a click aimed at
    another app land on Approve?);
  - the native banner, tap, and second-tap reuse;
  - the held page mic landing in the open thread;
  - Kokoro first-audio latency.
- **One-time model re-download.** Models now live under
  `~/Library/Application Support/Water/Models/`. Kokoro currently sits in
  `~/.cache/fluidaudio/Models/`, so the next launch asks to download again
  (about 1.5 GB). The old copy isn't deleted.
- **The voice bench measures the spoken "One moment." filler** as first
  sentence and first audio on escalated turns. The real time to the first
  answer sentence is about 1.0–3.1 s (from `route_log`).
- **§7.5's "identical `tools_used`" holds only for Tier 0.** Main-path tool
  choice varies from run to run, even on the same channel.
- **`ModelHub.offlineMode` is not set.** A non-default `kokoro_voice` pack
  could download lazily.
- **`reflex.quickDescription` keys on the wrong ids** (pre-existing), so
  every quick tool's model-facing description is the generic default.
  Fixing it restarts the warm session once.
- **No person links are written for the real roster** until `people.yaml`
  has email identities. Links are also a snapshot taken when a thread is
  anchored, and organizers aren't linked.
- **Page mic while voice is busy.** Native ignores the press, but the page
  can still show "Listening…" and never refresh.
- **A stale HUD card can keep its spinner** if a decision returns pending and
  the re-read fails. A step whose `tool_end` never arrives keeps its spinner
  until dismissed.
- **Leftover notification probe entries.** "Water Notify Probe" and "Water
  Probe Two", created by V-notify's permission probe, remain in System
  Settings > Notifications. Harmless.
- **Smaller items:**
  - A thread the mic creates is titled "Voice conversation".
  - Edits are field-based.
  - The "sent" label wasn't seen live.
  - Execution errors after an approval show in the text bar, not on the HUD.

## Found in the V-ui live check (2026-09-25)

- `water daemon` prints the CLI token in plain text to stdout at startup
  (`cli-token=…`). A daemon started with its output redirected to a file
  leaves the token in that file with default permissions. This contradicts
  A1's "tokens never appear in logs" invariant. It predates this session.
  The two local log files were `chmod 600`'d as a stopgap. Fix: print a
  fingerprint or the token's source, not the token.
- The Tier-0 calendar template repeats the day on an empty calendar
  ("Today, Fri 25 Sep:\nNothing on your calendar Today, Fri 25 Sep.").
  Cosmetic.

## Found in the globe-interaction / research-latency build (2026-09-26)

- The movable/dockable globe (hover, drag, dock into the menu bar, the
  double-click transparent-mode/go-to-panel/quit menu) is only verified by
  a headless self-test (`--globe-selftest`) driving a real, never-shown
  `GlobeHUD` with synthetic mouse events. It has never been tried in the
  owner's actual running Water.app, and the `NSMenu`/status-item behavior
  on a notched menu bar is untested.
- `research.web` latency dropped from a 21.2s to a 10.0s median in
  isolated testing (a warm spare `claude --print` process, haiku, thinking
  off, a tighter prompt), but a live voice answer is estimated at ~15–18s
  end to end, still short of the 8–12s target. The floor is `WebSearch`
  itself (3–8.6s, done server-side by the CLI) plus the main model's two
  turns around the tool call; neither is fixable from inside Water. Not
  yet measured against the real daemon. Two levers are identified but
  unbuilt: have the main model speak research's summary directly instead
  of a second turn, and pre-warm the research spare on the voice channel's
  first partial transcript instead of after first use.

## Found in Slice UI, Phase 0 (2026-09-26)

- `internal/mailnoise`'s two test fixtures are hand-synthesized, not pulled
  from the owner's real mailbox — the build's sandboxed shell couldn't open
  `~/.water/water.db` (`SQLITE_CANTOPEN`). Someone with normal (non-sandboxed)
  shell access should pull the two owner-cited message ids and swap them in.
- `runtime/brief.go`'s noise check always passes a nil `wroteTo` (a domain
  the CEO has actually written to still gets no exemption there, only from
  `internal/decisions`' triage path does). Fixing this needs a CEO-address
  config threaded through `runtime.Env`/`gateway.Config`, out of proportion
  for a heuristic already documented as deliberately conservative.
- `needsyou`'s "money" approval-priority signal (U14) is a generic,
  undocumented-elsewhere heuristic (a payload key containing amount/price/
  cost/budget) — no connector actually moves money yet, so it's untested
  against a real one.
- The approval priority rule's "deadline ≤3 days → urgent" half never fires
  today: `Envelope` carries no deadline field.
- The Decisions view computes an equivalent priority client-side
  (`decisionPriorityClass` in app.js, mirroring `needsyou/priority.go`)
  because `decisions.Card` doesn't carry a server-computed `Priority` yet —
  a deliberate duplication, to collapse once it does.

## Found in the adversarial code-review pass (2026-09-26)

- `internal/gateway`'s `Daemon.recaps` map never removes an entry once a
  meeting's recap finishes — a resource leak of the same shape as the
  turn-table one this pass fixed, but bounded by how many meetings are
  ever stopped in a day, so not a practical problem at this app's scale
  yet. A safe fix needs a retention timestamp plus care not to break
  `meetingViewOf`'s fallback-to-store logic for skipped/failed recaps.
- Swift: a mid-`startCapture` audio-device change landing in the narrow
  synchronous window before `MeetingController`'s `state` is set to
  `.active` is silently dropped instead of tearing down/notifying — the
  mic can go dead with no user-visible signal.
- Swift: `VoiceSession.startCapture` (`HoldToTalk.swift`) would silently
  swallow a *synchronous* `.error` from `SpeechCapture.start`. Not
  live-exploitable today (both real conformers, `ParakeetCapture` and
  `OnDeviceSpeechCapture`, always dispatch via `DispatchQueue.main.async`),
  but worth a `SpeechCapture` protocol contract note if a future
  conformer is added that could call back synchronously.
- The Swift meeting-capture data race this pass fixed (`RecognitionStream`
  in `MeetingController.swift`) has no automated regression test —
  `Sources/Water` (the AppKit/AVFoundation/Carbon glue) has no test target
  reaching it. A Thread-Sanitizer run against a real, several-minutes-long
  meeting capture (crossing a few 1.5s pauses and one 45s rotation) is the
  owner's own manual verification step if wanted.

## Carried over from A-series slices (still true)

- Long-term memory (`internal/memory`) is not wired into the runtime, the
  morning brief's signals, or (once built) the decision registry — all read
  the store directly.
- Hosting the daemon off the laptop (tracked in `docs/EVOLUTION_PLAN.md`'s
  log) is unscheduled; `water daemon` currently only runs while the Mac is
  awake.
- Gmail push (Pub/Sub) was explicitly declined in favor of fast polling
  (A4, 2026-09-24); revisit only if polling latency becomes a real problem.
