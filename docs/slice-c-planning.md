# Slice C planning: write functions, the P2 decision, a scenario harness, the CEO environment section

Companion to `docs/slices/C.md` (the decision-registry/generic-frame spec).
This document is the "still in force from my previous instruction" section
of the 2026-09-24 amendment: it's planning and decision-recording, not a
build spec, and none of it authorizes writing code before Slice B is
verified — same block as `docs/slices/C.md`.

## 1. Write functions C will need, the scopes, and the first approval

C-base (`docs/slices/C.md`) is read-only research: it prepares decision
cards but does not add any function capable of an outward effect. The
*next* increment of Slice C — staging real actions from a card, not just
naming them — needs four write functions, none of which exist yet:

| Function | Package | Scope needed | Not needed for C-base |
|---|---|---|---|
| `gmail.draft_message` | `internal/connectors/google/gmail` | `https://www.googleapis.com/auth/gmail.compose` | yes — C-base only reads |
| `gmail.send_message` | `internal/connectors/google/gmail` | `https://www.googleapis.com/auth/gmail.send` (compose alone is enough to draft+send from a draft it created; send is listed separately since a card may want "send" as a distinct approval step from "draft") | yes |
| `gcal.create_event` | `internal/connectors/google/gcal` | `https://www.googleapis.com/auth/calendar.events` (replaces `calendar.events.readonly` for this connector; read functions keep working under the write scope, so no separate readonly-scope function split is needed) | yes |
| `gcal.move_event` | `internal/connectors/google/gcal` | same `calendar.events` scope | yes |

These follow the exact `connectors.Function` shape `gcal.go`/`gmail.go`
already use (see e.g. `gcal.Functions()`'s `list_events` entry): `Level:
twins.A` (never R/D — these have an external effect), `External: false`
for the *call itself* (the twin is originating the content, not receiving
someone else's), and a `connectors.Schema` naming required fields (`to`,
`subject`, `body` for `send_message`; `title`, `start`, `end`, `attendees`
for `create_event`; `event_id`, `new_start`, `new_end` for `move_event`).
`twins/ceo/twin.yaml` would list each under its connector at `level: A`
with a rate cap, the same shape `list_events`/`list_messages` use today at
level R.

**Scope re-consent.** Adding `gmail.compose`/`gmail.send` and swapping
`calendar.events.readonly` for `calendar.events` both require the CEO to
re-run `water connect google` — Google requires re-consent whenever a
requested scope set grows, per A3's PKCE loopback flow
(`internal/connectors/google/gapi`). `gapi.Scopes()` (currently
`{ScopeCalendarEventsReadonly, ScopeGmailReadonly, ScopeDriveReadonly}`)
would grow to include the four scopes above; the two calendar scopes are
mutually exclusive in practice (Google grants the broader one and the
narrower one becomes redundant), so the real change is dropping
`ScopeCalendarEventsReadonly` in favor of `ScopeCalendarEvents` once C's
write functions ship, not stacking both.

### Worked example: staging a budget-request follow-up email

Walking through the first real write call, end to end, using the
mechanisms that already exist:

1. The CEO reviews a `budget_request` decision card (`docs/slices/C.md`
   §4) in `water ask` and says "draft a follow-up asking for the Q4
   breakdown before I approve this."
2. The runtime builds a `StagedAction{Function: "gmail.draft_message",
   Payload: {"to": [...], "subject": "Re: Q3 marketing budget request",
   "body": "..."}}` — the same `StagedAction` shape C.md's `Card` already
   defines.
3. Staging a level-A call creates an `approvals.Envelope` exactly the way
   `gate.authorize` already requires for any A-level function today
   (`NeedsEnvelope(level, taint)` returns true unconditionally for `A`) —
   no new gate code. `approvals.PayloadHash` binds the sha256 of the
   canonical payload; `q.Queue.Propose` (or whatever proposes it — the
   mechanism `gateway`'s model-tool bridge already uses when a model-
   initiated call needs an envelope) writes it to the approval queue and
   audits a `propose` record.
4. **The spoken read-back is already half-built for this exact action.**
   `internal/approvals/readback.go`'s `summary()` already special-cases
   `send_email` by short name (`shortName(e.Action)` strips the package
   prefix): `"Send email to %s, subject '%s'. Body begins: '%s'."`. Once
   the real function is named `gmail.send_message` (or a `draft_message`
   case is added alongside it), `ReadBack` needs only a matching `case` —
   the clipping, recipient-formatting, and yes/no prompt machinery
   (`clip`, `recipients`, `prompt`) are already written and tested. This
   is the concrete evidence that C's approval UX was anticipated, not
   improvised: **the first write call exercises code that already
   exists**, not a new subsystem.
5. `water approve` (today's CLI; Slice B's spoken yes/no once built) reads
   back: *"Send email to Dana Priyanka <dana@…>, subject 'Re: Q3 marketing
   budget request'. Body begins: 'Could you share the Q4 breakdown before
   …'. Say yes to send or no to cancel."* A "yes" decides the envelope,
   which — per A2's existing rule ("Deciding a pending approval 'yes' now
   executes the action in that same call, exactly once") — calls
   `gmail.draft_message` (or `send_message`) through the gate in the same
   round trip, audited, exactly once.

Nothing here needs new approval-queue mechanics, a new envelope shape, or
a new read-back renderer architecture — only two new connector functions,
two new manifest entries, and (for `send_message`/`create_event`/
`move_event`) new `case` branches in `readback.go`'s `summary()`/`prompt()`
next to the ones already there for exactly these two action families.

## 2. The P2 gate decision: DECIDED, stays strict

**Decision: P2 (auto mode) stays strict — no outward actions — until a
scenario proves the need.** This is not an open question; it's already
true in code and this section documents why it should stay that way
through Slice C.

`internal/gate/gate.go`'s `authorize` (around line 272) already enforces
this unconditionally for every call, not just today's read functions:

```go
if c.Origin == P2 {
    if !g.cfg.Manifest.AutoAllowed(c.Function) {
        return nil, none, "", false, deny("auto mode may not call %s (not on the auto allowlist)", c.Function)
    }
    if (f.Level != twins.R && f.Level != twins.D) || spec.Level == twins.A {
        return nil, none, "", false, deny("auto mode never performs outward actions (%s is level %s)", c.Function, f.Level)
    }
}
```

A P2 call must be on `auto_allowlist` **and** be level R or D **and** not
be a level-A spec — three independent checks, all of which a future
`gmail.send_message`/`gcal.create_event` (level A) fails automatically by
construction, with zero code change needed when C's write functions ship.
`twins.Manifest.auto_allowlist` validation (manifest.go line 183) already
refuses to even load a manifest that lists an R/D-violating function on
the allowlist, so this can't be loosened by editing `twin.yaml` alone
either — it would need a deliberate code change to `gate.authorize` itself.

**Why keep it this way through C.** C's whole model is "prepare unlimited,
act only through the gate" (`docs/slices/C.md`'s Principle). Auto mode
(P2) is specifically the twin acting *without* a CEO turn in progress —
exactly the situation where a wrong staged-and-somehow-executed action is
hardest to catch quickly. Standing grants (pre-approved classes of
action) are explicitly deferred (`docs/known-gaps.md`); until they exist,
there is no mechanism by which a P2-originated action could be safely
autonomous even if the level allowed it. The amendment names scenario S8
as the trigger to reconsider this — see the scenario-harness proposal
below for what "a scenario shows the need" would concretely mean.

No code change is needed for this section. It's a record of the decision
and the reasoning, so it isn't re-opened by a future session without
cause.

## 3. Scenario harness proposal

**No pre-existing numbered scenario list was found in this repo.**
Searched `PROGRESS.md`, `docs/archive/decisions-council-era.md`, and every
other `docs/*.md` for "scenario S" or "gate test" as a numbering
convention; nothing matches. The closest existing thing is the council-era
hierarchy-run gate tests in the archived `PROGRESS.md` history (named runs
like "Hierarchy run A (payments migration)", not numbered), which predate
the CEO twin and used a completely different architecture. **S8 and S13
are used here exactly as the amendment states them, as arbitrary
identifiers for scenarios this document and `docs/slices/M.md` define —
not as continuations of a numbering scheme that doesn't exist.** If a
scenario harness is built, it should mint its own S-numbers going forward
starting from whatever the amendment's examples establish (S8, S13 are
already spoken for; the harness's own scenario list would need to account
for the numbers in between when it's actually built, since none of S1–S7,
S9–S12 have been defined by anyone).

**What a scenario is, concretely, in this repo's style.** Looking at how
existing packages test cross-cutting behavior:

- `internal/guards` (`gate_test.go`, `google_test.go`, `metered_leak_test.go`,
  `tools_test.go`) tests *invariants that span packages* — a real gate, a
  real manifest, and a misbehaving connector (`leaky`) wired together, then
  asserted against. This is the closest existing pattern to "a scenario":
  it builds a small real system (not mocks-all-the-way-down) and exercises
  it end to end.
- `internal/sync/sync_test.go` builds a fake manifest inline (`testManifest`
  as a YAML string constant), fake connectors (`incrConnector`), and drives
  a real `Gate`+`Store`+`Refresher` through several ticks, asserting on
  store/cursor state afterward.
- `internal/runtime`'s brief tests use a fake `backend.Backend` and a real
  `store.Store` to assert the compute-once/taint behavior against realistic
  signal data.

None of these is a general "scripted scenario" runner; each test hand-
builds its fixture. A **lightweight scenario harness**, not a framework,
would generalize the shape all three already share:

```go
// internal/scenario (new, test-only package — imported by _test.go files,
// never by production code)
type Scenario struct {
    Name    string
    Seed    []store.Record        // synthetic records inserted before the script runs
    Script  []Turn                // a sequence of turns to send
    Expect  func(t *testing.T, tl *Timeline) // assertions against what happened
}

type Turn struct {
    Kind  TurnKind // Ask | Approve | Deny | Segment (M's replay endpoint)
    Text  string   // the CEO's ask, or a transcript segment's text
    At    time.Time
}

// Timeline records what a Scenario run actually produced: turns, tool
// calls, envelopes proposed/decided, and cards built — built from the
// same real Gate/Store/Queue/Audit a normal daemon uses (in-memory
// vault, temp-file SQLite, fake connectors where an external API would
// otherwise be called), so a scenario is exercising real wiring, not a
// second, parallel mock of it.
type Timeline struct { /* ... */ }

func Run(t *testing.T, s Scenario) *Timeline
```

This is deliberately *not* a YAML-driven DSL, a separate scenario-file
format, or a new CLI subcommand — just a shared Go helper a few packages'
tests import, following the existing "small helper, not a framework"
posture the rest of this codebase uses (compare `internal/connectors/fake`,
which plays the same role for individual connector calls that `scenario`
would play for a whole turn sequence). A scenario lives as an ordinary Go
test function in whichever package's behavior it's really testing
(`internal/decisions` for the generic-frame scenario below, `internal/meetings`
for M's S13), using `scenario.Run` to avoid re-deriving the seed/script/
assert boilerplate each time.

## 4. CEO environment section (`twins/ceo/role.md`)

`twins/ceo/role.md` already has a stub for this ("Environment (fill in for
your company)" — Company, Projects, People, Tools, one-liners each). This
proposes expanding it into three structured subsections with placeholder
example values (not the owner's real company data, which this session
doesn't have and won't invent as if real):

```markdown
## Environment

### Company
- **Name:** _Example Corp_
- **Stage:** _Series A, ~18 months runway_
- **Size:** _22 people_
- **What it makes:** _one sentence_

### Projects
One line each: name, one-line status, owner.
- **Atlas** — _core product rewrite, on track for Q1_ — owner: _CTO_
- **Compass** — _enterprise pilot, blocked on SOC2_ — owner: _Head of Sales_

### People
Names and what to know about working with them — direct reports,
co-founders, key investors.
- **Priya Shah** — _co-founder/CTO. Prefers async written proposals over
  live debate; escalate infra cost questions to her directly._
- **Dana Okafor** — _lead investor (Series A). Wants a monthly written
  update by the 5th; dislikes surprises more than bad news._

### Budget structure
- **Runway source:** _e.g. a Drive spreadsheet, name/id_
- **Approval thresholds:** _e.g. under $2,000: CEO alone; $2,000–$10,000:
  CEO + Priya; over $10,000: board notice required_
- **Burn categories:** _payroll, infra, tools, contractors, other_
```

This schema is what `docs/slices/C.md`'s `budget_request` type reads
against (the Drive spreadsheet named under "Budget structure", the
approval-threshold prose informing a `default_rule`), and what a
`generic`-frame card's "who is affected" section would draw on (the
People list). Filling it with the real company's data is the owner's own
task, not something this session should fabricate.

## 5. Example scenario: a decision type no playbook covers

Exercises the generic frame (`docs/slices/C.md` §2), proposed as a
concrete scenario (not yet built, since scenarios themselves are C-base
territory per the harness proposal above):

**Narrative.** A message arrives from a conference organizer inviting the
CEO to give a paid keynote in eight weeks, with a modest honorarium, travel
covered, and a request for a bio and slide deck by a two-week deadline. No
registered decision type matches: it isn't a budget request (no money
going out), an investor request, or an inbound support decision. `Classify`
returns `{NeedsDecision: true, TypeID: "generic", Confidence: 0.9}` (a
correct, confident "this is generic," not a false match against
`budget_request`).

**Expected card shape:**
- `Lead`: "Conference keynote invite, 8 weeks out, response needed in 2 weeks."
- `Question`: "Accept the speaking invite?"
- `Deadline`: two weeks from receipt (parsed from the email, in code —
  a date-extraction utility, not a model guess, per C's "numbers come
  from code or tools only" rule; if extraction fails, `Deadline` is nil
  and this fact appears in `Gaps`, not silently dropped).
- `Evidence`: the invite email itself (`Source: "gmail:msg-..."`), and
  (from the generic frame's own `needs`) any prior correspondence with
  this organizer if the local FTS/connector search finds one.
- `Options`: accept / decline / counter-propose (e.g. a shorter slot, or
  a different date), each with a stated consequence (time cost, travel,
  visibility) in prose — no invented numbers beyond what the invite states.
- `Gaps`: e.g. "no travel dates confirmed yet," "unclear if this conflicts
  with the board meeting the same week" (a scheduling conflict *should* be
  checked in code against `store.EventsInRange` if the invite's dates are
  known — this is exactly the kind of check the generic frame's "what do
  the connected tools already tell us" step is for).
- `Recommendation`: left empty unless the evidence clearly supports one
  (e.g. if the date is a confirmed hard conflict with an existing event,
  code can state that fact, but the card still doesn't recommend "decline"
  on the model's own authority — a scheduling conflict is evidence, stated
  in `Gaps`/`Evidence`, not a recommendation the twin makes).
- `StagedActions`: empty (nothing to send without the CEO's decision) or,
  if the CEO responds "draft a polite decline," a `gmail.draft_message`
  staged action (once C's write functions exist — see §1).
- `Readiness`: `missing_info` (travel-date confirmation and the
  board-meeting conflict check are open) unless both resolve cleanly, in
  which case `ready`.
- `Untrusted`: `true` (sourced from an external email).

This is the scenario a harness test (`internal/decisions`, using
`scenario.Run` from §3) would encode: seed a `store.Message` fixture
matching the narrative, script one turn ("what should I do about this
speaking invite?"), and assert the resulting `Card` has `TypeID:
"generic"`, non-empty `Gaps`, and no `Recommendation` unless the fixture
data supports one.
