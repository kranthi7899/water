# Slice C (base): flexible decisions

**BLOCKED on Slice B — do not implement until B is verified.** The owner's
own sequencing (2026-09-24 amendment) is: finish A4 (done) → B (text
pop-up first, voice second) → C-base (this spec) → M. This document is the
spec to build C-base against once B ships; it authorizes no code yet.

Builds on A1–A4: `gate` (R/D/A/S/B levels, taint, rate/usage caps),
`approvals` (payload-hash envelopes, code-generated read-back),
`audit`, `store` (SQLite, normalized records: `Message`, `Meeting`,
`Event`, `Document`, `Issue`, `Commit`, `Transaction`, `Contact`), and the
real Google connectors. Before C, the twin can read and summarize; it has
no notion of "this item needs a decision" and no structured way to prepare
one. C-base adds that, and nothing more — see the "still in force" notes
in `docs/slice-c-planning.md` for the write functions and gate questions
this slice's *next* increment (staging and sending real actions) will need,
which are answered but not built in C-base.

## Principle

What the twin can *prepare* is unlimited — preparing is read-only research
against connectors already at level R. What it can *do* stays bounded by
the manifest and the gate, exactly as today. **The twin never makes a
decision.** It prepares one and stages actions for approval. Nothing in
this slice adds a new outward-facing gate level or loosens P2 (see
`docs/slice-c-planning.md`'s P2 decision).

## Scope

### 1. Decision-type registry

One file per type in `twins/ceo/decisions/*.yaml`, following the same
"plain file the twin loads, validated at startup, fails loudly" pattern
`twins/ceo/twin.yaml` already establishes for the manifest. A new package,
`internal/decisions`, owns loading and validation — it does not live under
`internal/runtime` because the registry is a static, twin-scoped resource
loaded once (like `internal/twins`), not per-turn context assembly.

Each file:

```yaml
id: budget_request              # stable id, referenced by decision cards
title: Budget request
trigger: >-
  A one-line description used for triage matching, e.g. "someone is asking
  for money: a purchase, a renewal, a headcount budget, a raise."
needs:
  - name: requester_history
    fetch: gmail.list_messages       # a real connector function name from
                                      # the manifest; the registry loader
                                      # rejects an id that isn't in
                                      # twins/ceo/twin.yaml
    kind: lookup                     # lookup | computation | research
  - name: runway_and_burn
    fetch: internal://budget_request.compute_runway   # a code computation,
                                      # not a connector call — see below
    kind: computation
  - name: comparable_requests
    fetch: gdrive.search_files
    kind: research
default_rule: >-
  Approve if the amount is under $2,000 and runway stays above 6 months
  after it; otherwise route to the CEO with the full packet.
staged_actions:
  - send_email      # action ids the packet may propose; C-base does not
                     # build these functions (see slice-c-planning.md) —
                     # a card can name them, but nothing executes until
                     # they exist in the manifest at level A
severity_weight: 2  # used only to rank cards in the brief; not a probability
```

Followed by a free-text notes body (markdown, same file) for anything a
human maintaining the registry wants to say that doesn't fit the header —
edge cases, why the default rule is set where it is, examples.

**Validation at startup.** `internal/decisions.LoadRegistry(fsys)` parses
every `*.yaml` under `twins/ceo/decisions/`, validates against a fixed Go
struct (not a generic schema library — consistent with how
`twins.Manifest` is validated today, a plain Go `Validate()` method), and
**fails loudly**: a malformed file (missing `id`, a `needs[].fetch` that
names a connector function not present in `twins/ceo/twin.yaml`, a
duplicate `id` across files) stops the daemon from starting, the same way
an invalid `twin.yaml` does today (`twins.Load` already refuses to start on
a bad manifest — this reuses that posture, not a new one).

**Index kept in context; full file loaded on match.** The model's context
gets only `id` + `trigger` for every registered type (a few lines each,
cheap even with dozens of types) via `Registry.Index() []IndexEntry`. The
full YAML (needs, default rule, staged actions) loads only when
`Classify` (below) picks that type for a specific item — mirroring the
"index vs. full file" split `docs/CONTEXT.md`'s memory tiers describe for
long-term memory, applied here to decision types instead.

### 2. The `generic` decision type

Always present, not a file under `twins/ceo/decisions/` (so it can't be
deleted or malformed) — a Go-literal `Registry` entry, `id: "generic"`,
returned by `Registry.Generic()`. When `Classify` finds no registered type
above its confidence floor, the packet is built from the generic frame
instead of being dropped:

- What exactly is being decided, and by when.
- Who asked, and who is affected.
- What each option costs: money, time, people.
- Whether it's reversible.
- What the connected tools already tell us (a `needs` list built the same
  way a real type's is, but generic: recent related messages/documents by
  keyword match against the item, not a type-specific fetch).
- What's missing.

This must produce a usable packet for a decision nobody anticipated — the
amendment names a vendor renewal, a speaking invite, a hiring question. It
uses the same `DecisionCard` shape as every other type (section 4); it
just has no `default_rule` (a generic card is never auto-approved, however
small) and its `staged_actions` list is usually empty (a generic packet
recommends what to prepare, not what to send).

### 3. Triage behind an interface

```go
// internal/decisions/classify.go
type Classification struct {
    NeedsDecision bool
    TypeID        string  // a registered id, or "generic"
    Confidence    float64 // 0..1, informational — not itself a gate
}

type Classifier interface {
    Classify(ctx context.Context, item store.Record) (Classification, error)
}
```

A model-based implementation (`ModelClassifier`, using the `fast` model
tier — this is P0/P1 work reacting to something the CEO or a sync tick
surfaced, not P2 background drafting) is the only implementation in
C-base: it's handed the item plus the registry's index (id + trigger for
every type) and asked to pick one or say "generic". Confidence below a
configurable floor (default 0.5) also falls through to `generic` even if
the model named a type — a wrong "confident" guess is worse than an honest
generic packet.

Keeping this behind one interface is the whole point: a later, faster
non-model classifier (keyword rules, an embedding lookup) can replace
`ModelClassifier` without touching the registry, the card builder, or the
gate wiring, per the amendment's explicit deferral of "a dedicated
classifier model" to later (`docs/known-gaps.md`).

### 4. The decision card

```go
// internal/decisions/card.go
type Card struct {
    ID            string
    TypeID        string          // registered id, or "generic"
    Severity      int             // from severity_weight, or a generic default
    Lead          string          // one line
    Question      string          // what's actually being decided
    Deadline      *time.Time
    Evidence      []Evidence      // each item below
    Options       []Option
    Gaps          []string        // stated openly, never hidden
    Recommendation string         // "" when evidence doesn't support one
    Defaults      map[string]any  // computed, not model-written
    Parameters    map[string]any  // editable before staging
    StagedActions []StagedAction
    Readiness     Readiness       // ready | missing_info | blocked
    SourceItemIDs []string        // store.Meta (Source, SourceID) refs
    Untrusted     bool
}

type Evidence struct {
    Text   string
    Source string // e.g. "gmail:msg-abc123", "code:runway_calc"
}

type Option struct {
    Label        string
    Consequences string
}

type StagedAction struct {
    Function string         // e.g. "gmail.send_email" — must exist in the
                             // manifest at level A once C's write functions
                             // ship; C-base can produce a StagedAction that
                             // names a function that doesn't exist yet
                             // (send_email/create_event aren't built in
                             // C-base — see slice-c-planning.md) and mark
                             // it not-yet-actionable rather than erroring.
    Payload  map[string]any
}

type Readiness string
const (
    Ready       Readiness = "ready"
    MissingInfo Readiness = "missing_info"
    Blocked     Readiness = "blocked"
)
```

**Readiness rule, in code, not the model's judgment:** `ready` when every
`needs[]` entry for the matched type resolved to a non-empty result;
`missing_info` when at least one `needs[]` fetch returned empty/not-found
but nothing errored; `blocked` when a fetch errored (a denied gate call, an
expired token, a connector error) or a required computation couldn't run.
This is a pure function of the fetch results, computed the same way
`runtime.brief.go` computes its signals in code before the model ever sees
them — the model is never asked "is this ready?".

**Numbers come from code or tools only.** Every `Evidence.Source` and every
numeric value in `Defaults` traces to either a connector call result or a
named Go function (`internal://budget_request.compute_runway` above,
implemented as an ordinary Go function registered per type — not a model
completion). The card builder rejects (fails the build, falls back to
`missing_info`) a numeric field with no source, rather than let a
model-invented number reach a card silently. The model's only job on a
card is prose: the `Lead`, phrasing `Gaps`, and (when evidence supports it)
`Recommendation` — never a number that didn't come from `Evidence` or
`Defaults`.

**Rendering.** `Card.Render() string` for the CLI (full detail, same
"code builds the text, model doesn't" posture as `approvals.ReadBack`) and
`Card.Speak() string` for a short spoken summary (lead + question +
deadline + readiness, following `voice.Speakable`'s existing
markdown-stripping convention). The morning brief (`internal/runtime/brief.go`)
gets a new signal — ranked open cards, sorted by `Severity` then
`Deadline` — rendered the same way today's events/messages signals are:
computed in code, handed to the model only to phrase around.

### 5. Shipped types

- **`generic`** — section 2, always present.
- **`budget_request`** — built against a Drive spreadsheet. `needs` include
  a `gdrive.read_file` (or a new, C-scoped `gsheets`-shaped read through
  the existing `files.export` path A3 already uses for Sheets) for the
  budget/runway spreadsheet, plus `gmail.list_messages` for the requester's
  history. **Runway and burn are computed by code** (a small Go function
  parsing the exported CSV/TSV rows into numbers — no model arithmetic),
  exposed as an `internal://budget_request.compute_runway` need. This is
  the one type C-base actually exercises end to end.
- **`investor_request`**, **`inbound_decision`** — registry entries only
  (a valid YAML file with `id`, `title`, `trigger`, at least one plausible
  `needs` entry, and a `default_rule`), proving the registry holds more
  than one real type without building either out. `Classify` can match
  them; their cards will be sparse (mostly `missing_info`, since their
  `needs` fetches aren't wired to real logic) until a later slice fills
  them in. This satisfies the amendment's "stub as registry entries" ask
  without pretending they're finished.

## Do not build yet

Per the amendment, explicitly out of scope for C-base (tracked in
`docs/known-gaps.md`):

- Proposing new playbooks from repeated cases.
- Standing grants (pre-approving a class of future actions).
- Probabilities on options.
- A dedicated (non-model) classifier.

## Tests

- **Registry validation.** A malformed file (bad YAML, missing `id`, a
  `needs[].fetch` naming a connector function absent from `twins/ceo/twin.yaml`,
  a duplicate `id`) fails `LoadRegistry` loudly; a well-formed set of files
  loads and `Index()` returns exactly their `id`+`trigger` pairs.
- **Generic fallback.** An item that matches no registered type (or whose
  best match falls below the confidence floor) still yields a valid `Card`
  with `TypeID: "generic"` and a non-empty `Gaps` or `Question` — never a
  dropped item, never a panic.
- **Every figure has a source.** A property-style test builds cards across
  fixture items and types and asserts every value in `Defaults` and every
  `Evidence` entry carries a non-empty `Source`; a hand-built card with a
  sourceless numeric field is rejected by the builder.
- **Readiness labels.** A fixture where every `needs[]` fetch succeeds
  yields `ready`; one with an empty (not-found) fetch yields
  `missing_info`; one with a fetch error (simulated gate denial or
  connector error) yields `blocked`.
- **Untrusted flag.** An item sourced from `External: true` content (an
  email, a Drive doc) produces a card with `Untrusted: true`; an
  item with no external source does not.
- `go vet ./...`, `go test -count=1 ./...`, `CGO_ENABLED=0 go build ./cmd/water`
  green, as always.

See `docs/slice-c-planning.md` for: the exact write functions and OAuth
scopes C's *next* increment (staged actions that actually execute) will
need; the P2 gate decision (staying strict); a scenario-harness proposal;
the CEO environment section of `role.md`; and a worked generic-frame
scenario.
