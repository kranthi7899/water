# Slice I: decisions analysis lane — facts ledger (`facts.get`) plus advisory-only interpretation

**PLANNING ONLY. Do not implement until Slice H is verified.** This is one
sub-slice of the intelligence-layer redesign (F through Q). The redesign's
overview doc holds the ordering and the full open-questions list. This file
is the spec I is built against once H ships, and it authorizes no code yet.
It follows `docs/WORKFLOW.md`'s Explore → Plan → Approve → Implement loop.
Phase 1 (Explore) findings are below. Phase 2's plan still needs the
owner's approval before Phase 3 starts.

Covers brief §3: *"Decisions engine: add an analysis lane, don't loosen the
existing one."*

Builds on C-base and the C write increment (`internal/decisions`: `Registry`,
`Builder`, `Card`, `ModelPhraser`, the `internal://` compute registry), the
`company_finance` connector (`internal/connectors/google/gsheets`, migration
`0012_finance_figures.sql`, `store.FinanceFigure`), the gate's model-tool path
(`daemon.twinFunctions()` / `TwinToolPolicy` / `handleToolInvoke`, with P0 and
session-sticky taint), and H's skill files.

## Dependencies

- **H (hard).** H ships the `analysis` (analysis/modeling) and `decision_support`
  skills with only the tools that exist today. I adds `facts.get` to both
  tool lists. I must not invent a skill mechanism of its own. If H is not
  verified, I's tool can still be granted in `twin.yaml`, but the skill
  wiring waits.
- **Precondition (not a slice).** The `company_finance` connector, `0012`
  and `store.FinanceFigure` are **uncommitted in the working tree** as of
  2026-09-25 (`git status` shows `?? internal/connectors/google/gsheets/`,
  `?? internal/store/finance_records.go`, `?? internal/store/migrations/0012_finance_figures.sql`,
  and ` D internal/decisions/budget_request.go`). They must be committed
  before I starts, because I's ledger reads that table.
- **Not dependent on F/G (memory) or J/K (tasks).** The ledger holds figures,
  not memories. It never writes to `internal/memory`.
- **Downstream consumers:** L (the composer's grounded claims can cite ledger
  facts), P (a monitor can watch a `facts.get` metric against a threshold), and
  O/Q through the skills.

## 0. Findings. Corrections to the outline, read this first

Reading the real code changed three things the outline assumed.

1. **`compute_runway` no longer exists.** The outline names
   `decisions.RegisterCompute` "e.g. compute_runway/compute_deal_health" as
   candidate backing. In the working tree `internal/decisions/budget_request.go`
   is deleted: `company_finance.cash_position` / `budget_status` replaced
   the CSV path (EVOLUTION_PLAN log, `company_finance` entry). The only
   registered compute is `internal://investor_request.compute_deal_health`,
   and only the **demo** twin's `investor_request.yaml` uses it.
2. **The `internal://` compute registry is the wrong shape for a ledger.** A
   `ComputeFunc` takes `ComputeInput{Item store.Record, Resolved map[string]NeedResult}`,
   which is the triggering item plus the needs one card already resolved. It
   is a per-card step with no gate access "by design", not a
   `(metric, period) → figure` lookup. I therefore **mirrors its posture**
   (register at init, panic on a duplicate or empty name, arithmetic in Go
   only, every figure carries a `Source`), but it keeps its **own** metric
   registry in a new package. It does not call `RegisterCompute`, so the
   strict lane's registry stays untouched.
3. **`finance_figures` has no period dimension and keeps no history.**
   `UNIQUE (source, source_id)` means every read upserts over the previous
   one. The row is "the last live read", stamped by `updated_at` and by the
   connector's own `read_at`. Only `cash_position` yields named numeric
   fields (`cash_usd`, `burn_usd`, `runway_months`). `budget_status` /
   `spend_breakdown` return one raw matched `row` plus column letters. The
   package deliberately does not trust header positions. `revenue_by_application`,
   `funding_history` and `outstanding_invoices` return raw grids. So at
   build time `facts.get` can serve **`period: "current"` for the three
   `cash_position` metrics only**, with full confidence. Every other metric,
   and any non-current period, needs a column/period mapping the owner
   confirms (see Risks, item 1). A metric the ledger cannot map returns an
   explicit `not_available`, never a guess.
4. **An existing zero-for-missing hazard.** `gsheets.cashPosition` discards
   `get()`'s `ok` flag, so an empty cell reads as `0` and is stored as a
   real number. A ledger that republishes that value as a "sourced fact"
   would launder a missing cell into `runway_months: 0`. This is outside I's
   scope to fix silently. Per WORKFLOW.md it goes into `docs/known-gaps.md` at
   build time. I's ledger must not *add* to the hazard: see acceptance
   criterion 4 (a metric whose producing function cannot distinguish zero
   from missing is flagged `zero_may_mean_missing` in the result).
5. **The existing "email this card" path is the one place a card becomes an
   envelope.** `internal/gateway/decisions.go` finds a `*decisions.Card` by
   id, renders `card.HTMLReport`, and calls `d.cfg.Approvals.Propose` with a
   `gmail.send_message` envelope. That handler is typed to `*decisions.Card`.
   I's separation guard (§6) is written against this real path.
6. **Finance output is `External: true`.** Every `company_finance` function
   is declared `External` (`gate.go` sets `Result.Untrusted = spec.External`).
   A ledger fact derived from it must carry `External` forward, so reading
   a fact taints the session exactly as calling `company_finance.*` directly
   would. Once tainted, an S-level call queues instead of running inline.
   The ledger must not become a taint-laundering path.

The brief's §3 text parses cleanly. I found no corrupted wording in the
part this slice covers.

## Principle

Two epistemics, kept apart, never merged:

- **Sourced (the existing strict lane, unchanged).** Every number traces to a
  connector result or a code computation and is checked field by field.
  This lane is the only one allowed to become, or justify, an outward action.
  `Card`, `Builder.quarantine`, `Card.Validate`, `ModelPhraser` and
  `applyProse` keep their current behavior byte for byte.
- **Interpretation (new).** Open reasoning over sourced facts, written freely by
  the model and labeled as its own interpretation. It is advisory only and
  **structurally unable** to carry a `StagedAction`, produce an approval
  envelope, or be merged into a `Card`. This is enforced by the type system and by
  guard tests, not by convention.

The model decides live which facts it needs, per H's skills posture and Slice
R's `quick.*` precedent. Nothing hardcodes which metrics an analysis uses.

## Scope

### 1. The strict lane: explicitly unchanged

No edits to `card.go`, `build.go`, `compute.go`, `registry.go`, `classify.go`
or `twins/ceo/decisions/*.yaml`. The only permitted touch to `phrase.go` is
§4's behavior-preserving extraction of the number tokenizer, and the rule
is that every existing `internal/decisions` test passes **unmodified**. If
the extraction cannot meet that bar, the analysis package copies the
tokenizer instead and `phrase.go` is not touched at all.

### 2. The facts ledger: new package `internal/facts`

```go
// internal/facts/facts.go
type Fact struct {
    Metric   string    // catalog id, e.g. "cash.runway_months"
    Period   string    // "current" in I's first cut; see Risks 1
    Value    float64
    Unit     string    // "usd" | "months" | ...
    Source   string    // "company_finance:Cash & runway!B19:B21", same
                       // "<Source>:<SourceID>" citation form decisions.Ref builds
    AsOf     time.Time // the connector's read_at, else the row's updated_at
    External bool      // carried from the producing function's spec
    Flags    []string  // e.g. "stale", "zero_may_mean_missing"
}

type Result struct {
    Fact         *Fact  // nil when Status != "ok"
    Status       string // "ok" | "not_available" | "no_data"
    Reason       string // code-written, e.g. "call company_finance.cash_position first"
}

type MetricFunc func(ctx context.Context, st *store.Store, period string) (Result, error)

func RegisterMetric(id string, spec MetricSpec, fn MetricFunc) // init-time; dup/empty panics
func Catalog() []MetricSpec                                     // id, unit, periods, description
```

- **Reads the store, not the network.** A metric function reads
  `finance_figures` (and, later, other normalized records) and does its
  arithmetic in Go. It never calls a connector, because it has no gate access, like
  `ComputeFunc`. If no row exists, the result is `no_data` with a
  code-written hint naming the R-level function that would populate it
  (e.g. `company_finance.cash_position`). The model can then call that
  function live and retry. The model decides what to fetch, and the ledger never
  fetches on its own.
- **Staleness is reported, not hidden.** `AsOf` is always set. A fact older than
  a configurable age is still returned but carries the flag `"stale"`. See
  Risks 2 for the default.
- **First-cut catalog** (the only entries with unambiguous code semantics today):
  `cash.cash_usd`, `cash.burn_usd`, `cash.runway_months`, all `period:
  "current"`, all flagged `zero_may_mean_missing` until finding 4 is fixed
  upstream. Other metrics are added only once their column mapping is
  owner-confirmed.
- **Unknown metric or period** returns `not_available` plus the catalog ids.
  It never returns an error that the model might paper over, and never a
  nearest-match guess.

### 3. `facts.get` as a gate-checked internal connector

A `facts` connector (`internal/facts/connector.go`) implements
`connectors.Connector`, with `Credential()` returning `"", ""` and
`internal/twinlink` as the precedent for an internal, non-Google connector:

- `facts.get(metric, period)`: level **R**, `Risk: low`, `External: true`
  (finding 6), rate-capped in `twins/ceo/twin.yaml` like the other R reads.
- `facts.catalog()`: level **R**, `External: false`. It returns metric ids,
  units, supported periods and one-line descriptions, all static code data.
- `Normalize` returns no records. The ledger is derived, and re-storing it
  would create a second copy of the truth.

These reach the model through the existing `daemon.twinFunctions()` →
`TwinToolPolicy` → `handleToolInvoke` → `gate.Invoke` path, with audit, taint
and rate caps included, just by being registered and granted. There is no new
tool plumbing. Not added to `auto_allowlist`: background use is P's business.
The daemon wiring passes the `*store.Store` into the connector at construction.

`facts.get` is also usable as a `needs[].fetch` in a decision-type YAML
(it is a manifest function at level R, so `validateNeed` accepts it). That
is how a ledger fact may enter the **strict** lane: through the ordinary
connector-evidence path, with its `Source` citation, **never** through an
`Analysis`. I ships no YAML change that does this. It only confirms the path
works (acceptance criterion 7).

### 4. The `Analysis` output type: new package `internal/analysis`

```go
// internal/analysis/analysis.go
type Analysis struct {
    ID             string
    Question       string       // what the CEO asked
    Facts          []facts.Fact // code-resolved this call, each with Source
    Interpretation string       // model-written, free text
    ModelDerived   []string     // number tokens in Interpretation that match
                                // no Fact value; computed by code, rendered
                                // as "the model's own figure"
    Untrusted      bool         // any Fact External, or Question from external content
    CreatedAt      time.Time
}

const Label = "Interpretation: Water's own reasoning over the figures above. Advisory only; not a sourced figure and not a decision."

func (a *Analysis) Render() string // code builds the text; Label always first
func (a *Analysis) Speak() string  // Label's short spoken form always first
```

Deliberately **absent**: `StagedActions`, `Payload`, `Parameters`,
`Defaults`, `Readiness`, `Recommendation`, and any `map[string]any` field. The
struct has no method returning an `approvals.Envelope` or a
`decisions.StagedAction`.

**Numbers are labeled, not banned.** The interpretation lane may reason
("if burn holds, the runway is roughly N months after the hire"). Code
tokenizes the interpretation with the same lexical tokenizer `applyProse`
uses (`numbers()` in `phrase.go`: digit runs with glued suffixes and
number words). Any token that does not match a `Fact.Value` / `Fact` text is
listed in `ModelDerived`, and `Render` marks those figures as the
model's own. This keeps the lane free while keeping the two epistemics
visible, with the same tokenizer and the same lexical limits `phrase.go`
already documents.

### 5. The composer for an analysis: `analysis.Composer`

```go
type Composer struct {
    Backend backend.Backend
    Model   string
    Timeout time.Duration
    Charge  func() error // wired to the gate's ModelCall usage cap, as ModelPhraser.Charge is
    Ledger  *facts.Ledger
}

func (c *Composer) Compose(ctx context.Context, question string, metrics []string) (*Analysis, error)
```

- Resolves the requested metrics through the ledger **in code** first, then
  makes **one cold `backend.Run`** with a fixed system prompt. This is the house
  pattern of classify/phrase/recap/`promote.Draft`, and it never uses the warm session.
  Facts go in a fenced block. The system prompt says that interpretation is
  labeled as the model's own and that nothing the model writes is an action.
- The `metrics` list is chosen by the caller: the CEO via CLI/endpoint, or
  the model when it calls `facts.get` live in chat. The composer does not
  pick metrics on its own, and an empty list is allowed (a pure-reasoning
  analysis with no facts, which then renders with `Facts: none`).
- All model calls go through the subscription CLI backend. Tests use `backend.Fake`.
- CEO-initiated (P0) only in I. No background or P2 analysis: the monitor
  archetype (P) decides whether that ever happens.

Named `analysis.Composer` to stay distinct from L's task/report Composer. L
may later call it or subsume it. That is L's decision, not I's.

### 6. Separation, enforced in code

1. **Import graph (guard test, `internal/analysis/guard_test.go`).** It uses
   `go/build` / `go list -deps` over the package set:
   - `internal/decisions` and `internal/approvals` do **not** import
     `internal/analysis`.
   - `internal/analysis` does **not** import `internal/approvals`, and it imports
     nothing from `internal/decisions` except, at most, a shared tokenizer
     (or none, if §1's fallback is taken).
2. **Struct shape (reflection test).** `analysis.Analysis` has no field whose
   type is, contains, or is a slice/map/pointer of `decisions.StagedAction`,
   `decisions.Card`, `approvals.Envelope`, or `map[string]any`. `decisions.Card`
   has no field of any `internal/analysis` type.
3. **Gateway rule (AST test over `internal/gateway`).** No function that
   references an `analysis.*` identifier also calls `Approvals.Propose`
   (or any `approvals` constructor). The existing "email this card" handler
   stays typed to `*decisions.Card`. The test is written so it fails if
   someone later adds an "email this analysis" handler without revisiting
   this spec (see open point below).
4. **Response shape.** `GET /v1/decisions` and `decisions.Card`'s JSON are
   unchanged. An analysis is served on its own endpoint and is never embedded in a
   card payload.

### 7. Surfaces

- `POST /v1/analysis {question, metrics[]}` → `Analysis` JSON (authenticated,
  same auth as `/v1/decisions`). There is no list endpoint, and analyses are
  **not persisted**, like cards. Persisting them would make them memory-like,
  which is F/G's domain and CONTEXT.md principle 6's constraint.
- `water analyze "<question>" [--metric id ...]` and `water facts get
  <metric> [--period current]` / `water facts catalog`, all thin clients
  over the daemon.
- H's `analysis` and `decision_support` skills gain `facts.get` and
  `facts.catalog` in their tool lists. This is a data change to H's skill files,
  re-validated at startup by H's own loader.

## Do not build yet

- Any change to the strict lane's rules (quarantine, `Validate`, phraser checks,
  readiness).
- Persisting analyses, or writing anything from an analysis into memory (F/G).
- Background/P2 analysis or threshold watching (P).
- A historical/period store for finance figures (a new migration). See Risks 1.
  I does not add a migration.
- An "email/share this analysis" action. See the open point below.
- The separate-context judgment critic (L). I's analysis is advisory and
  labeled, and it is not quality-gated in this slice.

## Task list

Each task is one commit with its own tests, in this order.

1. Record finding 4 (`cashPosition` zero-for-missing) in `docs/known-gaps.md`.
   Commit the `company_finance` precondition if the owner hasn't already.
2. `internal/facts`: `Fact`/`Result`, the metric registry, and the three `cash.*`
   metrics over `finance_figures`, with staleness and the `no_data` hint.
3. `facts` connector (`get`, `catalog`) and the registration in the daemon's connector
   list and the `twins/ceo/twin.yaml` grant at level R. `TestEmbeddedCEOManifestBuildsAGate`
   still passes.
4. The tokenizer extraction (or copy) and the `internal/analysis` type with `Render`/`Speak`
   and `ModelDerived`.
5. `analysis.Composer` (a cold call, `Charge`, `backend.Fake` tests).
6. The separation guards (§6, tests 1–3).
7. `POST /v1/analysis`, `water analyze`, and `water facts get|catalog`.
8. Extend H's two skill files with `facts.*`, update `docs/EVOLUTION_PLAN.md`,
   and stop.

## Acceptance criteria

1. **The strict lane is unchanged.** Every test in `internal/decisions` passes
   with zero modifications to its test files. `git diff` over
   `card.go build.go compute.go registry.go classify.go twins/ceo/decisions/`
   is empty. `phrase.go`'s diff is either empty or a pure tokenizer move.
2. **Every fact is sourced.** A property test over the catalog asserts that every
   `Status: "ok"` result has a non-empty `Source` in `<Source>:<SourceID>`
   form, a non-zero `AsOf`, and a `Unit`. A metric function that returns an
   `ok` fact without a source is rejected by the ledger. It is converted to
   `not_available`, not passed through.
3. **No guessing.** An unknown metric, an unsupported period, or an empty
   `finance_figures` table each yield `not_available`/`no_data` with a
   code-written `Reason`, never a value and never a panic.
4. **No laundering of missing cells.** A `finance_figures` row with
   `cash_usd: 0` produces a fact flagged `zero_may_mean_missing`. A row older
   than the staleness threshold is flagged `stale`.
5. **Taint is preserved.** Calling `facts.get` through `handleToolInvoke` on
   an untainted session leaves the session tainted, exactly as calling
   `company_finance.cash_position` does. A subsequent S-level call answers
   `queued`. This is tested through the real `*gate.Gate`.
6. **Separation holds structurally.** Guard tests §6.1–§6.3 pass. A
   deliberately broken fixture (an `Analysis` with a `StagedActions` field,
   or a gateway func calling `Approvals.Propose` with an `analysis` value) makes
   each guard fail. Each guard is verified to fail before the fix and pass after.
7. **Strict-lane path via YAML works.** A test-only decision type whose need
   is `facts.get` builds a card whose evidence cites the ledger's `Source`. This
   proves the only route from ledger to card is the sourced connector path.
8. **Interpretation is labeled.** `Analysis.Render()` and `Speak()` always begin
   with `Label` / its spoken form. A `backend.Fake` interpretation containing
   one ledger figure and one invented figure yields `ModelDerived` containing
   exactly the invented one, and `Render` marks it.
9. **One cold call.** `Compose` makes exactly one `backend.Run` (counted on
   `backend.Fake`), calls `Charge` first, and returns the charge error without
   calling the backend when the usage cap is hit.
10. **No outward action.** No code path added in I can reach
    `approvals.Propose` or any level D/A/S function. The `facts` connector
    declares only R functions, which is checked against `Functions()` directly.
11. Gates: `go vet ./...`, `go test -count=1 ./...` and `CGO_ENABLED=0 go build ./cmd/water` are
    green. No new dependency. No metered backend.

## Invariant checks (WORKFLOW.md, restated for this slice)

- No connector function runs without a gate permit. `facts.*` go through `gate.Invoke`.
- Untrusted content stays tagged. `External` is carried from finance to fact to
  analysis `Untrusted`, and nothing derived from it runs autonomously.
- No metered dependency. The binary still builds statically.
- The audit log still verifies. Every `facts.get` call is audited like any R read.

## Risks and open design points

Items marked **owner** are genuinely open. They are not resolved here. The
others carry a proposed default for the owner to accept or change.

1. **Metric catalog beyond `cash.*` and periods (owner).** Which columns of
   `Budget` / `Spend by Application` / `Revenue` mean what, and whether
   `revenue_by_application`'s month columns become `period: "YYYY-MM"`,
   need the owner to confirm the sheet's layout. `gsheets` deliberately
   avoids trusting header positions. Any historical period ("last quarter's
   burn") needs figures to be kept over time, which means a new migration
   (a history table, or dropping the upsert-overwrite). That is a real schema
   decision, not assumed here. Proposed default: ship `current` only.
2. **Staleness threshold.** Proposed default: 24h, set by config (`facts.stale_after`).
   This is provisional, like every threshold in this redesign, until it has
   been checked against real use.
3. **Can the CEO share an analysis outward (owner)?** Brief §3 says advisory-only,
   "never auto-staging an approval". It does not say whether a CEO-initiated
   "email this analysis to the board" is allowed as an explicit, envelope-approved action.
   Guard §6.3 currently forbids it, which is the conservative reading. Relaxing it
   is an owner decision that also touches the redesign's global "sensitivity
   tiers" open question (which outputs always need CEO review).
4. **Chat-path labeling.** In the warm chat session the model can call
   `facts.get` and reason in its reply, and that reply is not an `Analysis`
   value. Proposed default: rely on H's `analysis` skill description
   to instruct labeling in chat, and keep the structural guarantee on the typed
   `Analysis` surface only. The alternative, per-turn tool scoping or a
   post-processor on chat replies, costs a warm-session restart (A2) or adds a
   rewrite step. It is H's mechanism question, not I's.
5. **Relationship to L's composer and critic.** An `Analysis` is not
   quality-gated in I. Whether L's separate-context critic should also review
   analyses depends on the redesign's open **critic-architecture** question
   (same model with fresh context vs a different model vs a rubric judge). I does not resolve it.
6. **Confidence on interpretations.** No calibrated confidence is attached to an
   interpretation. The redesign's global open question on confidence thresholds
   applies. I deliberately adds no confidence field rather than inventing one.
