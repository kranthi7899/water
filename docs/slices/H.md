# Slice H: skills as named responsibilities with gate-checked tool access

**BLOCKED on Slice G. Do not implement until G is verified.** This is one
sub-slice of the intelligence-layer redesign. The sequence, the shared
open-questions list and the reasoning behind the order live in the
redesign's overview planning doc, which plays the role
`docs/slice-c-planning.md` plays for C. This document is the spec to build
H against once G ships. It authorizes no code yet. The build follows
`docs/WORKFLOW.md`'s Explore → Plan → Approve → Implement loop, like every
slice since V.

Covers brief §2. It is mostly organizational and low risk: no new store
type, no migration, no new connector, no new gate level and no new
dependency.

Builds on:
- **A1–A4:** `twins.Manifest` (`Function`, `FunctionIDs`) and the gate's
  R/D/A/S/B levels, taint and rate caps.
- **C-base:** the "plain file the twin loads, validated at startup, fails
  loudly" pattern of `decisions.LoadRegistry` (`internal/decisions/registry.go`).
  `ParseType` decodes with `yaml.Decoder.KnownFields(true)`, so an unknown
  key is an error. `Type.Validate(m)` rejects a `needs[].fetch` or staged
  action that names a connector function missing from the manifest.
  Duplicate ids across files are an error.
- **R:** the main path's live tool loop. `Daemon.twinFunctions()`
  (`internal/gateway/daemon.go`) renders every manifest function except
  level B as an MCP tool. `TwinToolPolicy()` adds the nervous system's
  `quick.*` functions (`Nervous.QuickFunctions()`, sourced from
  `reflex.Specs()`'s `QuickTool` names). `handleToolInvoke` runs every
  call through `gate.Invoke` at P0 with session-sticky taint. R's live
  verification showed the model choosing `quick.*` tools on its own to
  compose an answer, with nothing hardcoding which functions applied. H
  keeps that loop exactly as it is.
- **G:** `memory.search`, `memory.propose` and `memory.invalidate` exist as
  granted manifest functions, and a core-memory block is already part of
  `runtime.RoleSystem`. H's memory-management skill names G's functions,
  and H's prompt block sits next to G's in the system prompt.

## Principle

A skill is **a name, a description of the responsibility, and the list of
functions that responsibility may use.** That is all it is.

A skill is **not** a context bundle. It does not pre-enumerate data
sources, `.md` files, prompt fragments or fetch recipes that it is locked
to. That would rediscover the rigid-workflow mistake one layer up. The
model decides live what to fetch and whether it has enough, as the main
path already does with `quick.*` tools. H trusts that loop for "what do I
need".

H does **not** trust the model's same-context judgment for "is what I
produced good enough". That is a separate gate, built as a separate-context
critic in Slice L. A skill carries no acceptance criteria, no "done"
check and no self-grading step.

A skill never widens access. Every function a skill names is already
callable through `twinFunctions()` or `quick.*`, and every call still goes
through `gate.Invoke` at the level the manifest grants. A skill that lists
`gmail.send_message` does not make it any less level A. The call still
queues an approval envelope, and P2/auto still cannot reach it.

## Findings (from reading the current code)

1. **No skills concept exists anywhere.** Nothing in the code has one, and
   neither does `twins/`. `twins/ceo/` holds `decisions/`, `intents/`,
   `role.md`, `seed/`, `style.yaml` and `twin.yaml`. `twins/ceo-demo/` and
   `twins/counterparty/` also exist and must keep loading without a skills
   directory.
2. **`twins/` is `go:embed`'d** (`embed.go`: `//go:embed all:twins`). Skill
   files therefore change only with a rebuild, never at runtime. That
   matters for the warm session (finding 4).
3. **Startup validation already has two call sites.** `buildTwinDepsFS`
   (daemon) and `loadTwinManifest` (`water status` / `water doctor`) in
   `internal/cli/twin.go` both call `decisions.LoadRegistry` and
   `intents.LoadRegistry` after `twins.Load`. A skills loader belongs in
   both, so a bad skill file fails `water doctor` as well as the daemon.
4. **The warm session restarts on any change to the system prompt or the
   tool set.** `WarmSession.turnLocked` and `Prewarm`
   (`internal/backend/warmsession.go`) restart when `w.system !=
   req.System`, on a model change, when `w.toolsKey != toolsKey(req.Tools)`,
   or after `maxTurns` (40). Two consequences follow:
   - A skills block that is part of `RoleSystem` and built only from
     embedded files never changes between turns. It fits `RoleSystem`'s
     documented contract ("nothing that changes between turns") and costs
     no restarts.
   - Scoping tools per turn (a narrower `tools.Policy` per skill) would
     change `toolsKey` whenever the active skill changed and force a cold
     restart. That latency cost is real and is why the mechanism is still
     open (§6).
5. **The prewarmer must build the same system prompt as a real turn.**
   `daemonPrewarmer.Prewarm` (`internal/cli/cmd_daemon.go`) builds
   `runtime.RoleSystem(runtime.Env{RoleMD: p.roleMD})` on its own. Its doc
   comment explains why it has to match: a mismatch "just gets restarted
   anyway". Anything H adds to `RoleSystem` has to reach the prewarmer too,
   or every first turn after a prewarm pays a cold start.
6. **Quick tools are not manifest functions.** They come from
   `reflex.Specs()`, `quick` is a reserved connector name
   (`intents.reservedConnectorNames`) and `handleToolInvoke` refuses them.
   Validating a skill's function list therefore needs the manifest **and**
   the quick-tool set. The shape is the same as `intents.LoadRegistry`'s
   `Functions` parameter, which already takes reflex specs from the CLI
   layer (`intentFunctions()`), so the skills package never imports the
   nervous system.
7. **Decision-support has no model-callable decisions function.** Decision
   cards come from triage and the builder (`internal/decisions`), not from
   a manifest function, and the brief keeps that strict lane untouched
   (Slice I). Today the decision-support skill can only name the read
   functions a packet draws on. `facts.get` joins it in I.

## Scope

### 1. Skill files

One file per skill in `twins/ceo/skills/*.yaml`. It uses the same "header,
optional `---`, free-text notes" layout as `twins/ceo/decisions/*.yaml`
(reuse `splitNotes`'s behaviour, and move it to a shared helper only if
that is a clean, behaviour-preserving move).

```yaml
id: scheduling                    # stable id, lowercase letters/digits/_
title: Scheduling
responsibility: >-
  Keep the CEO's calendar coherent: find time, propose and move meetings,
  protect focus blocks, and flag conflicts before they happen. Creating or
  moving an event is always staged for the CEO's approval.
functions:                        # what this responsibility may use; each
  - gcal.list_events              # must be a non-B manifest function or a
  - gcal.create_event             # quick.* tool, or loading fails
  - gcal.move_event
  - quick.calendar
  - quick.free_time
  - quick.next_event
---
Notes for whoever maintains this file: edge cases, why a function is or
isn't here. Never shown to the model.
```

The struct is exactly `id`, `title`, `responsibility` and `functions`,
decoded with `KnownFields(true)`. **No other key is accepted.** A
`sources:`, `context:`, `files:`, `prompt:`, `fetch:` or `criteria:` key
fails the load. That makes "no fixed context bundle" a structural property
of the schema, not a convention a later edit could erode. The notes body
stays out of the prompt for the same reason.

### 2. Loader and validation: `internal/skills`

```go
// internal/skills/registry.go
type Skill struct {
    ID             string   `yaml:"id"`
    Title          string   `yaml:"title"`
    Responsibility string   `yaml:"responsibility"`
    Functions      []string `yaml:"functions"`
    Notes          string   `yaml:"-"`
    File           string   `yaml:"-"`
}

// Quick is the set of quick.* tool names the nervous system serves,
// supplied by the caller (internal/cli, from reflex.Specs()) so this
// package never imports internal/nervous.
func LoadRegistry(fsys fs.FS, m *twins.Manifest, quick map[string]bool) (*Registry, error)

func (r *Registry) All() []Skill            // file order, sorted by path
func (r *Registry) Get(id string) (Skill, bool)
func (r *Registry) Block() string           // the prompt text, §3
```

It is a new package, not part of `internal/decisions` or
`internal/nervous/intents`. Skills are a twin-scoped static resource like
both of those, but they belong to neither.

**Fails loudly**, the same posture as `twins.Load` and
`decisions.LoadRegistry`. Loading stops, and so does the daemon and
`water doctor`, when any of these holds:
- a file is malformed YAML or has an unknown key;
- `id` is missing or badly formed, or two files share an `id`;
- `title` or `responsibility` is empty or whitespace;
- a `functions` entry is not a `connector.function` id;
- a function is not in the manifest and not in `quick`;
- a manifest function is level B (never callable, so listing it is a
  bug);
- a function appears twice within one skill.

A function may appear in several skills. Reading mail serves research,
briefing and drafting alike.

**A missing `skills/` directory is an empty registry, not an error.** This
matches `intents.LoadRegistry`'s handling of a missing `intents/`, so
`ceo-demo` and `counterparty` keep loading unchanged.

**An empty `functions` list is valid.** It is how a skill whose tools a
later slice builds ships honestly today (§4). The prompt block marks such
a skill "not yet available", so the model says so instead of improvising
a monitor or a delegated task it cannot run.

Wiring: `buildTwinDepsFS` and `loadTwinManifest` call `skills.LoadRegistry`
right after the decisions and intents registries, with the quick set built
from `reflex.Specs()`. `twinDeps` gains a `skills` field.

### 3. What the model sees

`Registry.Block()` renders one short entry per skill, in code, as a
`## Skills` section: title, responsibility, and the tool names it may use
(or "not yet available"). It ends with one fixed sentence saying the list
describes responsibilities, not a script: the model fetches what the
specific request needs, and every call is still checked by the gate.

`runtime.Env` gains a `SkillsBlock string`, the same shape as the existing
`StyleBlock`. `RoleSystem` appends it last, in a fixed order: `role.md`,
G's core-memory block, the style block, then skills. The block is built once at startup
from embedded files, so it never changes between turns and never triggers
a warm-session restart. The one extra `RoleSystem` input also has to reach
`daemonPrewarmer`, so a prewarm and the real turn build a byte-identical
`System` (finding 5).

This is the **baseline** that H builds. How the skills are used beyond
this is the open point in §6, and H implements nothing further until the
owner decides it.

### 4. The nine shipped skills

Each skill ships with its description and **only the functions that exist
in the manifest or the quick set today** (after G). A later slice that adds
a function appends it to the relevant skill file in the same commit that
grants it in `twins/ceo/twin.yaml`. The loader would reject a function
listed before it exists, so the files cannot run ahead of the manifest.

| id | Responsibility (summary) | Functions at H | Extended by |
|---|---|---|---|
| `analysis` | Analysis and modeling: finance, pipeline, project state | `company_finance.*` (6), `hubspot.list_deals`, `github.list_prs`, `github.list_issues`, `linear.list_issues`, `gdrive.search_files`, `gdrive.read_file` | I (`facts.get`) |
| `decision_support` | Prepare decisions and surface what's pending; never make one | `quick.pending_approvals`, `company_finance.*`, `gmail.list_messages`, `gmail.get_message`, `gdrive.search_files`, `gdrive.read_file` | I (`facts.get`) |
| `briefing` | Reporting and briefing: brief, status, what needs attention | `quick.cached_brief`, `quick.calendar`, `quick.latest_mail`, `quick.pending_approvals`, `gcal.list_events`, `gmail.list_messages`, `linear.list_issues`, `github.list_prs` | I, L (composer, when it becomes callable) |
| `memory` | Memory management: recall, propose, correct | `memory.search`, `memory.propose`, `memory.invalidate` (G) | none planned |
| `messaging` | Message drafting and sending; sending is always an approved envelope | `gmail.list_messages`, `gmail.get_message`, `gmail.draft_message`, `gmail.draft_for_review`, `gmail.send_message`, `quick.latest_mail`, `quick.mail_from`, `twinlink.send_message`, `twininbox.list_messages` | none planned |
| `scheduling` | Scheduling (example in §1) | `gcal.list_events`, `gcal.create_event`, `gcal.move_event`, `quick.calendar`, `quick.free_time`, `quick.next_event` | none planned |
| `delegation` | Delegated task execution: multi-step and waiting work | none (not yet available) | O (`tasks.plan`) |
| `monitoring` | Watch a defined threshold and react | none (not yet available) | P (monitor archetype) |
| `research` | Research across mail, docs, code and CRM | `gdrive.search_files`, `gdrive.read_file`, `gmail.list_messages`, `gmail.get_message`, `github.list_prs`, `github.list_issues`, `linear.list_issues`, `hubspot.list_deals`, `hubspot.list_contacts` | L (composer) |

The per-skill function lists are a proposal for the owner to edit at the
Plan stage. They are not a decision. The **ids and the set of nine are
decided** by the brief. `agentmail.list_messages` is deliberately in no
skill: it serves `internal/agentmail`'s background triage, not the model's
responsibilities. Descriptions state responsibilities in the voice of
`role.md` ("Responsibilities of the role"), with no persona and no
judgment style, which `role.md` explicitly does not model.

### 5. `water skills`

This command is read-only. `water skills` lists id, title and function
count, and `water skills show <id>` prints the responsibility, the
functions with their manifest level (or `quick`), and the notes. It uses
`loadTwinManifest`'s no-store, no-audit path, so it runs while the daemon
is up.

## Do not build yet

- **Per-turn tool scoping** (a narrower `tools.Policy` per active skill),
  and any skill-selection or classifier step. Both are open (§6).
- **Any quality or "is this good enough" check** inside a skill. That is
  L's separate-context critic.
- **Context bundles** of any kind, which are forbidden by §1's schema, not
  just deferred.
- **Skill-to-intent coupling.** Skills affect only the main (model) path.
  Tier 0 routing, `twins/ceo/intents/` and the promotion loop are
  untouched.
- **Learned or promoted skills.** Q promotes procedures, not skills.

## Tests

- **Loader validation.** Each failure case in §2 fails `LoadRegistry` with
  an error that names the file and field: malformed YAML, unknown key,
  missing or bad id, duplicate id across files, empty title or
  responsibility, a malformed function id, a function absent from both the
  manifest and the quick set, a level-B function, and a duplicate function
  within one skill.
- **No context bundle, structurally.** A fixture skill with a `sources:`,
  `context:`, `files:` or `prompt:` key fails to load. A guard test
  reflects over `Skill`'s YAML-tagged fields and fails if any field beyond
  `id`, `title`, `responsibility` and `functions` is ever added, so
  widening the schema is a visible, reviewed change.
- **Shipped files.** The embedded `twins/ceo/skills/` loads against the
  real `twins/ceo/twin.yaml` and `reflex.Specs()`, contains exactly the
  nine ids in §4, and `delegation`/`monitoring` render as "not yet
  available".
- **Optional directory.** `ceo-demo` and `counterparty` load with an empty
  skills registry, and `buildTwinDepsFS` succeeds for both.
- **Fail-loudly wiring.** `buildTwinDepsFS` with a fixture tree containing
  a bad skill file returns an error before the store or audit log opens.
  This mirrors the existing malformed-`decisions/*.yaml` test.
- **Never widens access.** For every function in every shipped skill:
  - if it is a manifest function, it is in `twinFunctions()`'s output;
  - if it is a quick tool, it is in `QuickFunctions()`.
  The `TwinToolPolicy` a turn uses is byte-identical with and without the
  skills registry loaded (same `toolsKey`). An A-level function named in a
  skill (`gmail.send_message`) still answers `queued` through
  `handleToolInvoke`, never `ok`.
- **No restart churn.** `RoleSystem` with the skills block is identical
  across two consecutive turns. The prewarm request's `System` equals a
  real main-path turn's `System`. Tested with a warm session over
  `backend.Fake`, asserting the process is reused (`"alive"`), not
  restarted.
- **Block is code-rendered.** `Block()` output is deterministic (golden
  test), contains every skill's title and responsibility, and never
  contains a notes body.
- `go vet ./...`, `go test -count=1 ./...` and
  `CGO_ENABLED=0 go build ./cmd/water` pass, as always. All model calls in
  tests use `backend.Fake`.

## Acceptance criteria

1. The daemon and `water doctor` both refuse to start on any malformed
   skill file, with an error naming the file. They start unchanged when
   `skills/` is absent.
2. The nine skills load from embedded files. Every function they name is
   callable today through the existing MCP path, and none is level B.
3. Adding a skill or a function to a skill changes neither the tool set the
   model receives nor any gate outcome. This is shown by the policy
   equality and `queued` tests above.
4. The warm session is not restarted by the skills block. The prewarm and
   the real turn share a `System` string, and a two-turn warm test reuses
   one process.
5. The skill schema cannot express a context bundle. That is shown by the
   unknown-key failure and the struct-field guard test.
6. `water skills` and `water skills show` work against a running daemon
   without touching the audit-log lock.

## Invariant checks

These are `docs/WORKFLOW.md`'s list, restated for what H could plausibly
touch:
- No connector function runs without a gate permit, and an A-level call is
  still refused without an approved envelope. Skills are prompt text and a
  validated list. Neither is on the invoke path.
- Untrusted content handling and taint are unchanged. The session token
  and `escalateTaint` are not touched.
- Reflex tiers are unchanged. `quick.*` is read by name only.
- No new dependency (YAML parsing reuses the decoder `decisions` already
  uses), no metered call, and the static build still works.
- No secret appears in the skills block (it is built only from embedded
  files).

## Task list (for the Plan stage to confirm)

1. `internal/skills`: `Skill`, `ParseSkill`, `LoadRegistry`, with every
   validation and guard test.
2. The nine `twins/ceo/skills/*.yaml` files, plus the shipped-files test.
3. Wiring: `twinDeps.skills`, calls in `buildTwinDepsFS` and
   `loadTwinManifest`, and the fail-loudly and optional-directory tests.
4. `Registry.Block()`, `runtime.Env.SkillsBlock` in `RoleSystem`, the
   prewarmer passing the same block, and the no-restart and never-widens
   tests.
5. `water skills` / `water skills show`.
6. Docs: `docs/architecture.md` (the skills registry next to decisions and
   intents), `docs/known-gaps.md` (per-turn scoping deferred, pending §6),
   and the `docs/EVOLUTION_PLAN.md` log entry.

One task per commit, with the gates before each.

## Dependencies

- **Hard: G.** The `memory` skill names `memory.search`,
  `memory.propose` and `memory.invalidate`, and the loader rejects
  functions that don't exist yet. `RoleSystem`'s block order (`role.md`,
  then G's core memory, then style, then skills, following G §3's
  placement of the core block before `## Style`) and the prewarmer's
  matching `System` both
  build on G's change to the same function. H can't be tested for "no
  restart churn" until G's core-memory block has settled how it
  interacts with restarts.
- **Transitively: F**, through G.
- **Not dependent on** J, K, L, N, O, P or Q. Those depend on H or extend
  it:
  - I adds `facts.get` to `analysis` and `decision_support`.
  - O adds `tasks.plan` to `delegation`. J and K expose no model-callable
    task functions (J's and K's "do not build" lists).
  - P makes the monitor archetype selectable. P's spec does not yet name
    which functions `monitoring` lists, so that is settled at P's Plan
    stage.
  - P gives `monitoring` its functions.
  - L's composer may join `briefing` and `research` if it becomes a
    model-callable function.
  - O's planner reads skills only as descriptions. It scopes each task's
    context itself (brief §4), never from a skill's default list.

  Each of those slices owns its own skill-file edit. H does not
  pre-reserve their function names, and no `plannedActions`-style
  allowance is added: unlike a decision card's staged action, a skill has
  no reason to name a function before it exists.

## Open points for the owner (H-specific, not resolved here)

- **How skills are used per turn.** There are two options:
  - (a) **Description only.** The block in the system prompt (§3) is the
    whole mechanism. The model keeps the full tool set, and a skill's
    function list documents and validates but does not restrict. There
    are no restarts.
  - (b) **Per-turn tool scoping.** Some step picks the active skill(s) and
    narrows `tools.Policy` to their functions. This really does restrict
    access, but a changed `toolsKey` restarts the warm session, which
    costs latency on each skill switch. It also needs a selection step
    that decides which skill a turn is, and that step is itself a
    judgment the brief does not assign.

  H builds (a) because it is the part both options share. It does not
  treat (a) as the final answer. Whether (b) is ever wanted, and what
  would select the skill, is the owner's call.
- **The exact per-skill function lists** in §4, which are proposed and
  editable at the Plan stage.
- **Whether every model-callable function must belong to at least one
  skill** (a coverage test that fails when a new grant has no home). This
  is useful as a forcing function, but it is a policy choice, so it is
  listed here rather than built by default.
- From the redesign's global open questions: **sensitivity tiers**, which
  decide which outputs always need CEO review before sending, may later
  attach to skills (e.g. `messaging`). H adds no such field. When the
  owner decides, that is a schema change to review, per the struct-field
  guard test.
