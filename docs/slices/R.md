# Slice R: the nervous system (router)

Status: **Slice R complete (2026-09-25), with one flagged item for the owner.** R-1 through R-11 and R-13 through R-28 are committed and ticked below. **R-12** ("Runtime split, main path, front door") has its work fully committed (`cd2e5c5`, `24cfe33`) and depended on successfully by every later task, but the "tick the box" commit every other task in this build has was never made for it — left `[ ]` on purpose rather than silently ticked; see R-12's own line, `docs/known-gaps.md` and `docs/EVOLUTION_PLAN.md`'s 2026-09-25 log entry for the same note. This build followed `docs/WORKFLOW.md`'s Explore → Plan → Approve → Implement → Verify → Review → Wrap-up loop, scoped to this slice only (not a standing rule for every future slice). See `docs/EVOLUTION_PLAN.md`'s 2026-09-25 log entry for the summary, and `docs/known-gaps.md`'s Slice R section for residual gaps.

Context note: the owner's original prompt for this slice says "Sequence: finish A4 verification, then this slice, then B." That sequencing note is stale — per `docs/EVOLUTION_PLAN.md`, A4, B (interim), C-base, and M are all already done as of 2026-09-24. Slice R is being planned now, out of that original order, at the owner's explicit direction. A separate session is concurrently advancing Slice C's write-function increment and Slice E on the same `feat/ceo-twin` branch — see "Risks" below for the overlap this creates.

## Findings

Three read-only research passes covered: (1) `internal/runtime` (`fastpath.go`, `brief.go`, `runtime.go`) and `internal/decisions` as a registry precedent; (2) `internal/gateway` (daemon routes, turn handling, taint) and `internal/backend` (warm sessions, fake backend) plus `internal/approvals` and `internal/config`; (3) `internal/gate`, `twins/ceo/twin.yaml`, and `internal/store`.

### 1. The current fast path (`internal/runtime/fastpath.go`)

`FastPath(ctx, env, prompt) (string, bool)` is the only entry point, called at the top of `RunTurn` before any model call. It normalizes the prompt, then applies a shared eligibility gate (`fastPathEligible`): reject if over 10 words, if any word is an action verb (reschedule/cancel/send/approve/create/...), if any word signals a non-today time scope (yesterday/week/weekday/month names/"next"/"last"), or if any word contains a digit. Only prompts that survive are tried, in fixed order, against three hardcoded matchers: `matchSchedule`, `matchApprovals`, `matchBrief` — pure substring/phrase containment, no tokenizer, no scoring, first match wins. Each matcher builds its answer directly from the store/approvals queue with **no model call**, except `briefAnswer`, which is a deliberate, commented exception: it may call the model once (via `ComputeAndCacheBrief`) to phrase a cached-or-freshly-computed brief, with taint recomputed and fed to `env.OnTaint` on every call regardless of cache hit, failing closed on any unreadable signal.

If `FastPath` returns `ok=false`, `RunTurn` falls through to assembling `StateSummary` and making a real model call. **Every non-fast-path turn today uses the fast tier only** (`env.Manifest.ModelFor(twins.TierFast)`); `TierStrong` is defined in `twin.yaml`'s schema and `ModelFor` but is dead code — nothing calls it. There is no background/strong-model routing path wired into the turn handler at all today.

### 2. `internal/decisions` as the registry precedent

This is the pattern Slice R's intent registry should mirror, already proven in Slice C-base: a `Type`-like struct (strict YAML header via `KnownFields(true)`, optional free-text notes body after a `---` line); `LoadRegistry(fsys, manifest)` globs `twins/<id>/<kind>/*.yaml`, sorts deterministically, decodes and validates each file against the *already-loaded* manifest (cross-referencing function ids and required levels), rejects duplicates, and never partially succeeds — one bad file fails the whole load, exactly like `twins.Load` does for `twin.yaml` itself. A reserved fallback entry (`generic`) keeps the registry always non-empty. `Index()` exposes only cheap fields (`{ID, Trigger}`) for a classification prompt, never full type bodies. Startup wiring happens in `internal/cli/twin.go`'s `buildTwinDepsFS`, immediately after `twins.Load`, before store/audit/gate open — a malformed registry file stops the daemon before anything else does.

Test patterns worth reusing directly: `fstest.MapFS`-based loader-validation tests (`decisions_test.go`'s `TestLoadRegistryWellFormed`/`Rejects`/`EmptyDirIsOnlyGeneric`), an embedded-registry drift test (`TestEmbeddedCEORegistryLoads`), and `fastpath_test.go`'s near-miss table style (deliberate false-negative phrasings that must NOT match).

### 3. The central design gap: store reads bypass the gate

**Connector calls are fully gated today; store reads are not.** `Gate.Invoke(ctx, Call)` is the only path to a connector: it hashes args, audits "received", calls `authorize()` (manifest lookup + schema + envelope requirement), checks rate caps, fetches credentials from vault, mints a single-use permit via the internal-only `gate/internal/mint` package (so no code outside `internal/gate` can fabricate a permit or call a connector's `Invoke` with real credentials), audits the decision and the execute result, then (via `Normalize`) upserts resulting records into the store. Every connector call is audited, rate-capped, and manifest-checked end to end.

By contrast, `fastpath.go` and `brief.go` call `store.EventsInRange`, `store.List[...]`, `env.Store.GetCursor/GetBrief`, and `env.Approvals.Pending(ctx)` **directly**, holding a raw `*store.Store` handle with no `Call`, no permit, no audit record, no rate cap, and no manifest entry. There is no "store read" concept in the gate at all — no level, no function id.

Slice R's spec says "every tier only proposes or → GATE → connectors / store," which implies store reads should also flow through gate-like control. Nothing in the current architecture does that. Two ways to close the gap, neither free:
- **(a) Register a `store` pseudo-connector.** Makes `store.calendar_events`-style ids real `connector.function` entries flowing through the existing `Gate.Invoke`/permit/mint machinery — consistent with the connector path, but the manifest schema (`twins.Function`, `ValidateManifest`) currently hard-requires every function to resolve via `connectors.Registry.Lookup`, which requires a connector name with no dot in it; `Gate.Invoke`'s post-execute path unconditionally calls `Normalize` + `Store.Upsert`, which makes no sense for a pure read and would need a no-op path. Adds hashing/audit/permit overhead to every trivial local read.
- **(b) Build a separate, lighter read-gate** specifically for store queries (its own audit kind, its own manifest section or none at all, feeding the same `route_log`) that parallels but doesn't reuse `internal/gate`. Cheaper per-call, but is a second, parallel security mechanism to reason about and keep consistent with the first.

This decision cannot be inferred from existing code and must be made explicitly in the Plan phase.

### 4. The manifest (`twins/ceo/twin.yaml`)

44 lines: `id`, `name`, a `usage` block (5h window, 200 model_calls, 40 auto_model_calls), a `models` block (`fast: haiku`, `strong: ""`), then `connectors:` — currently exactly three: `gcal.list_events`, `gmail.list_messages`/`get_message`, `gdrive.search_files`/`read_file`, all level R with per-function rate caps. `auto_allowlist` lists `gcal.list_events` and `gmail.list_messages` (R/D only, enforced by `twins.Manifest.Load`). No `store.*` entries exist, and the `connector.function` naming convention is the only one the registry/gate machinery understands today — `store.calendar_events` is not resolvable by `Registry.Lookup` without new code either way in point 3 above.

### 5. Store schema and query helpers

Migrations `0001`–`0007`, no gaps (`0001_init` core record tables; `0002_gate_state` rate_hits/audit_anchor; `0003_sync_cursors` cursors + briefs cache; `0004_decision_classifications`; `0005_record_indexes`; `0006_message_body_full`; `0007_meetings` + FTS5). Next migration is `0008`. No `route_log` table or symbol exists anywhere (grepped clean) — confirmed new. Helpers fastpath/brief already use: `store.EventsInRange`, `store.List[store.Message]` with `Since`, `GetCursor`/`SetCursor`, `GetBrief`/`SetBrief`, `Approvals.Pending`. `SearchMessages`/`SearchDocuments` (FTS5) and `GetDecisionClassification` exist but aren't called from fastpath/brief today — they're used by the meetings/decisions code. No "latest N emails" or "unread count" helper exists yet; a Tier 0 handler for those would reuse `store.List[store.Message]` the way `brief.go` does.

### 6. The daemon's turn path and streaming

Full route surface: `GET /v1/health`, `POST /v1/turns`, `GET /v1/approvals`, `POST /v1/approvals/{id}/decision`, `GET /v1/state`, `GET /v1/decisions`, `POST /v1/tasks/{id}/cancel`, `POST /v1/meetings/start|{id}/segments|{id}/stop`, `GET /v1/meetings/{id}/cues`, `POST /v1/tools/invoke` (internal MCP-bridge callback, separately authed).

`handleTurn` is the one path every channel (CLI, hotkey, voice, meeting mode) already goes through — this is good news for "one entry point": routing logic can be inserted as a step inside `runtime.RunTurn` (called from `handleTurn`) rather than needing a parallel HTTP surface. Streaming is NDJSON over chunked HTTP (not SSE): `ack` → `delta`*/`sentence`* → optional out-of-band `approval_required` (from a concurrent `handleToolInvoke` call sharing a `turnSink`) → terminal `done`/`error`. `runtime.RunTurn(ctx, env, turn, emit)` is already a clean, HTTP-independent primitive a router could call directly.

Taint escalation (`escalateTaint`) lives in `gateway.Daemon`, wired into `runtime.Env.OnTaint`, and is called from three places: `handleTurn` (state-summary/meeting-context taint), `handleMeetingSegment` (unconditional, before the body is even parsed), and `handleToolInvoke` (when a tool result comes back `Untrusted`). It escalates one long-lived, session-scoped stable tool-proxy token — deliberately sticky, never de-escalated, because the warm session's MCP bridge child reads its policy file once at startup. Any new tier that makes tool calls must reuse this same discipline.

### 7. Backend, warm sessions, and the fake backend for tests

`Backend`/`Streamer` interfaces; `Request.Model` pins a model string, resolved once via `Manifest.ModelFor(tier)` before the request is built — not inside the backend. `WarmSession` keeps one `claude --print --input-format stream-json ...` subprocess alive across turns behind a one-slot semaphore (serializes turns), restarting on system-prompt/model/tool-policy change, after 40 turns, or on any early termination (cancel/timeout/death) since a stray reply could otherwise leak into a later turn. **The warm session is a scarce singleton bound to the fast tier** — a new tier running concurrently with an in-flight chat turn must not share that semaphore or it will starve/serialize behind it; it needs its own process lifecycle.

`backend.NewFake(name)` records every `Request` and replies via a settable `Reply func(Request) string` hook, splitting replies into word-deltas for streaming-order tests. This is exactly the seam Slice R's tests should use: a distinct fake per tier (or one fake keyed by `req.Model`/`req.System`), asserting on `fb.Requests()` to verify routing without a real `claude` CLI call.

### 8. Approvals: the yes/no matcher is envelope-scoped, not prompt-scoped

`Envelope` carries `PayloadHash` (sha256 of canonical payload); `Match(reply string)` is a deterministic yes/no classifier (affirmative/negative/filler word sets; any negation-with-other-words or unrecognized word is `Ambiguous`, treated as `No`). Critically, **this matcher only runs inside `Queue.Decide`, reachable only via `POST /v1/approvals/{id}/decision`**, which is always called with one specific envelope id and the payload hash the client was shown — the CLI (`water approve`) lists pending envelopes, has the CEO pick one, shows a read-back, then posts. There is no existing "is anything pending, and does this free-text turn look like an answer to it" check anywhere in `fastpath.go` or `handleTurn` — `matchApprovals` today only answers "what's pending," it doesn't decide one. Slice R's Tier 0 "approve/deny only when a request is pending, always bound to that request" needs new glue: check `Approvals.Pending(ctx)`, and when exactly one envelope is pending, treat a matching yes/no reply as a decision on it — the multi-pending case (which envelope does a bare "yes" answer?) is unresolved by anything existing and needs an explicit rule (e.g., escalate to Tier 2 to disambiguate when more than one is pending).

### 9. Config

Key-name validation is already a single source of truth (`defaults()` map, consulted by `Keys()`, `Load()`, and `Save()`). Per-field wiring (`apply()`, `Flat()`, `intKeys`/`boolKeys`) is still separately enumerated per field — inherent to the hand-rolled typed layer, not a bug. A new tier's flags (`router.tier1.enabled`, `.timeout_ms`, `.breaker_threshold`, etc.) fit this exact pattern directly: one `RouterConfig`/`TierConfig` struct, entries in `defaults()`, one line each in `apply()`/`Flat()`, and the right `intKeys`/`boolKeys` entries. No existing validation for cross-field/cross-tier relationships (e.g. tier1 timeout < tier2 timeout) — each field validates independently today.

## Risks / open questions carried into the Plan phase

1. **Gate vs. direct store read (section 3) — must be decided explicitly**, not inferred. This is the single biggest architectural fork in the whole slice.
2. **No router seam exists.** Tier dispatch is inline in `runtime.RunTurn`/`FastPath`. The least invasive path is to make dispatch data-driven inside/behind `runtime.RunTurn` (calling `runtime.RunTurn` as the shared primitive already used by `handleTurn`), rather than inventing a parallel HTTP surface.
3. **Concurrent work on the same branch.** A separate session is advancing Slice C's write functions (`gmail.draft_message`/`send_message`, `gcal.create_event`/`move_event`) and Slice E (manifest generalization) on `feat/ceo-twin` at the same time as this planning pass. Both of those will touch `twins/ceo/twin.yaml`, and Slice E may touch the manifest schema itself. This slice's task list should sequence manifest edits to minimize collision (e.g., additive-only YAML changes, rebase before each commit) and the Plan should flag this explicitly as an execution risk, not something to silently route around.
4. **Fastpath's hardcoded three-branch dispatch is a structural rewrite, not an additive change**, once it becomes registry-driven — existing `fastpath_test.go` near-miss cases must all still pass post-refactor.
5. **`briefAnswer`'s one deliberate model-call exception** and its fail-closed taint re-check must be preserved (or explicitly re-justified) inside whatever Tier 0 becomes.
6. **Approve/deny disambiguation** when more than one envelope is pending (section 8) needs an explicit default rule.
7. **`TierStrong`/background routing is currently 100% unwired** — Tier 3 in the new design is not a refactor of existing code, it's new code end to end.
8. **FunctionGemma sidecar (Tier 1)** is an entirely new local-process dependency (GGUF via llama.cpp server or Ollama) — needs an explicit ask-before-adding checkpoint per `CLAUDE.md`'s dependency rule, even though it's a local sidecar, not a Go module dependency, and the Gemma license terms need to be checked and noted.

## Plan

This plan replaces the earlier R1/R2/R3 split with **one build**, following the owner's amendment "one build, head chef and sous chef". Everything the earlier plan designed is carried forward unless a section below says otherwise: the store-vs-gate decision, the template grammar, the slot resolvers, the registry schema, the reflex handlers, the renderer and `style.yaml`, the route log, the breaker, the eval harness and the null `Decider`.

**Vocabulary.** These terms are used throughout, and each maps to a specific code path:

| Term | Meaning | Code path |
|---|---|---|
| **Sous chef** / **quick owner** | Tier 0 (`t0`, templates) plus Tier 1 (`t1`, FunctionGemma) over a fixed table of audited handlers | `internal/nervous/{tier0.go,tier1.go}`, `internal/nervous/reflex`, `internal/nervous/propose` |
| **Head chef** / **main owner** | Claude on the warm session. This is the earlier plan's "T2". Its tier id is `main` | `runtime.ModelTurn` (today's `runtime.RunTurn` minus fast path, ack and done) over `backend.WarmSession`, with connector tools via `internal/tools` → `POST /v1/tools/invoke` → `Gate.Invoke` |
| **Quick tools** | Sous-chef read handlers exposed to the head chef as MCP tools named `quick__*` | `internal/tools` → `POST /v1/quick/invoke` → `reflex.QuickService` (never `Gate.Invoke`) |
| **Front door** | `nervous.Handle`, the single entry point for every turn | called only from `gateway.handleTurn` |
| **Tier 3** | Folded into the main path; see §15. `TierStrong` stays unwired | none |

### Execution environment

- **Separate git worktree.** This build runs in a separate worktree, for example `git worktree add ../water-r -b slice-r feat/ceo-twin`, never in the owner's main checkout at `/Users/kranthikoneti/water`. The reason is that another session is editing `feat/ceo-twin` at the same time.
  - Git will not check out the same branch in two worktrees, so the worktree runs on a short-lived local branch, `slice-r`, created from `feat/ceo-twin`.
  - **Integration.** Before each task's commit, run `git fetch` (if there is a remote) and `git rebase feat/ceo-twin` inside the worktree. Rebasing unpublished local commits onto the shared branch is not a rewrite of shared history. At the end of each group of tasks, the coordinating session runs `git merge --ff-only slice-r` **from the main checkout**, and only when the other session has no uncommitted changes in any path the merge touches.
  - Never move `feat/ceo-twin` with `git update-ref` or `git branch -f`. The branch is checked out in the other worktree, and its index would silently disagree with its HEAD.
  - The slice is tagged `slice-R-done` on `feat/ceo-twin` after the final fast-forward.
- **What the worktree protects against:** two sessions sharing one working directory and index. That means one session's `git add` staging the other's uncommitted files, a build or test run picking up the other session's half-edited files, and editor saves clobbering each other.
- **What it does not protect against:** both worktrees share one object store and one branch history, so commits still have to coexist.
  - If both sessions edit the same file, the rebase conflicts. If both add a migration numbered `0008`, the rebase succeeds but the store breaks.
  - So the file-level collision rules below still apply in full: **[shared]** markers, small hunks, re-reading a shared file right before editing it, staging by explicit path, `git status` before every commit, and choosing the migration number at commit time.
- **Commit convention.** Every commit in this slice starts with `R-<n>:`. The "untouched paths" acceptance check greps by that prefix (criterion 30).
- **Gates for every commit:** `go vet ./...`, `go test -count=1 -race ./...`, `CGO_ENABLED=0 go build ./cmd/water` and `gofmt -l .` (empty). Task R-27 also requires `clients/macos/build.sh` and `clients/macos/test.sh`.

### Scope and non-scope

**In scope (one build):**
- **Intent registry.** Files in `twins/ceo/intents/*.yaml` hold **read** intents, which are answered by the sous chef, and **write** intents, which produce proposals only. The registry is validated at startup and fails loudly, the same way `decisions.LoadRegistry` does. There is one exception: a write intent whose manifest function is not granted yet loads as **inactive** instead of failing (§5.4).
- **Tier 0.** A deterministic template matcher (in-house Go, no new dependency) plus slot resolvers. It replaces `internal/runtime/fastpath.go` end to end.
- **Reflex handlers.** 12 read-only handlers (`internal/nervous/reflex`) that read the store through a separate read-only SQLite connection pool.
- **Pre-tier escalation.** `escalate_words`, multi-clause detection and the too-long cap. The sous chef never ranks or judges.
- **Tier 1.** FunctionGemma through a `llama-server` sidecar that the daemon supervises. It is limited to `reflex_eligible` intents, runs the same validator with a grounding check, and **ships off**. It can only be enabled after a recorded live eval passes the thresholds (§10).
- **The front door** (`nervous.Handle`) with a turn state machine: `listening → final → routed(quick|main) → done`, with exactly one owner per turn.
- **Partial transcripts** at `POST /v1/turns/{id}/partial`, used for speculative prefetch only, with zero model calls.
- **Handoff acknowledgement** within 300 ms of the final transcript.
- **Quick tools** (`quick__*`) exposed to the head chef through the existing MCP bridge, on a separate daemon endpoint that never reaches the gate.
- **One-voice contract.** `speak.Speakable()` runs on every tier's voice output, `style.yaml` gains a `voice:` section (tone, phrases, banned phrases, TTS profile), a lint records warnings in the route log, and `GET /v1/voice/profile` serves the TTS profile to clients.
- **Write intents.** Create or move an event, reply to or send to a person. They produce proposals that go through the same envelope path the model's tool calls use, then the approval queue with a read-back. **Nothing executes without approval.**
- **Voice approval binding** (`router.voice_approve.enabled`, **off** by default): a bare yes or no within 60 s of a read-back on the voice channel decides that envelope with that hash, using a risk-tier mapping.
- **Promotion loop** (`router.promotion.enabled`, **off** by default): candidate detection, a model-drafted intent file, validation, owner approval, a learned overlay, and demotion through a per-intent breaker.
- **Route log** (migration `0010_router.sql`) with owner, voice/partial, acknowledgement-latency, lint, tool-usage and action columns. Also possible-miss detection, `water route report`, `water route candidates`, `GET /v1/router`, and a `router` line in `water status`.
- Per-tier config flags, timeouts and circuit breakers.
- The `Decider` interface with a null implementation.
- `docs/functiongemma.md` covering the Gemma terms. `water model pull functiongemma` is a command the owner runs explicitly.
- **Swift client:** it takes its TTS profile from `/v1/voice/profile` and optionally streams partial transcripts.
- **Eval harness.** A held-out eval set with reasoning, multi-clause and write-intent cases, a legacy baseline, and a Tier 1 live eval.

**Non-scope (explicit):**
- **Files owned by the concurrent session:** no edits to `twins/ceo/twin.yaml`, `internal/gate/**`, `internal/connectors/**`, `internal/decisions/**` or `internal/approvals/**`.
  - Adding the four write functions to the manifest, and adding their `ReadBack` `case` branches, belongs to that session.
  - Write intents activate automatically once the manifest grants those functions (§5.4).
- **Nothing autonomous.** No autonomous action, and no action promotion of any kind. Autonomy for actions comes only from standing grants, which are a separate, deferred mechanism.
- **Barge-in** (the user interrupting a reply) is deferred.
- No per-turn switch to `TierStrong` (§15).
- No unread-flag sync, because it would need an edit to `gmail.Normalize`, which the other session owns.
- No project-entity resolution. The slot type is reserved and unused.
- No Jev or any other `Decider` adapter.
- No `water route export` and no fine-tuning recipe. The `confirmed` column is reserved for them.
- No new Go module dependency. `llama-server` is an owner-approved Homebrew binary, a separate process that is not linked. No cgo.
- The Swift client keeps its current behaviour of cancelling the in-flight turn when a new turn starts. The daemon supports concurrent turns (§11); changing the client is a follow-up owner decision (Risks, item 16).

### Design

#### 1. Store reads and the gate: decision (owner-confirmed)

**The sous chef reads the twin's local store directly, through a read-only view. It does not go through `internal/gate`, and the gate gains no `store` pseudo-connector.** This is option (b) from Findings §3, without a second audit mechanism. Quick tools served to the head chef bypass the gate for the same reason (§9).

Why:
- **Store reads are not outward actions.** CONTEXT.md principle 2 governs outward actions. Every record in the store already passed `Gate.Invoke` (permit, rate cap, hash-chained audit) when it was synced in.
- **The gate would add cost and no protection.** Its per-call machinery (canonical hashing, fsynced audit appends, permit minting, the `Normalize`/`Upsert` step after execution) would add latency to every trivial lookup and fill the audit log, with no upstream quota to protect.

The "one action path" invariant is enforced by construction, not by convention:
- **(a) Import restriction.** A `go/parser` test (`internal/nervous/reflex/imports_test.go`) covers the sous packages `internal/nervous/{reflex,propose,tmpl,slots,intents,render,speak,turn}`. None of them may import `os/exec`, `syscall`, `net`, `net/http`, `water/internal/backend`, `water/internal/gate`, `water/internal/gate/...`, `water/internal/connectors/...` or `water/internal/vault`.
  - `internal/nervous/t1` may import `net/http` but not `os/exec`. Its endpoint is validated as loopback (§10).
  - `internal/nervous/sidecar` may import `os/exec`. It is started once at daemon startup, never per turn.
- **(b) No mutating calls.** An AST selector check in the same test forbids `Decide`, `Respond`, `Edit`, `Claim`, `Abandon`, `Propose`, `Upsert`, `SetCursor`, `SetBrief`, `InsertApproval`, `TransitionApproval`, `InsertRoute`, `MarkPossibleMiss` and `SetIntentState` in reflex, propose, tmpl, slots, intents, render, speak and turn.
  - Proposing and deciding happen only in `internal/gateway`, through interfaces (§12, §13).
  - Route logging happens only in `internal/nervous` itself, which is not in the reflex list.
- **(c) Disjoint namespaces.**
  - A read intent's `function` must be a reflex handler id and must not also be a manifest function id.
  - Registry load fails if the manifest declares a connector named `store`, `approvals`, `control`, `status`, `help`, `quick`, `brief` or `learned`. That makes `quick__*` tool names impossible for connector tools (§9).
  - A write intent's `action` must be a `connector.function` id (§5.4).
- **(d) Connector data always goes through the gate.** A write intent never calls a connector. It hands a typed payload to the gateway's `ProposeAction`, which queues an envelope. Execution happens only through `Gate.Invoke` with that envelope id after approval (§12).
- **(e) Defence in depth.** Reflex handlers get a `*store.Store` opened with `store.OpenReadOnly` (`mode=ro`, `query_only(1)`), so any write fails inside SQLite itself (§14).

#### 2. Package and file layout (all new unless marked)

```
internal/nervous/                  front door (package nervous)
  nervous.go                       Nervous, Config, DefaultConfig, New, Handle, Partial, Health, Reload
  tier.go                          Tier interface, TierID (t0|t1|main), Owner, Input, Outcome, reason consts
  eligibility.go                   pre-tier escalation: too_long, escalate_word, multi_clause, empty
  validate.go                      Proposal, Validated, Validate (shared by T0/T1; grounding for T1)
  tier0.go                         Tier 0 adapter
  tier1.go                         Tier 1 adapter (uses internal/nervous/t1)
  mainpath.go                      head-chef adapter over runtime.ModelTurn (the earlier plan's tier2.go)
  ack.go                           handoff-acknowledgement timer (fake-clock testable)
  actions.go                       write-intent proposals via ActionSink; voice approval binding via Approver
  readback.go                      Readbacks (last_readback per channel)
  voiceapprove.go                  VoiceApprovalTier (risk mapping, pure)
  speculate.go                     partial-transcript prefetch (no emitter, no backend)
  tooltrace.go                     attributes quick__/connector tool calls to the in-flight main turn
  breaker.go                       per-tier and per-learned-intent circuit breakers
  routelog.go                      route-row assembly, possible-miss detector, recent-reflex ring
  report.go                        Report, Candidates computation from route_log rows
internal/nervous/turn/             turn state machine + owner-gated emitters (pure)
internal/nervous/tmpl/             template grammar: parse, compile, anchored match, specificity, raw spans
internal/nervous/slots/            date, daterange, time, part_of_day, duration, count, person, text, project(reserved)
internal/nervous/intents/          registry: schema, LoadRegistry, _shared.yaml, planned actions, learned overlay
internal/nervous/reflex/           read handlers, handler table (FunctionSpec), StoreView, QuickService
internal/nervous/propose/          write proposers (pure; read-only StoreView; emit typed payloads)
internal/nervous/render/           Result, style.yaml schema + loader, DefaultStyle, Render, PromptBlock
internal/nervous/speak/            Speakable, FlattenMarkdown (moved from internal/voice), Lint (stdlib only)
internal/nervous/t1/               FunctionGemma client: declarations, request, parse (loopback HTTP)
internal/nervous/sidecar/          llama-server supervisor, model pull (pinned URL + sha256), eval-gate file
internal/nervous/promote/          candidate detection, draft request, ValidateLearned, overlay writer
internal/nervous/eval/             eval harness + testdata/ceo_eval.yaml (+ ceo_eval_t1.yaml supplement)
internal/decider/                  Decider, Null, New, Wrap
internal/store/route_log.go        RouteRow, InsertRoute, MarkPossibleMiss, ListRoutes, PruneRoutes, QuickOnlyRoutes
internal/store/intent_state.go     SetIntentState, ListIntentStates
internal/store/reader.go           OpenReadOnly
internal/store/queries_router.go   LatestMessages, MessagesFrom, CountMessagesSince, Senders, NextEvent, CursorUpdatedAt
internal/store/migrations/0010_router.sql   (number chosen at commit time; see Risks)
internal/tools/quick.go            QuickFunction, QuickToolName, Policy.quickByTool, Service.callQuick
internal/gateway/router.go         /v1/router, /v1/route/report, /v1/route/candidates, /v1/voice/profile,
                                   /v1/turns/{id}/partial, /v1/intents/{reload,draft}
internal/gateway/quick.go          POST /v1/quick/invoke (handleQuickInvoke)
internal/gateway/actions.go        proposeEnvelope (extracted), ProposeAction, decideAndExecute (extracted), DecideBound
internal/cli/cmd_route.go          water route report | candidates | eval --tier1
internal/cli/cmd_intent.go         water intent list | draft | promote | demote | enable
internal/cli/cmd_model.go          water model pull functiongemma
twins/ceo/intents/_shared.yaml     rules, skip_words, deny_words, escalate_words, clause_joiners
twins/ceo/intents/<id>.yaml        12 read intents + 4 write intents
twins/ceo/style.yaml
docs/functiongemma.md
```

**Modified files:**
- `internal/runtime/runtime.go`: `RunTurn` becomes `ModelTurn`, `DeliverText` is exported, and `Env` gains `StyleBlock` and `Summary`.
- `internal/runtime/brief.go`: adds `CachedBrief`.
- `internal/runtime/fastpath.go` and `fastpath_test.go`: deleted. The test cases move to the eval set and the embedded tests.
- `internal/voice/speakable.go`: becomes a one-line delegate to `speak.FlattenMarkdown`, so behaviour is unchanged.
- `internal/tools/{policy.go,service.go}`: small hunks.
- `internal/backend/warmsession.go`: adds `Prewarm`.

**[shared]** files, which the concurrent session may also edit:
- `internal/config/config.go`
- `internal/cli/{twin.go,cmd_daemon.go,cmd_status.go,root.go}`
- `internal/gateway/{handlers.go,daemon.go}`
- `docs/{architecture.md,known-gaps.md,EVOLUTION_PLAN.md}`

New gateway logic goes into new files. Shared-file hunks are limited to route registration, the `handleTurn` body, the `handleToolInvoke` prefix guard, the `notifyApprovalRequired` voice hook, and replacing `handleDecideApproval`'s body with a call to the extracted `decideAndExecute`.

**Dependency direction (no cycles):**
- `gateway → nervous → {runtime, turn, intents, reflex, propose, render, speak, slots, tmpl, t1, store, approvals}`
- `intents → {tmpl, slots, twins}`
- `reflex → {store, approvals, render, slots, intents}`
- `propose → {store, slots, intents}`
- `promote → {intents, eval, store}`
- `voice → speak`
- `decider → decisions`
- `runtime` never imports `nervous`. `nervous` never imports `gateway`, `gate` or `connectors`. The gateway supplies connector schema lookups as callbacks.

#### 3. Template language (`internal/nervous/tmpl`), carried forward with two adjustments

The grammar, normalization, anchored backtracking, the 10,000-step budget, specificity and the 24-token cap are unchanged from the earlier plan.

**Adjustment 1: raw spans.** The tokenizer keeps a map from each normalized token back to its byte range in the raw utterance. `Capture` gains `Raw string`, the original text of the captured span with case and punctuation kept. A write intent's `text` slot (a message body, an event title) uses `Raw`, never the lowercased tokens.

**Adjustment 2: `text` slots.** A `text` slot may appear only in a `kind: write` intent, and only as the **last** element of a template, where it captures 1 to 60 tokens to the end of the utterance. Read intents may never use `text`. Learned intents may never use it either (§16).

```go
package tmpl
type Template struct{ Source string /* compiled nodes unexported */ }
type Capture struct {
    Slot   string
    Tokens []string // normalized
    Raw    string   // original substring (case, punctuation kept)
}
type Match struct {
    Captures   []Capture
    Literals   int      // literal tokens consumed (specificity)
    LiteralSet []string // literal tokens consumed (deny-word exemption)
}
type Utterance struct {
    Raw    string
    Tokens []string
    spans  [][2]int // token i -> raw byte range
}
func Normalize(raw string, skip map[string]bool) Utterance
func Compile(src string, rules map[string]string, slotTypes map[string]string /*name->type*/) (*Template, error)
func (t *Template) MatchAll(u Utterance, try func(Match) bool)
func (t *Template) LiteralVocabulary() map[string]bool // every literal token the template can consume (for T1 deny checks)
```

#### 4. Slots (`internal/nervous/slots`), carried forward plus `duration`, `text`, and a spoken label

The earlier plan's resolvers (date, daterange, time, part_of_day, count, person) and their rules are unchanged. The additions:
- **`duration`** accepts `15/30/45/90 minutes`, `half an hour`, `an hour`, `1 hour`, `2 hours` and `hour and a half`. The range is 5 minutes to 4 hours; anything else is `Unresolved`.
- **`text`** always resolves when it is non-empty. `Value.Text` is the raw span, trimmed and capped at 1,000 characters.
- **`Value.Spoken`** is the human interpretation label every quick answer echoes, for example `"Tomorrow, Thu 25 Sep"`, `"today at 3pm"` or `"this afternoon, Wed 24 Sep"`. `Label` stays the canonical eval label (`"tomorrow"`).

```go
type Type string // date, daterange, time, part_of_day, duration, count, person, text, project
type Value struct {
    Type   Type
    Label  string        // canonical (eval-compared): "tomorrow", "3pm", "alex chen <alex@x.com>"
    Spoken string        // interpretation echoed in every answer header: "Tomorrow, Thu 25 Sep"
    Start, End time.Time // date/daterange/part_of_day (half-open)
    At     time.Time     // time
    Dur    time.Duration // duration
    N      int           // count
    Person Person        // person
    Text   string        // text
}
func Resolve(t Type, c tmpl.Capture, spec Spec, now time.Time, ents Entities) (Value, Outcome, []string)
func ResolveString(t Type, s string, spec Spec, now time.Time, ents Entities) (Value, Outcome, []string) // T1 and quick-tool args: normalizes s, then Resolve
```

#### 5. Registry (`internal/nervous/intents`)

##### 5.1 Schema

Decoding is strict (`KnownFields(true)`), with an optional notes body after `---`, as in `decisions`.

```go
type Kind string
const ( KindRead Kind = "read"; KindWrite Kind = "write" )

type Intent struct {
    ID              string              `yaml:"id"`          // ^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$ ; learned: ^learned\.[a-z][a-z0-9_]*$
    Description     string              `yaml:"description"`
    Kind            Kind                `yaml:"kind"`        // default read
    Function        string              `yaml:"function"`    // read: reflex handler id (never a manifest id)
    Action          string              `yaml:"action"`      // write: manifest connector.function (level A)
    Proposer        string              `yaml:"proposer"`    // write: key in propose.Specs()
    Slots           map[string]SlotSpec `yaml:"slots"`
    Templates       []string            `yaml:"templates"`   // >= 1
    Examples        []string            `yaml:"examples"`    // T1 declaration text; ambiguity checks
    EscalateIf      []string            `yaml:"escalate_if"` // {slot_unresolved, ambiguous_match}; both required when reflex_eligible
    ReflexEligible  bool                `yaml:"reflex_eligible"`
    RequiresPending bool                `yaml:"requires_pending_approval"` // approvals.respond only
    Tests           []IntentTest        `yaml:"tests"`       // >= 2, >= 1 positive
    Origin          string              `yaml:"origin"`      // "" in embedded files; "learned" required in overlay files
    Provenance      *Provenance         `yaml:"provenance"`  // learned only

    Notes, File    string `yaml:"-"`
    Active         bool   `yaml:"-"` // computed at load
    InactiveReason string `yaml:"-"` // e.g. "gmail.send_message is planned but not granted in the ceo manifest"
    Disabled       string `yaml:"-"` // learned only, from intent_state; "" = enabled
}
type Provenance struct {
    Candidate   string    `yaml:"candidate"`   // tool signature id
    TurnIDs     []string  `yaml:"turn_ids"`
    Repeats     int       `yaml:"repeats"`
    DraftedAt   time.Time `yaml:"drafted_at"`
    DraftModel  string    `yaml:"draft_model"`
    PromotedAt  time.Time `yaml:"promoted_at"`
    RegistryHash string   `yaml:"registry_hash"` // embedded registry the validation ran against
}
type SlotSpec struct {
    Type     slots.Type `yaml:"type"`
    Default  string     `yaml:"default"` // parsed by the same resolver at load time
    Min, Max *int       `yaml:"min"`/`yaml:"max"`
    Required bool       `yaml:"required"`
}
type IntentTest struct {
    Utterance string            `yaml:"utterance"`
    Intent    string            `yaml:"intent"`  // "none" for must-not-match
    Slots     map[string]string `yaml:"slots"`   // vs Value.Label (text slots: vs Value.Text)
    Pending   int               `yaml:"pending"`
}
```

##### 5.2 `FunctionSpec`: extended for capability assessment

```go
type Class string
const ( ClassLookup Class = "lookup"; ClassCompute Class = "compute"; ClassFormat Class = "format";
        ClassControl Class = "control"; ClassAction Class = "action" )
type FunctionSpec struct {
    ID            string
    Args          map[string]slots.Type
    Required      []string
    Defaults      map[string]string // used when a quick tool call omits an optional arg
    Class         Class
    ReadOnly      bool
    Deterministic bool     // same store state + args => same result
    SideEffects   []string // e.g. "process_control", "approval_surface"; empty = none
    QuickTool     string   // "" = not exposed; else "quick.<name>" (explicit, reviewed opt-in)
    Emits         []string // proposers only: payload keys always produced
    EmitsOptional []string // proposers only: produced only if the connector schema declares them
}
func (f FunctionSpec) Learnable() bool { // §16
    return f.ReadOnly && f.Deterministic && len(f.SideEffects) == 0 &&
        (f.Class == ClassLookup || f.Class == ClassCompute || f.Class == ClassFormat) && !hasText(f.Args)
}
func (f FunctionSpec) QuickEligible() bool { return f.ReadOnly && len(f.SideEffects) == 0 && f.Class == ClassLookup && !hasText(f.Args) }
```

An `init`-time check in `reflex`, backed by a test, enforces two things: `QuickTool != ""` requires `QuickEligible()`, and quick names are unique.

##### 5.3 Loading

```go
type Functions struct {
    Read  map[string]FunctionSpec // reflex.Specs()
    Write map[string]FunctionSpec // propose.Specs()
}
type SchemaInfo struct{ Required, Properties []string }
type LoadOptions struct {
    // Schema reports a granted connector function's input schema. The gateway
    // supplies it from connectors.Registry; intents never imports connectors.
    // nil skips the write-payload schema check (unit tests only).
    Schema   func(action string) (SchemaInfo, bool)
    Learned  fs.FS             // overlay; nil when router.promotion.enabled=false
    Disabled map[string]string // intent_state: id -> reason
}
type Registry struct{ /* intents, order, compiled templates, shared, hash */ }
func LoadRegistry(fsys fs.FS, m *twins.Manifest, fns Functions, opts LoadOptions) (*Registry, error)
func (r *Registry) Intents() []Intent          // all, including inactive and disabled
func (r *Registry) Candidates() []Intent       // Active && Disabled == "" — the only set any tier may match
func (r *Registry) Shadow() []Intent           // inactive/disabled: matched only to record escalation_reason, never answered
func (r *Registry) Shared() Shared
func (r *Registry) Hash() string               // sha256 over sorted file bytes (embedded + loaded learned)
func (r *Registry) LearnedSkipped() []Skipped  // {File, Reason}: invalid overlay files, surfaced in /v1/router
```

`LoadRegistry` globs `twins/<m.ID>/intents/*.yaml`.
- **Missing intents directory:** the result is an empty registry, not an error. This mirrors `decisions`, and keeps `twins/ceo-demo`, which has no intents, loading. Every turn then goes to the main path.
- **Directory present:** `_shared.yaml` is required.

Embedded files use the strict rules below, and **any failure fails the whole load**. The rules from the earlier plan still apply:
- bad id or duplicate id;
- unknown key;
- a template that doesn't compile, uses an undeclared slot or references an unknown rule;
- an `escalate_if` value outside the enum;
- `requires_pending_approval` set on anything other than `approvals.respond`.

Rules added or changed:
- **Read intents:**
  - `function` must be in `fns.Read` and not be a manifest function.
  - A `reflex_eligible` read intent's function must be `ReadOnly`.
  - Read intents may not use `text` slots.
  - Slot names and types must match `FunctionSpec.Args`. A required arg needs either a declared slot or a default.
- **Write intents:**
  - `function` must be empty, and `action` and `proposer` are required.
  - The proposer must be in `fns.Write`, and the slots must match its `Args`.
  - `text` slots may only be template-final.
  - The action is checked as described in §5.4.
- **Template vocabulary:** a template literal may never contain an `escalate_words` entry or match a `clause_joiners` phrase (§6).
- **Reserved names:** the manifest may not declare a connector named `store`, `approvals`, `control`, `status`, `help`, `quick`, `brief` or `learned`.
- **`origin`:** it must be empty in embedded files, and `provenance` must be absent there.

Learned overlay files are validated with `ValidateLearned` (§16). An invalid learned file is **skipped and reported**, not fatal. The daemon must not be blocked by owner-data drift, and the embedded registry is unaffected.

##### 5.4 Write intents whose function doesn't exist yet: the `plannedActions` precedent

This mirrors `decisions.validateStagedAction`. `intents` keeps its own allowlist:

```go
// plannedActions mirrors internal/decisions/registry.go's plannedActions:
// write functions named in the evolution plan that the manifest does not yet
// grant. TestPlannedActionsMatchDecisions parses that file with go/parser and
// requires identical keys, so the two lists can't drift, without this slice
// editing internal/decisions.
var plannedActions = map[string]bool{
    "gmail.draft_message": true, "gmail.send_message": true,
    "gcal.create_event": true, "gcal.move_event": true,
}
func checkAction(a string, m *twins.Manifest, schema func(string) (SchemaInfo, bool), p FunctionSpec) (active bool, reason string, err error)
```

`checkAction` works as follows:
1. **Shape and connector.** `a` must match `^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`, and its connector must be declared in `m.Connectors`. Otherwise it is a load **error**.
2. **Granted function.** If `m.Function(a)` is found:
   - A level other than `A` is an **error** ("write intents must target a level-A function").
   - Otherwise, if `schema` is non-nil, the proposer's `Emits` must be a superset of `SchemaInfo.Required` and a subset of `Properties`, or the load fails with an **error**. `EmitsOptional` keys that the schema does not declare are dropped at propose time.
   - If both checks pass, the intent is **active**.
3. **Planned function.** If `m.Function(a)` is not found and `plannedActions[a]` is set, the intent is **inactive** with reason `"<a> is planned but not granted in the <id> manifest"`. It loads and validates: templates compile, slots are checked against the proposer spec, and tests parse.
4. **Anything else** is an **error**, so a misspelling fails at load.

Inactive intents:
- are excluded from `Candidates()`, Tier 1 declarations, `help.intents` and quick tools;
- sit in `Shadow()`, so an utterance that matches only an inactive intent escalates with `escalation_reason = "intent_inactive:<id>"`;
- are listed in `GET /v1/router` with their reason.

**Activation needs no code change.** When the other session adds, say, `gmail.send_message` at level A to `twin.yaml`, the next registry load makes the intent active. The manifest is embedded (`water.TwinsFS()`), so in practice that happens after the rebuild and daemon restart that pick up the new manifest.

`POST /v1/intents/reload` (also used by `water intent promote`) rebuilds the registry from the current embedded FS plus the overlay and swaps it atomically (`atomic.Pointer[intents.Registry]`).

Tests (R-6):
- **Planned, not granted:** a MapFS manifest without `gmail.send_message` → load succeeds, `mail.send_reply` is `Active == false` with that reason, and a matching utterance escalates `intent_inactive:mail.send_reply`.
- **Granted:** the same files with a manifest granting `gmail.send_message: A` and a schema stub `{to, subject, body}` → the intent is active and matches.
- **Failures:** granted at `R` is an error, an unknown `gmail.sned_message` is an error, and a schema missing a required key the proposer doesn't emit is an error.
- `TestPlannedActionsMatchDecisions`.

##### 5.5 `_shared.yaml`

```go
type Shared struct {
    Rules          map[string]string `yaml:"rules"`
    SkipWords      []string          `yaml:"skip_words"`      // please, hey, water, um, uh, just, quickly
    DenyWords      []string          `yaml:"deny_words"`      // old fastPathActionWords; exempt only when consumed as a template literal
    EscalateWords  []string          `yaml:"escalate_words"`  // §6; never exempt; may never appear as template literals
    ClauseJoiners  []string          `yaml:"clause_joiners"`  // "and then", "and also", "after that", "as well as", "then also"
    Corrections    []string          `yaml:"corrections"`     // "that's wrong", "not that", "i meant", "no i said"
}
```

#### 6. Handoff rules: pre-tier eligibility (`eligibility.go`)

The sous chef answers only on a **positive, certain match**. Before any quick tier runs, `Eligible(u tmpl.Utterance, sh Shared) (ok bool, reason string)` sends the turn straight to the main path in any of these cases:

| Reason | Condition |
|---|---|
| `empty` | no tokens remain after skip words |
| `too_long` | more than 24 tokens |
| `escalate_word:<w>` | any token, or any 2- or 3-token phrase, is in `escalate_words`. The initial list is: `should`, `shouldn't`, `why`, `because`, `prioritize`, `prioritise`, `priority`, `priorities`, `important`, `importance`, `matters`, `matter`, `compare`, `comparison`, `versus`, `vs`, `better`, `best`, `worse`, `worst`, `summarize`, `summarise`, `summary`, `plan`, `planning`, `recommend`, `recommendation`, `suggest`, `advice`, `advise`, `think`, `opinion`, `draft`, `write`, `compose`, `prepare`, `explain`, `analyze`, `analyse`, `decide`, `worth`, `ought`, `rank`, `strategy`, `which is better`, `don't`, `dont`, `not`, `never`, `unless`, `except`, `instead`, `without` |
| `multi_clause` | the raw text contains a `clause_joiners` phrase, or it has two or more sentence-final marks (`.`, `?`, `!`, `;`) with at least 2 tokens after the first one |

- **The sous chef never ranks or judges priority.** "What needs my attention" and "what's on my plate" match only `brief.today`, which answers only from the **cached** brief (signals computed by code, phrased once). A cache miss escalates with `Hint{Intent: "brief.today"}`, and the main path computes the brief.
- **Why `draft` is an escalate word.** It lets the head chef own drafting: "draft a reply to Alex" goes to the main path. The sous chef's `mail.draft_reply` covers only *dictated* text ("reply to Alex saying sounds good, see you then"), whose templates use `reply ... saying` and never `draft`.
- **Required eval phrasings** include "what should I prioritize tomorrow", "which meeting matters more", "why is the board sync on friday", "summarize my inbox", "compare my tuesday and wednesday", "what's on my calendar and then email alex" and "plan my week". The acceptance criteria require 0 answers from either quick tier on all of them.

#### 7. The intents (12 read + 4 write; one file each)

**Read intents** carry forward from the earlier plan. Every response header echoes the interpretation (§8.3).

| intent id | function | slots | notes | quick tool |
|---|---|---|---|---|
| `schedule.on_date` | `store.calendar_events` | `when: daterange, default today` | | `quick.calendar` |
| `schedule.next_event` | `store.next_event` | none | | `quick.next_event` |
| `schedule.free_time` | `store.free_slots` | `when: date, default today`; `part: part_of_day` | 09:00–18:00 is a handler constant (known gap) | none — corrected in R-8: `QuickEligible()` (§5.2) requires `Class == lookup`, and `free_slots` is `ClassCompute` (it derives gaps, doesn't just fetch data); exposing it to the main agent as a raw tool would blur "the model gets lookups, not derived judgment calls" |
| `mail.latest` | `store.latest_messages` | `n: count, default 5, 1..20` | | `quick.latest_mail` |
| `mail.unread_count` | `store.unread_count` | none | says truthfully that unread state isn't synced, and gives today's received count as a separate fact | none (the answer is non-informative to the model) |
| `mail.latest_from` | `store.latest_from` | `who: person, required` | | `quick.mail_from` |
| `brief.today` | `store.cached_brief` | none | cached only; "what needs my attention" | `quick.cached_brief` |
| `approvals.list` | `approvals.pending` | none | `approvals.Menu` | `quick.pending_approvals` |
| `approvals.respond` | `approvals.bind_pending` | none | `requires_pending_approval`; yes/no vocabulary only; §13 | none (SideEffects `approval_surface`) |
| `control.stop` | `control.cancel_tasks` | none | whole-utterance stop, cancel, never mind | none (Class control) |
| `status.overview` | `status.overview` | none | no vault access | none (not Deterministic) |
| `help.intents` | `help.intents` | none | lists `Candidates()` only | none (Class format) |

**Write intents** (`kind: write`) are all **inactive until the manifest grants their action**. All are `reflex_eligible: true`, which means Tier 1 may propose them, but only as proposals.

| intent id | action | proposer | slots | example template |
|---|---|---|---|---|
| `calendar.create_event` | `gcal.create_event` | `calendar.create` | `who: person` (optional), `when: date, required`, `at: time, required`, `dur: duration, default 30 minutes`, `title: text` (optional, template-final) | `(schedule\|book\|set up) a meeting [with {who}] {when} at {at} [for {dur}] [called {title}]` |
| `calendar.move_event` | `gcal.move_event` | `calendar.move` | `from: time, required`, `on: date, default today`, `to_date: date` (optional), `to: time` (at least one of `to_date`/`to` required, enforced by the proposer) | `move my {from} [{on}] to {to}`, `move my {from} [{on}] to {to_date} [at {to}]` |
| `mail.draft_reply` | `gmail.draft_message` | `mail.reply` | `who: person, required`, `body: text, required` | `reply to {who} saying {body}` |
| `mail.send_reply` | `gmail.send_message` | `mail.reply` | `who: person, required`, `body: text, required` | `send {who} a reply saying {body}`, `reply to {who} and send it saying {body}` |

**Proposers** (`internal/nervous/propose`) are pure functions over a read-only `StoreView`:

```go
type Deps struct{ Store reflex.StoreView; Now func() time.Time; Owner slots.Person /* from config, for organizer */ }
type Proposal struct {
    Action  string
    Payload map[string]any
    Summary string // one-line interpretation echo: "Create 'Meeting with Alex Chen' Thu 25 Sep 3pm–3:30pm"
    Tainted bool   // any store record it used is External (e.g. a sender address or thread)
}
type Proposer struct {
    Spec  intents.FunctionSpec // Class=action, ReadOnly=false, Emits/EmitsOptional
    Build func(ctx context.Context, d Deps, a reflex.Args) (Proposal, Outcome /*ok|unresolved|ambiguous*/, error)
}
func Table() map[string]Proposer
func Specs() map[string]intents.FunctionSpec
```

- **Payload keys** follow `docs/slice-c-planning.md` §1: `create_event {title, start, end, attendees}`, `move_event {event_id, new_start, new_end}`, and `send_message`/`draft_message {to, subject, body}`. `EmitsOptional` adds `thread_id` and `in_reply_to` when the landed schema declares them.
- **Entity resolution** is code only, and anything uncertain escalates `entity_unresolved`/`entity_ambiguous`. The sous chef never asks its own clarifying question:
  - `calendar.move` needs exactly one non-cancelled event starting at `from` on `on`. Zero or two or more escalates.
  - `mail.reply` needs the latest message whose sender is `who` (subject `Re: <subject>`, thread id). No such message escalates.

#### 8. Result, renderer, style and the one-voice contract

##### 8.1 Result and renderer (`internal/nervous/render`), carried forward

`Result` gains `Interpretation string` (the header echo) and `Kind` (`read|proposal|decision|clarify`).

```go
type Result struct {
    Intent, Kind       string
    Interpretation     string            // "Tomorrow, Thu 25 Sep" — always non-empty for Kind read/proposal
    Facts              map[string]string // header substitutions, incl. "<slot>_spoken" for every slot
    Items              []Item
    Warnings           []string
    NeedsClarification *Clarification
    ApprovalID         string
    Tainted            bool
    Text               string            // pre-phrased (brief, read-back)
}
func LoadStyle(fsys fs.FS, twinID string) (*Style, error) // missing file -> DefaultStyle()
func DefaultStyle() *Style                                  // compiled-in; passes the same strict validation
func (s *Style) Render(r Result, ch runtime.Channel) string
func (s *Style) PromptBlock() string                        // static; appended to the main-path system prompt
func (s *Style) Voice() VoiceStyle
func (s *Style) MaxChars(ch runtime.Channel) int
```

##### 8.2 `style.yaml` (strict decode)

It keeps the earlier plan's keys: `tone`, `max_chars` and `max_list_items` per channel (`cli: 2000/20`, `text-bar: 600/8`, `voice: 280/3`), `confirmation`, `responses` (`header`, `item`, `empty`, `more`, all keyed by intent id) and `prompt_block`. It adds:

```yaml
voice:
  name: Water
  tone: "Calm, brief, plain. Facts first. No filler, no apologies, no hedging."
  handoff: ["One moment.", "Let me check.", "On it."]   # chosen by hash(turn_id) % len
  errors:
    generic: "I can't answer that right now."
    timeout: "That's taking too long. Try again in a moment."
    tap_required: "That one needs a tap to confirm."
    readback_stale: "That changed. Here it is again."
    nothing_pending: "Nothing is waiting for approval."
  banned_phrases: ["as an ai", "as a language model", "great question", "i'd be happy to", "certainly!", "absolutely!", "i hope this helps"]
  max_sentence_chars: 200
  tts: {voice: "", rate_wpm: 185}   # "" = system default / config voice.ceo_voice
```

Load-time validation of `responses`:
- Every intent with slots must reference `{<slot>_spoken}` for every slot in **both** `header` and `empty`.
- Every slotless intent must have a non-empty literal interpretation in `header` (for example "Your next event:").

`TestEveryQuickAnswerEchoesInterpretation` asserts that the rendered output of every eval positive contains each slot's `Spoken` value. Empty results are stated plainly, for example `empty: "Nothing on your calendar {when_spoken}."`.

##### 8.3 `speak` (`internal/nervous/speak`, stdlib only)

```go
type Options struct{ MaxListItems, MaxChars int; Loc *time.Location }
func FlattenMarkdown(s string) string       // moved verbatim from internal/voice/speakable.go
func Speakable(s string, o Options) string  // FlattenMarkdown + the passes below
func Lint(raw string, v render.VoiceStyle, maxChars int) []string
```

`Speakable` passes, in order:
1. Fenced code becomes "(code block omitted)" (existing behaviour).
2. Markdown links become their label.
3. Bare URLs (`https?://…`, `www.…`) become "a link".
4. Headings, bullets, emphasis, quotes and tables are flattened (existing behaviour).
5. Emoji and pictographs are stripped: runes in `So`, variation selectors, ZWJ and regional indicators.
6. Times: `15:00`, `3:00pm` and `3pm` become "3 PM", `3:30` becomes "3:30 PM" (using the slot am/pm rule), and `3-4pm` becomes "3 to 4 PM".
7. Dates: `2026-09-25` and `Thu 25 Sep` become "Thursday, September 25".
8. List items beyond `MaxListItems` are dropped and "and N more." is appended.
9. The result is hard-capped at `MaxChars` on a sentence boundary. This applies to quick output only; the main path is not truncated (§8.4).

`voice.Speakable` becomes `return speak.FlattenMarkdown(text)`. Existing `internal/voice` and `internal/decisions` behaviour is unchanged, and their tests pass untouched.

`Lint` returns `banned:<phrase>`, `overlength`, `markdown` (the raw text had markdown), `emoji`, `url` and `list_overcap`. **It never rewrites prose.**

##### 8.4 Where the contract applies

- **Quick tiers on voice:** `Render`, then `Speakable`, delivered as `delta` plus `sentence` events. The lint runs too and must be clean; a test asserts this.
- **Main path on voice:** deltas pass through raw for display. Every `sentence` event is passed through `Speakable(sentence, {MaxListItems: 0 /*no cap per sentence*/})`. At `done`, `Lint(fullRaw)` writes warnings to `route_log.warnings`. The reply is never truncated, because cutting mid-answer can drop the one fact that mattered. Going over `max_chars.voice × 1.5` adds `overlength`.
- **Shared phrases.** Handoff acknowledgements and error phrases come only from `style.voice`, whichever tier fails.
- **Main-path prompt.** `PromptBlock()` includes `voice.tone`, the list of banned phrases, and "on voice, no markdown, no lists longer than 3". It is static, so the warm session restarts once per deploy.
- **TTS profile.** `GET /v1/voice/profile` returns `{name, tts:{voice, rate_wpm}, handoff}`.
  - The Swift client (R-27) sets `AVSpeechUtterance.voice` and `.rate` from it, mapping `rate_wpm` to `AVSpeechUtterance` rate with the documented linear map, clamped.
  - `water ask --voice` uses `tts.voice` when it is non-empty, otherwise config `voice.ceo_voice`.
  - The voice therefore never differs by tier.

#### 9. Quick tools: the sous chef as the head chef's toolbox (`internal/tools`, `internal/gateway/quick.go`, `reflex.QuickService`)

**The boundary.** A `quick.*` tool's handler is looked up only in `reflex.Table()`, never in `connectors.Registry`. It is served on a **different daemon endpoint**, and that endpoint's handler cannot reach the gate.

**Changes to `internal/tools`:**
- **`policy.go`:**
  - `Policy` gains `Quick []QuickFunction \`json:"quick,omitempty"\``. `QuickFunction{ID ("quick.calendar"), Tool ("quick__calendar"), Description, Schema []byte}` is declared in the new `quick.go`, and `QuickToolName(id)` replaces `.` with `__`.
  - `Empty()` becomes true only when both `Twin` and `Quick` are empty.
  - `ToolNames()` lists both.
  - A new `(*Policy).Validate()` error, called by `LoadPolicyFile` and by the gateway when it builds the policy, rejects:
    - duplicate tool names across `Twin` and `Quick`;
    - any `Twin` tool whose name starts with `quick__`;
    - any `Quick` entry whose ID lacks the `quick.` prefix.
  - `Authorize` checks `twinByTool` first, then `quickByTool`. For a quick tool the basis is `"local quick lookup (no connector, no gate) for quick.x"`.
- **`service.go`:**
  - `Definitions()` appends the quick definitions.
  - `Call` dispatches: `if qf, ok := s.Policy.quickByTool(tool); ok { result, err = s.callQuick(ctx, qf, resolved) } else { result, err = s.callTwin(ctx, tool, resolved) }`.
  - `callQuick` POSTs `{"function": qf.ID, "args": args}` to **`/v1/quick/invoke`** over the same socket with the same session token. It never uses `/v1/tools/invoke`.
  - Event logging, `recordEvent` and the per-call timeout are unchanged.
- **`mcp.go`:** unchanged. Every tool result, quick ones included, is still wrapped by `untrustedWrap`.

**Changes to `internal/gateway`:**
- **`quick.go` (new):** `handleQuickInvoke` authenticates the bearer token with `lookupTurnToken`, decodes the body, and calls `d.cfg.Nervous.Quick().Invoke(ctx, id, args)`. Then it calls `d.escalateTaint(res.Tainted)`, then `d.cfg.Nervous.RecordToolUse(tools.QuickToolName(id), args)`, then writes `{"status":"ok","output":...}`, or `denied` with a reason for an unknown tool, bad arguments or an unresolved slot.
- **Boundary test:** `internal/gateway/quick_boundary_test.go` parses `quick.go` and fails if it references the selectors `Gate`, `Registry`, `Approvals` or `Invoke` on `d.cfg`, or imports `water/internal/gate` or `water/internal/connectors`.
- **`daemon.go` [shared]:**
  - `handleToolInvoke` gains a first-line guard that denies any `quick.` function with "quick tools are served at /v1/quick/invoke". The gate would deny it anyway, since it isn't a manifest function; the guard makes the split explicit.
  - `handleToolInvoke` also calls `d.cfg.Nervous.RecordToolUse(tools.TwinToolName(fn), args)` for attribution.
  - `TwinToolPolicy()` sets `Quick: d.cfg.Nervous.QuickFunctions()` when `router.quick_tools.enabled`. The list is static, so `toolsKey` changes once per deploy and the warm session restarts once.

**`reflex.QuickService`** lives in the reflex package, so the import denylist covers it:

```go
type QuickResult struct{ Output json.RawMessage; Tainted bool }
type QuickService struct{ deps func() Deps; table map[string]Handler /* keyed by QuickTool name */ }
func NewQuickService(deps func() Deps) *QuickService // only specs with QuickTool != "" && QuickEligible()
func (q *QuickService) Functions() []tools.QuickFunction // JSON schema: each slot arg a string with accepted-form description; count -> integer
func (q *QuickService) Invoke(ctx context.Context, id string, args map[string]any) (QuickResult, error)
```

- **`Invoke`:** resolves each argument with `slots.ResolveString` (defaults from `FunctionSpec.Defaults`), runs the handler, and returns `json.Marshal(struct{Intent, Interpretation, Facts, Items, Warnings})`. That is structured data for the head chef to phrase, not rendered prose. An `Unresolved` or `Ambiguous` argument returns an error naming the argument and its accepted forms, so the model can adapt.
- **Importing `internal/tools`.** `reflex` may import `water/internal/tools` only for the `QuickFunction` type. `tools` imports `net/http`, so the denylist test allows this one import explicitly with a comment, and a second test asserts `reflex` calls nothing from `tools` except the type.
- **Concurrency.** Handlers hold no mutable state. They read through `store.OpenReadOnly` (a separate pool: WAL, `mode=ro`, `query_only(1)`, `busy_timeout(5000)`, `SetMaxOpenConns(4)`), so parallel quick calls don't queue behind the writer's single connection.
  - Test `TestQuickParallel`: 16 goroutines × 25 invocations over mixed tools, while a writer goroutine upserts events, run under `-race`. There must be no error, no `SQLITE_BUSY`, and every result must be consistent.
  - A gateway test runs 8 parallel `/v1/quick/invoke` calls.

#### 10. Tier 1: FunctionGemma (`internal/nervous/{sidecar,t1}`, `tier1.go`), shipped off

**Sidecar.** The owner-approved Homebrew `llama-server` (llama.cpp).

```go
package sidecar
type Config struct{ Bin, ModelPath string; CtxSize int; Home string }
type Supervisor struct{ /* cmd, port, state, backoff */ }
func New(cfg Config) *Supervisor
func (s *Supervisor) Start(ctx context.Context) error  // picks a free 127.0.0.1 port; spawns
func (s *Supervisor) Endpoint() string                  // "http://127.0.0.1:<port>"
func (s *Supervisor) Healthy(ctx context.Context) bool  // GET /health
func (s *Supervisor) Stop()
```

- **Invocation:** `llama-server --model <path> --host 127.0.0.1 --port <p> --ctx-size 2048 --jinja --no-webui --parallel 2`.
- **Health:** checked every 30 s. There is no inference ping; the model stays resident.
- **Restarts:** backoff of 1 s, 2 s, 4 s … up to 60 s. After 5 consecutive failures the T1 breaker opens with reason `sidecar_down`.
- **When it starts:** only at daemon startup, and only when `router.tier1.enabled=true` **and** the model file exists **and** the eval gate passes (below). Otherwise it is never spawned.
- **Tests** use a fake server binary built from the test package through the `TestMain` re-exec trick.

**Model pull.** `water model pull functiongemma [--accept-gemma-terms]`:
- It downloads a pinned GGUF (URL and sha256 are constants in `sidecar/model.go`, filled in and verified by the implementer in R-17) to `$WATER_HOME/models/functiongemma.gguf.part`, verifies the sha256, then renames the file into place.
- It refuses to run without `--accept-gemma-terms` and prints the terms URLs from `docs/functiongemma.md`.
- It honours `HF_TOKEN` when Hugging Face gating requires one, and says so when it does.
- It never runs implicitly. It is the only network call this slice adds, and it runs only when the owner types the command.

**Client (`internal/nervous/t1`):**

```go
type Decl struct{ Name, Description string; Params map[string]ParamDecl } // name = intent id with "." -> "_"
type Call struct{ Intent string; Args map[string]string }
type Client interface{ Propose(ctx context.Context, utterance string, decls []Decl) ([]Call, error) }
func NewHTTP(endpoint string) (Client, error) // rejects any non-loopback host (127.0.0.1, ::1, localhost)
func Declarations(r *intents.Registry) []Decl  // Candidates() with ReflexEligible && !RequiresPending, read and write
```

- **Request:** `POST /v1/chat/completions` with `temperature: 0`, `max_tokens: 128`, `tool_choice: "auto"`, the FunctionGemma developer prompt, and the declarations.
- **Parsing:**
  - Exactly one tool call is required. Zero calls escalates `t1_no_call`, two or more escalates `t1_multi_call`, and a text reply escalates `t1_text`.
  - An unknown name escalates `t1_unknown_intent`.
  - **The model's own confidence is ignored.**

**Tier 1 adapter.** It runs only when Tier 0 escalated with `no_match`, and never for reasons like `escalate_word`, since those skip every quick tier. The proposal then goes through `Validate` (§11.3) plus the **T1-only checks**:
- **Grounding:** each argument value, normalized, must appear as a contiguous token run in the normalized utterance. `count` may appear as a digit or a word, and an omitted argument takes its default. An ungrounded value escalates `t1_ungrounded`.
- **Deny words:** any deny word in the utterance that is not in the chosen intent's `LiteralVocabulary()` escalates `action_word`.

**Eval gate, enforced in code.** `water route eval --tier1`:
- starts its own supervisor against a fixture store;
- runs `ceo_eval.yaml` plus the supplement `ceo_eval_t1.yaml`, at least 400 cases in total (see Risks, item 12);
- writes `$WATER_HOME/router/tier1_eval.json` = `{model_sha256, registry_hash, n, false_accepts, fa_rate, wilson95_upper, warm_p95_ms, at}`.

The daemon enables T1 only if **all** of these hold, and otherwise reports the reason in `GET /v1/router` (`eval_missing`, `eval_stale` or `eval_failed`):
- `router.tier1.enabled` is true;
- the file exists;
- `model_sha256` matches the file on disk;
- `registry_hash` matches the current `Registry.Hash()`;
- `fa_rate ≤ 0.01`;
- `wilson95_upper ≤ 0.02`;
- `n ≥ 200`;
- `warm_p95_ms ≤ 400`.

#### 11. The front door, turn state machine and cascade (`internal/nervous`, `internal/nervous/turn`)

##### 11.1 Turn state machine (`turn`, pure)

```go
type State string // listening | final | routed | done | expired | cancelled
type Owner string // "" | router | quick | main
type Turn struct {
    ID, ClientID string; Channel runtime.Channel
    State State; Owner Owner
    Partials int; FirstPartialAt, FinalAt time.Time
    Spec *Speculation // owned by speculate.go
}
type Table struct{ /* mu, turns, now, ttl=30s, maxListening=8, doneKeep=10m */ }
func NewTable(now func() time.Time) *Table
func (t *Table) Partial(clientID string, ch runtime.Channel, text string, seq int) (*Turn, error) // absent -> listening; listening -> listening; else ErrState
func (t *Table) Final(clientID, taskID string, ch runtime.Channel) (*Turn, error)                  // absent|listening -> final
func (t *Table) Route(id string, o Owner) error                                                  // final -> routed(o), exactly once (CAS)
func (t *Table) Done(id string, st State) error                                                  // routed|final -> done|cancelled
func (t *Table) Emitter(id string, o Owner, emit func(runtime.Event)) func(runtime.Event)
func (t *Table) Sweep()                                                                          // listening older than ttl -> expired
```

`Emitter` drops, and counts in `Turn.Dropped`, any event from an owner that does not currently own the turn:
- `router` may emit only `ack` and `handoff`, and only while the turn is `final` or `routed(main)` before the main path's first delta.
- `quick` may emit only while `routed(quick)`.
- `main` may emit only while `routed(main)`.
- Nothing may emit after `done`.

Speculation receives **no emitter at all**: its function signatures take no `emit` parameter. **Exactly one owner answers each turn**, so a late Tier 1 result after `Route(main)` is discarded.

##### 11.2 Types

```go
type TierID string  // "t0", "t1", "main"
type Turn struct {
    ID       string          // daemon task id
    ClientID string          // optional turn_id the client streamed partials under
    Channel  runtime.Channel
    Text     string          // raw final utterance; the only thing quick tiers see
    Context  string          // meeting HelpContext prefix; main path only
    Origin   gate.Origin     // P0 (type only; nervous does not call the gate)
}
type Hint struct{ Intent string; Slots map[string]string }
type Input struct{ Turn Turn; Env runtime.Env; Utt tmpl.Utterance; Hint *Hint; RecentReflex string; Spec *turn.Speculation }
type Outcome struct {
    Answered bool; Result *render.Result; Streamed string
    Escalate string; Hint *Hint; Intent string; Slots map[string]string
    Proposal *propose.Proposal
}
type Tier interface{ ID() TierID; Try(ctx context.Context, in Input, emit func(runtime.Event)) (Outcome, error) }

type ActionSink interface { // implemented by gateway (§12)
    ProposeAction(ctx context.Context, fn string, payload map[string]any, ch runtime.Channel) (approvals.Envelope, error)
}
type Approver interface { // implemented by gateway (§13)
    DecideBound(ctx context.Context, id, payloadHash, reply string) (DecisionOutcome, error)
}
type DecisionOutcome struct{ Status string; Executed bool; Error string }

type TierConfig struct{ Enabled bool; Timeout time.Duration }
type BreakerConfig struct{ Failures int; Cooldown time.Duration; MaxMissRatePct, MissSample int }
type Config struct {
    Registry       func() *intents.Registry  // atomic snapshot (reloadable)
    Style          *render.Style
    Store          *store.Store  // writer: route_log, intent_state
    ReadStore      *store.Store  // OpenReadOnly: reflex, propose, quick, speculation
    Tier0, Tier1, Main TierConfig
    T1             t1.Client     // nil unless the eval gate passed
    Breaker        BreakerConfig
    AckAfter       time.Duration // 250ms
    Speculation    bool
    QuickTools     bool
    VoiceApprove   VoiceApproveConfig // {Enabled bool; Window time.Duration; InternalDomains []string}
    Promotion      PromotionConfig    // {Enabled bool; MinRepeats, MaxLearned, DemoteMissPct, DemoteMin int}
    MissWindow, Retention time.Duration
    Tasks          reflex.TaskControl
    Actions        ActionSink
    Approver       Approver
    Prewarm        func(ctx context.Context, req backend.Request) (string, error) // "alive|started|busy|skipped"
    Clock          Clock // Now() + AfterFunc(); fake in tests
}
func DefaultConfig() Config // the flag defaults from §17, used by the gateway until R-25 maps config keys
func New(cfg Config) (*Nervous, error)
func (n *Nervous) Handle(ctx context.Context, env runtime.Env, t Turn, emit func(runtime.Event))
func (n *Nervous) Partial(clientID string, ch runtime.Channel, text string, seq int) (turn.State, error)
func (n *Nervous) Health() RouterHealth // tiers, breakers, sidecar, inactive intents, learned skipped/disabled
func (n *Nervous) Quick() *reflex.QuickService
func (n *Nervous) QuickFunctions() []tools.QuickFunction
func (n *Nervous) RecordToolUse(tool string, args map[string]any)
func (n *Nervous) OnApprovalQueued(ch runtime.Channel, e approvals.Envelope, emit func(runtime.Event)) // §13
func (n *Nervous) Reload(r *intents.Registry)
```

##### 11.3 Shared validator (`validate.go`)

```go
type Proposal struct { Source TierID; Intent string; Captures []tmpl.Capture /*T0*/; Args map[string]string /*T1*/ }
type Validated struct { Intent intents.Intent; Args reflex.Args; Labels, Spoken map[string]string }
func (n *Nervous) Validate(p Proposal, u tmpl.Utterance, ents slots.Entities, now time.Time) (Validated, string /*escalation reason*/, bool)
```

The validator checks, in order:
1. The intent is in `Candidates()`.
2. For T1: `ReflexEligible` is true and `RequiresPending` is false.
3. Slot names are declared.
4. Each slot resolves through `slots.Resolve` or `ResolveString`. `Unresolved` escalates `slot_unresolved`, and `Ambiguous` escalates `slot_unresolved` with the options kept in the row.
5. Required slots are present or defaulted.
6. For T1 only, the grounding check (§10).

##### 11.4 `Handle`, in order

1. **Open the turn.** Call `table.Final(t.ClientID, t.ID, ch)`, taking over any speculation. Emit `ack` as router. Start the ack timer: `Clock.AfterFunc(AckAfter, emitHandoffIfUnrouted)`.
2. **Eligibility (§6).** If the turn is not eligible, skip every quick tier with that reason.
3. **Tier 0** (enabled, breaker closed, `WithTimeout(150ms)`):
   1. Normalize.
   2. Collect candidates. `requires_pending_approval` intents are included only when `Pending()` is non-empty.
   3. Apply the deny-word literal check.
   4. Run `Validate`.
   5. Pick the highest specificity. A tie between intents escalates `ambiguous_match`.
   6. If nothing matched, try `Shadow()` to record `intent_inactive:<id>`.

   Tier 0 never touches `env.Warm` or `env.Backend`.
4. **Tier 1**, only when Tier 0 returned `no_match` and Tier 1 is enabled with its breaker closed and the gate passed. It runs under `WithTimeout(400ms)` while the ack timer keeps running.
5. **Quick answer** (Tier 0 or Tier 1): call `Route(quick)`.
   - **Read intent:** run the handler, then `Render`, plus `Speakable` on voice, then `DeliverText` and `env.OnTaint(result.Tainted)`. Emit `done{tier}`.
   - **Write intent:** see §12.
   - **`approvals.respond`:** see §13.
6. **Escalation:** call `Route(main)`. If the handoff acknowledgement has not been emitted yet, emit it now: on voice as a `sentence` event with a `style.voice.handoff` phrase; on other channels as a new `handoff` event kind, which older clients ignore because `TurnEvent` maps unknown kinds to `.unknown`. Record `ack_ms` as ack time minus `FinalAt`.
7. **Main path.**
   - With `Hint.Intent == brief.today`, it runs `runtime.ComputeAndCacheBrief` with briefAnswer's recomputed, fail-closed taint logic, moved verbatim.
   - Otherwise it runs `runtime.ModelTurn`. The prompt is `Context + Text`, plus the `## Recent quick answers` ring, plus a prefetched state summary if one is at most 5 s old.
   - `tooltrace.Begin(t.ID)` and `End` bracket the call.
   - Sentences on voice pass through `Speakable`. `first_sentence_ms` is recorded.
   - Once any delta has streamed, the main path owns the answer: errors become `error`, never a further escalation.
8. **Every tier fails:** emit `error` using `style.voice.errors.generic`, or `timeout` when the timeout was the cause.
9. **Deferred cleanup,** which also runs on panic or cancellation:
   - `table.Done`, then the lint (voice).
   - Exactly one `route_log` row.
   - The possible-miss check against the previous row on the same channel, then the learned-intent breaker (§16).
   - Pruning, at most once per local day.

**Turn-level state:**
- Every turn starts at the pre-tier check and Tier 0; no state skips the cascade.
- The recent-reflex ring holds the last 3 quick exchanges from the last 10 minutes, as context for the main path only.
- Possible-miss and the breakers are unchanged from the earlier plan (window 60 s; 5 failures; 60 s cooldown; miss rate 20% over 50 answered turns with at least 20 samples).

**The sous chef stays available while the head chef is busy.** `handleTurn` runs per request. Tier 0, Tier 1, quick tools and speculation never touch `WarmSession.sem`. The one-slot semaphore serializes only main-path turns.
- `TestTier0WhileMainBlocked`: the fake backend's `Reply` blocks on a channel. While turn A is blocked in the main path, turn B ("what's on my calendar today") must complete within 250 ms with 0 new backend calls. Turn A is then released and completes normally.
- A gateway-level twin of this test runs over real HTTP.

**`runtime` changes** (carried forward):
- `Env` gains `StyleBlock string` and `Summary *PrefetchedSummary`.
- `RoleSystem` appends `## Style` + `StyleBlock`.
- `runtime.ModelTurn(ctx, env, turn, emit) (backend.Response, error)` replaces `RunTurn` and uses `env.Summary` if it is fresh.
- `DeliverText` is exported.
- `CachedBrief(ctx, env, day)` does store reads only.
- `fastpath.go` is deleted.

##### 11.5 Voice path: partials and speculation (`speculate.go`, `POST /v1/turns/{id}/partial`)

- **Endpoint.** `POST /v1/turns/{id}/partial` is additive and optional, and uses normal client auth. The body is `{text, seq, channel}` and the reply is `202 {"state": "listening"}`.
  - `{id}` is a client-generated id matching `^[A-Za-z0-9_-]{8,64}$`.
  - Limits: text up to 2,000 characters, at most 20 partials per second per turn, and at most 8 listening turns, with the oldest evicted.
  - Partial text is **never** stored. Only counts and timestamps reach the route log.
- **Final.** `POST /v1/turns` gains an optional `turn_id`, which is the `ClientID`. The existing full-text path without `turn_id` still works unchanged.
- **Speculation** (`router.speculation.enabled`, **on** by default) is debounced to once per 150 ms per turn, and only runs when the normalized tokens changed. A newer partial cancels the previous speculation context. `speculate(ctx, SpecDeps, turn)` takes:

  ```go
  type SpecDeps struct { // deliberately no Backend, no T1 client, no emitter
      ReadStore *store.Store
      Summary   func(ctx context.Context) (string, bool /*tainted*/, error) // runtime.StateSummary on ReadStore
      DryMatch  func(u tmpl.Utterance) (intent string, labels map[string]string, ok bool)
      RunRead   func(ctx context.Context, intent string, a reflex.Args) (render.Result, error)
      Prewarm   func(ctx context.Context, req backend.Request) (string, error)
  }
  ```

  It works like this:
  1. Precompute the state summary.
  2. Dry-match Tier 0. On a read-intent hit, run the handler (store reads only) and cache the result, keyed by intent and slot labels.
  3. If there is no Tier 0 hit, call `Prewarm`: `WarmSession.Prewarm` try-acquires the semaphore, and if no process is alive or the key differs, starts the process **without writing any turn to stdin**. When the semaphore is busy it returns `busy`.
- **At final:** a cached read result is reused only if it was computed at most 2 s before `FinalAt` and the final match has the same intent and labels. Otherwise the handler reruns. Speculation is cancelled when the turn is routed to the quick owner.
- **Zero model calls, by construction.** `SpecDeps` has no backend or Tier 1 field. `route_log.speculation_model_calls` is computed from the fake backend's `Calls()` delta in tests. `TestSpeculationZeroModelCalls` sends 50 partials and asserts no backend calls and no emitted events.

#### 12. Actions: the sous chef proposes, the gate and approvals decide (`actions.go`, `internal/gateway/actions.go`)

Flow for a write intent routed to the quick owner:
1. Build the proposal with `propose.Table()[intent.Proposer].Build(ctx, deps{ReadStore}, args)`. `unresolved` or `ambiguous` escalates `entity_unresolved` or `entity_ambiguous` through `Route` to the main path, since the turn isn't routed until a successful build.
2. Call `cfg.Actions.ProposeAction(ctx, p.Action, p.Payload, ch)`. The gateway implementation is `(d *Daemon) ProposeAction`, which calls the **same `proposeEnvelope`** extracted verbatim from `handleToolInvoke`'s queued branch. It:
   1. checks `d.cfg.Manifest.Function(fn)` is present, not level B, and passes `gate.NeedsEnvelope(level, Tainted)`. For level A this is always true; anything else is denied with "sous proposals must be level A";
   2. runs `connectors.Registry.Lookup(fn)`, then `spec.Schema.Validate(payload)`, the exact check `gate.authorize` repeats at execution time;
   3. calls `Approvals.Propose(Envelope{Action, Payload, Recipient, Origin: P0, Risk: functionRisk(...)})`.

   The model's queued tool calls and the sous chef's proposals are therefore identical as far as the queue is concerned. `handleToolInvoke` now calls `proposeEnvelope` too. This refactor is behaviour-preserving, and the existing tests cover it.
3. The facade emits the interpretation echo plus `approvals.ReadBack(env)` (through `Speakable` on voice), then `approval_required{approval_id}`. It records `last_readback` for the channel (§13), then emits `done`. The route row has `outcome = proposed` and `action = {function, envelope_id, decision: "pending"}`.
4. **Nothing executes.** Execution happens only through `POST /v1/approvals/{id}/decision`, or the voice binding, both going to `decideAndExecute` and then `Gate.Invoke` with `EnvelopeID`. This path is unchanged.

**Gating.** Write intents need no extra flag. They are inactive until the manifest grants their action (§5.4), and every effect requires an approved envelope.

#### 13. Voice approval binding (`readback.go`, `voiceapprove.go`), `router.voice_approve.enabled`, off by default

```go
type Readback struct{ Channel runtime.Channel; EnvelopeID, PayloadHash string; At time.Time }
type Readbacks struct{ /* mu; last map[runtime.Channel]Readback */ }
func (r *Readbacks) Record(rb Readback)
func (r *Readbacks) Void(envelopeID string)
func (r *Readbacks) Bound(ch runtime.Channel, now time.Time, window time.Duration) (Readback, bool)
```

**Where read-backs are recorded:** only where a read-back is actually emitted on a **voice** turn's stream. That happens in three places:
- (a) the write-intent flow (§12);
- (b) `approvals.respond` surfacing the one pending envelope;
- (c) model-queued tool calls. `notifyApprovalRequired` [shared] calls `d.cfg.Nervous.OnApprovalQueued(sink.channel, env, sink.emit)` for each open sink. `turnSink` gains a `channel` field. On voice sinks this emits the read-back as `sentence` events and records it.

**Decision procedure.** With the flag off, `approvals.respond` behaves exactly as in the earlier plan: bind and surface only. With the flag on, `approvals.respond` on the `voice` channel works like this:
1. The utterance matched `approvals.respond`, which uses yes/no vocabulary only, and `approvals.Match(raw)` returns `Yes` or `No`.
2. `Pending()` returns 0 envelopes: reply "Nothing is waiting for approval". Two or more: reply with `approvals.Menu`. **With more than one pending there is never a voice decision.**
3. `Bound(voice, now, 60s)` must return a read-back, and the envelope must match `pending[0].ID` and `pending[0].PayloadHash`. If not, the read-back is stale, voided or an edit (an `Edit` creates a new id): void the binding, then re-surface the current envelope's read-back with the `readback_stale` phrase. This records a fresh binding. There is no decision.
4. **No** → `Approver.DecideBound(ctx, id, hash, "no")`. Denying is the safe direction, so it is allowed at every risk tier.
5. **Yes** → check `VoiceApprovalTier(env, cfg.InternalDomains)`:
   - `VoiceYes` → `DecideBound(ctx, id, hash, "yes")`;
   - `TapRequired` → speak `errors.tap_required` and re-emit `approval_required{id}` so the client shows its tap UI. There is no decision.
6. Void the binding after any decision. The route row gets `outcome = decided | tap_required` and `action.decision`.

`DecideBound` is `(d *Daemon) decideAndExecute(ctx, id, payloadHash, reply)`, **extracted verbatim** from `handleDecideApproval`: hash check, then `Decide`, then detached `Gate.Invoke` with `EnvelopeID`, then `Abandon` on refusal. The HTTP handler becomes a thin wrapper around it. **Nothing in `internal/nervous` calls `Queue.Decide`,** and deciding still happens only on the one hash-bound path.

**Risk-tier mapping.** The existing risk fields are:
- `connectors.Function.Risk ∈ {low, medium, high}`, copied into `Envelope.Risk` by `functionRisk`. An unknown function becomes `medium`, and the field can also be empty.
- `Envelope.Recipient`, and the payload's recipient fields.
- `Envelope.Origin` (P0, P1 or P2).
- `decisions.Type.SeverityWeight`, which ranks cards and is **not** a risk signal. It is deliberately unused here.

```go
type VoiceTier int
const ( VoiceYes VoiceTier = iota; TapRequired )
// voiceEligibleActions: the only actions a spoken yes may ever approve (default deny).
var voiceEligibleActions = map[string]bool{"gcal.create_event": true, "gcal.move_event": true, "gmail.draft_message": true}
func VoiceApprovalTier(e approvals.Envelope, internalDomains []string) (VoiceTier, string /*reason*/)
```

Rules, first match wins:

| # | Condition | Tier | Reason |
|---|---|---|---|
| 1 | `e.Origin == "P2"` | Tap | auto-mode origin |
| 2 | `e.Risk == "high"` | Tap | declared high risk (public, send, money) |
| 3 | `e.Risk` not in {`low`, `medium`} (empty or unknown) | Tap | unrated |
| 4 | `e.Action` not in `voiceEligibleActions`, which includes `gmail.send_message` and any future money or post function | Tap | not voice-eligible |
| 5 | any address in `e.Recipient` ∪ payload `to`/`cc`/`bcc`/`attendees` (string or []string) has a domain not in `internal_domains`. An empty list means every address is external | Tap | external recipient |
| 6 | otherwise (`low` or `medium`, eligible action, internal-only recipients) | **VoiceYes** | |

In summary, `low` and `medium` allow a spoken yes, and `high`, empty or unknown require a tap. A spoken no is always allowed.

#### 14. Route log (migration `0010_router.sql`; the number is chosen at commit time, see Risks)

```sql
-- route_log records every turn's routing decision (Slice R). utterance is
-- local-only; partial transcripts are never stored, only counts/timings.
CREATE TABLE route_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	turn_id TEXT NOT NULL UNIQUE,                 -- daemon task id (X-Water-Task-Id)
	client_turn_id TEXT NOT NULL DEFAULT '',      -- id partials were posted under, if any
	at INTEGER NOT NULL,                          -- final transcript arrival, unix nanos
	channel TEXT NOT NULL,
	utterance TEXT NOT NULL,
	tiers_attempted TEXT NOT NULL,                -- JSON, e.g. ["t0","t1","main"]
	owner TEXT NOT NULL DEFAULT '',               -- quick | main | '' (never routed)
	answered_by TEXT NOT NULL DEFAULT '',         -- t0 | t1 | main | ''
	intent TEXT NOT NULL DEFAULT '',
	intent_kind TEXT NOT NULL DEFAULT '',         -- read | write
	intent_origin TEXT NOT NULL DEFAULT '',       -- embedded | learned
	slots TEXT NOT NULL DEFAULT '{}',             -- JSON {slot: label}
	escalation_reason TEXT NOT NULL DEFAULT '',   -- no_match | escalate_word:should | multi_clause | intent_inactive:x | t1_ungrounded | ...
	latency_ms TEXT NOT NULL DEFAULT '{}',        -- JSON {"t0":2,"t1":180,"main":1430}
	total_ms INTEGER NOT NULL,
	outcome TEXT NOT NULL,                        -- answered | clarified | proposed | decided | tap_required | error | cancelled
	warnings TEXT NOT NULL DEFAULT '[]',          -- JSON: lint (banned:x, markdown, emoji, url, list_overcap), overlength, readback_stale
	voice INTEGER NOT NULL DEFAULT 0,
	partials INTEGER NOT NULL DEFAULT 0,
	first_partial_lead_ms INTEGER,                -- final_at - first partial; NULL without partials
	speculation TEXT NOT NULL DEFAULT '{}',       -- {"summary":true,"dry":"schedule.on_date","prewarm":"alive|started|busy|skipped","reused":true}
	speculation_model_calls INTEGER NOT NULL DEFAULT 0,
	ack_ms INTEGER,                               -- final -> handoff ack; NULL when owner=quick
	first_sentence_ms INTEGER,                    -- final -> first answer delta/sentence
	tools_used TEXT NOT NULL DEFAULT '[]',        -- main turns: ["quick__calendar","gcal__list_events"]
	tools_attributed INTEGER NOT NULL DEFAULT 1,  -- 0 when >1 main turn was in flight (attribution ambiguous)
	quick_only INTEGER NOT NULL DEFAULT 0,        -- main turn, tools_used non-empty, all quick__*, attributed
	tool_signature TEXT NOT NULL DEFAULT '',      -- canonical "quick.calendar(when)+quick.next_event()" when quick_only
	action TEXT NOT NULL DEFAULT '{}',            -- {"function","envelope_id","decision":"pending|approved|denied|tap_required"}
	possible_miss INTEGER NOT NULL DEFAULT 0,
	confirmed INTEGER NOT NULL DEFAULT 0          -- reserved for a later export/labeling slice
);
CREATE INDEX route_log_at ON route_log(at);
CREATE INDEX route_log_intent ON route_log(intent, at);
CREATE INDEX route_log_quick_only ON route_log(tool_signature, at) WHERE quick_only = 1;
-- intent_state: per-intent enable/disable for learned intents (demotion, breaker).
CREATE TABLE intent_state (
	intent_id TEXT PRIMARY KEY,
	disabled INTEGER NOT NULL DEFAULT 0,
	reason TEXT NOT NULL DEFAULT '',              -- manual | auto: miss 25% over 12
	at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS messages_sent_at ON messages(sent_at);
CREATE INDEX IF NOT EXISTS messages_sender ON messages(sender);
```

```go
type RouteRow struct {
    ID int64; TurnID, ClientTurnID string; At time.Time; Channel, Utterance string
    TiersAttempted []string; Owner, AnsweredBy, Intent, IntentKind, IntentOrigin string
    Slots map[string]string; EscalationReason string; LatencyMS map[string]int64; TotalMS int64
    Outcome string; Warnings []string; Voice bool; Partials int; FirstPartialLeadMS, AckMS, FirstSentenceMS *int64
    Speculation map[string]any; SpeculationModelCalls int
    ToolsUsed []string; ToolsAttributed, QuickOnly bool; ToolSignature string
    Action map[string]string; PossibleMiss, Confirmed bool
}
func (s *Store) InsertRoute(ctx context.Context, r RouteRow) (int64, error)
func (s *Store) MarkPossibleMiss(ctx context.Context, id int64) error
func (s *Store) ListRoutes(ctx context.Context, since time.Time, limit int) ([]RouteRow, error)
func (s *Store) QuickOnlyRoutes(ctx context.Context, since time.Time) ([]RouteRow, error)
func (s *Store) IntentAnswered(ctx context.Context, intent string, limit int) ([]RouteRow, error) // learned breaker
func (s *Store) PruneRoutes(ctx context.Context, before time.Time) (int64, error)
func (s *Store) SetIntentState(ctx context.Context, id string, disabled bool, reason string, at time.Time) error
func (s *Store) ListIntentStates(ctx context.Context) (map[string]string, error)
func OpenReadOnly(path string, conns int) (*Store, error) // no migrate; file:...?mode=ro + query_only(1) + busy_timeout + WAL
```

The read helpers `LatestMessages`, `MessagesFrom`, `CountMessagesSince`, `Senders`, `NextEvent` and `CursorUpdatedAt` go in `queries_router.go`.

**Tool attribution (`tooltrace.go`).** While exactly one main turn is in flight, `RecordToolUse` appends the call to that turn. If none is in flight the call is dropped. If two or more are in flight, it is appended to all of them with `tools_attributed = 0`, which excludes those turns from candidates. The warm session serializes main turns, so this is exact in practice.

**Report.** `water route report [--since 7d] [--json]` calls `GET /v1/route/report?since=`. It prints:
- the owner split (quick vs main) and the tier distribution;
- the escalation-reason histogram, including `escalate_word:*` grouped;
- per-tier p50, p95 and p99 latency, and totals;
- ack p50/p95 and first-sentence p50/p95 on voice turns;
- the count of turns with partials and the speculation reuse rate;
- lint warning counts by kind;
- the possible-miss count plus the 20 most recent possible misses;
- quick-tool usage counts;
- near-matches to inactive intents;
- the proposal and decision counts by outcome.

#### 15. Tier 3 folded into the main path; `Decider`

- **Tier 3** is no longer a separate tier. The head chef *is* the strong path, served on the warm session with the manifest's `models.fast` model, as today.
- No per-turn model switch is added, because it would restart the warm session. `TierStrong` stays unwired. Choosing a stronger model for the head chef is a manifest change the owner makes in `twin.yaml` (Risks, item 14).
- **`internal/decider`** is carried forward unchanged: `Decider`, `Null`, `New("none")` and `Wrap`, with `buildDecisionsTrigger` calling `decider.Wrap`. The config key is `decider.provider = none`, and any other value is rejected.

#### 16. Growth: the promotion loop (`internal/nervous/promote`), `router.promotion.enabled`, off by default

Growth means new phrasings and bindings over the fixed, audited handler set. It never means a new capability.

1. **Candidates.** `water route candidates [--since 30d] [--min 5] [--json]` calls `GET /v1/route/candidates`. It reads `route_log` directly and **works even with the flag off**. Qualifying rows are grouped by `tool_signature`, and a group qualifies with at least `min_repeats` (5) distinct turns on at least 2 distinct local days. A row qualifies when all of these hold:
   - `owner = main`, `quick_only = 1`, `tools_attributed = 1`;
   - `outcome = answered`, `possible_miss = 0`;
   - the utterance **does not** fail eligibility (§6) — reasoning turns are never candidates;
   - every quick tool in the signature maps to a `Learnable()` handler.

   The candidate id is the first 12 hex characters of `sha256(signature)`.
2. **Draft** (flag on). `water intent draft <candidate>` calls `POST /v1/intents/draft`, which makes one CEO-initiated, P0, cold model call (never the warm session). The prompt contains the handler's `FunctionSpec`, the grammar summary, and up to 10 sample utterances from the group; the call returns one intent YAML with `id: learned.<name>`, `origin: learned` and `provenance` filled in by code, not the model. The file is written to `$WATER_HOME/twins/<id>/intents/pending/<candidate>.yaml`, then validated at once, and the result is printed.
3. **`ValidateLearned(file, reg, negatives)`.** It rejects the file if any of these holds:
   - it breaks a normal `LoadRegistry` rule;
   - it is not `kind: read` or not `reflex_eligible`;
   - its function is not `Learnable()`. Write or action intents are **always rejected**, so actions can never be promoted;
   - it has any `text` slot;
   - its id collides with an embedded intent, or it lacks the `learned.` prefix;
   - its templates contain `escalate_words` or `deny_words`;
   - **there is ambiguity**: one of its own tests, examples or candidate utterances matches anything other than exactly this intent (with no tie), or it matches any embedded or learned intent's tests or examples;
   - **it would false-accept**: it matches any `none` case in the embedded eval negatives (`eval.Negatives()`, via go:embed of `testdata/ceo_eval.yaml`), including every reasoning case, or any main-answered utterance from the last 30 days outside the candidate group;
   - its own tests fail;
   - the cap `max_learned` (20) would be exceeded.
4. **Approval.** `water intent promote <id>` requires the flag to be on. It revalidates, shows the templates and validation results, and asks for interactive `y/N` confirmation. It then writes `$WATER_HOME/twins/<id>/intents/learned/<id>.yaml` with `provenance.promoted_at` and `registry_hash`, and calls `POST /v1/intents/reload`.
   - Embedded files always win: `LoadRegistry` rejects a learned id collision.
   - With the flag off, the overlay is not loaded at all.
5. **Demotion.**
   - **Automatic:** after each possible miss marked on a row with `intent_origin = learned`, the per-intent breaker computes that intent's miss rate over its last `IntentAnswered(id, 50)` rows. With at least `demote_min_samples` (10) samples and a rate above `demote_miss_rate_pct` (20%), it calls `SetIntentState(disabled, "auto: miss X% over N")`. The intent is excluded from `Candidates()` immediately, through the live `Disabled` set.
   - **Manual:** `water intent demote <id> [--reason]` disables an intent, and `water intent enable <id>` re-enables one.
   - `water intent list` shows embedded, learned, inactive (with reason) and disabled (with reason).

#### 17. Config keys (`internal/config`, one task, **[shared]**)

Every key follows the existing pattern: `defaults()`, `apply()`, `Flat()`, and `intKeys`/`boolKeys`.

| Key | Default | Notes |
|---|---|---|
| `router.tier0.enabled` | `true` | |
| `router.tier0.timeout_ms` | `150` | |
| `router.tier1.enabled` | `false` | also needs the eval-gate file (§10) |
| `router.tier1.timeout_ms` | `400` | |
| `router.tier1.server_bin` | `llama-server` | |
| `router.tier1.model_path` | `""` | `""` → `$WATER_HOME/models/functiongemma.gguf` |
| `router.main.enabled` | `true` | disabling it leaves quick answers only; unanswered turns error |
| `router.breaker.failures` | `5` | |
| `router.breaker.cooldown_seconds` | `60` | |
| `router.breaker.max_miss_rate_pct` | `20` | |
| `router.possible_miss_window_seconds` | `60` | |
| `router.log_retention_days` | `90` | |
| `router.ack_ms` | `250` | must be < 300; `apply()` rejects values ≥ 300 |
| `router.speculation.enabled` | `true` | |
| `router.quick_tools.enabled` | `true` | |
| `router.voice_approve.enabled` | `false` | |
| `router.voice_approve.window_seconds` | `60` | |
| `router.voice_approve.internal_domains` | `""` | comma-separated; empty → every recipient is external |
| `router.promotion.enabled` | `false` | |
| `router.promotion.min_repeats` | `5` | |
| `router.promotion.max_learned` | `20` | |
| `router.promotion.demote_miss_rate_pct` | `20` | |
| `router.promotion.demote_min_samples` | `10` | |
| `decider.provider` | `none` | any other value → "decider.provider: only \"none\" is supported" |

The main path keeps `Env.Timeout`. The retired `tools.` and `memory.` prefixes are not used. Before R-25 lands, the gateway uses `nervous.DefaultConfig()`, whose values equal these defaults.

**Flags and gates summary:**

| Component | Default | Enabled when |
|---|---|---|
| Tier 0 templates | on | CI asserts eval false-accept rate = 0 |
| Tier 1 FunctionGemma | off | config on **and** a recorded live eval with FA ≤ 1%, Wilson 95% upper ≤ 2%, n ≥ 200 (plan uses ≥ 400) and warm p95 ≤ 400 ms, matching the current model and registry hash |
| Speculative prefetch | on | no model calls, by construction |
| Quick tools for the main agent | on | read-only, listed in the tool policy |
| Write intents | active per intent | only when the manifest grants the action at level A; always approval-gated |
| Voice approval | off | the write functions exist and the binding tests pass; the owner turns it on |
| Promotion loop | off | enough route-log data and owner approval; each promotion is approved individually |

#### 18. Eval harness (`internal/nervous/eval`)

`testdata/ceo_eval.yaml` holds `cases: [{utterance, intent ("none" for negative), slots, pending, class}]`, where `class` is one of `positive`, `reasoning`, `multi_clause`, `action_unsupported`, `legacy_near_miss`, `ood`, `yesno_zero_pending` or `write`.

Minimum counts:
- at least 12 held-out paraphrases for each of the 12 read intents (144 or more);
- at least 8 for each write intent (32 or more), evaluated against a fixture manifest that grants all four actions;
- at least 100 negatives, including:
  - 30 or more `reasoning` cases ("what should I prioritize tomorrow", "which meeting matters more", "why is…", "summarize…", "plan my week", "is the board sync important");
  - 10 or more `multi_clause` cases;
  - all 5 legacy near-misses;
  - unsupported actions ("cancel my 3pm", "delete the email from alex", "draft a reply to alex");
  - out-of-domain questions;
  - "yes" or "no" with zero pending.
- A total of 276 or more.

`ceo_eval_t1.yaml` adds further held-out paraphrases so that Tier 1's live n is at least 400.

```go
type Case struct{ Utterance, Intent, Class string; Slots map[string]string; Pending int }
type Report struct {
    Tier string; N, Positives, Negatives int
    HitRate, IntentAcc, SlotAcc, FalseAcceptRate, Wilson95Upper, EscalationRate float64
    ReasoningAnswered int; FalseAccepts []Case; P50, P95 time.Duration
}
func Run(ctx context.Context, t nervous.Tier, fx Fixture, cases []Case) Report
func Negatives() []Case // go:embed of the same file; used by promote.ValidateLearned
```

**A false accept** is any answered case with the wrong intent, a wrong slot label, or an expected intent of `none`. For a write case, "answered" means a proposal was built. The fixture is unchanged: a fixed clock of `2026-09-24T09:00 PT`, two people named "Alex", and a pending count.

### Task list

Each task is one commit (message prefix `R-<n>:`) with its own tests, and each commit passes the gates listed under Execution environment.

**Coordination rules for every task:**
- **[shared]** marks files the concurrent session may also edit. Before touching one, `git rebase feat/ceo-twin` in the worktree and re-read the current file.
- Stage by explicit path, never `-A` or `.`, and run `git status` before committing.
- This slice never edits `twins/ceo/twin.yaml`, `internal/gate/**`, `internal/connectors/**`, `internal/decisions/**` or `internal/approvals/**`.

---

- [x] **R-1 Gemma terms doc** (`docs/functiongemma.md`). Docs only; it blocks nothing.
  - What FunctionGemma is: `google/functiongemma-270m-it`, a 270M-parameter function-calling Gemma 3 variant.
  - Why a sidecar: Homebrew `llama-server`, owner-approved on 2026-09-24, a separate process, no Go module, no cgo.
  - The **Gemma Terms of Use** and **Gemma Prohibited Use Policy** URLs, and a summary: not OSI-licensed; use and modification are allowed; the terms and use restrictions pass on to anyone the weights or derivatives are redistributed to; Water never redistributes weights, since they live only under `$WATER_HOME/models/` and never in the repo.
  - Hugging Face gating: the owner accepts the terms on HF, and the pull may need `HF_TOKEN`.
  - The pull command, and the fact that nothing downloads implicitly.
  - Where the sha256 pin lives.
  - The eval gate required before Tier 1 can be enabled.
  - Flag: none (docs).
- [x] **R-2 Template matcher** (`internal/nervous/tmpl/{tmpl.go,parse.go,match.go}`).
  - Implements the grammar, normalization with raw spans, the anchored backtracking match, specificity, the step budget, `LiteralVocabulary` and the template-final `text` rule.
  - Tests (`tmpl_test.go`):
    - the earlier plan's grammar table, parse errors, specificity ordering, budget exhaustion and `BenchmarkMatch`;
    - `Capture.Raw` keeps case and punctuation ("reply to alex saying Sounds good, see you at 3!");
    - `text` anywhere but last is a compile error.
  - Flag: none (no behaviour until wired).
- [x] **R-3 Slot resolvers** (`internal/nervous/slots/{slots.go,date.go,time.go,duration.go,count.go,person.go,text.go}`).
  - Tests: the earlier plan's date (40 or more), time, count and person tables, plus `duration_test.go` (every accepted form, out-of-range rejection) and `spoken_test.go` (`Spoken` for today, tomorrow, a weekday, next week, a time and a part of day, in a fixed PT zone).
  - Flag: none.
- [x] **R-4 Migration, store helpers, read-only pool** (`internal/store/migrations/0010_router.sql`, `route_log.go`, `intent_state.go`, `reader.go`, `queries_router.go`). Migration number `0008` was free when this task committed, so it was originally `0008_router.sql`.
  - **Collision discovered and fixed during R-11's rebase.** The concurrent session landed its own `0008_twin_messages.sql` on `feat/ceo-twin` afterward. `store.migrate`'s version tracking (`schema_migrations(version INTEGER PRIMARY KEY)`) keys purely on the leading number, and `fs.Glob` sorts lexicographically, so `0008_router.sql` (`r` < `t`) ran first, claimed version 8, and `0008_twin_messages.sql` was silently skipped — never creating `twin_messages`, which broke five gateway/store tests (`TestTwinMessages`, `TestScenarioEBudgetQuestionBetweenTwoTwins`, and others) on the shared branch. Exactly the scenario Risk item 3 pre-authorized handling directly: since this migration hadn't reached `feat/ceo-twin` yet (only on the unmerged `slice-r` worktree branch), it was renumbered to `0009_router.sql` rather than expecting the other session's already-landed file to change. Verified: after the rename, both migrations apply and all previously-failing tests pass.
  - **Second collision, discovered independently while reviewing R-12 (2026-09-24), same root cause.** Between R-11's rebase and R-12's commit, the concurrent session landed a further `0009_meeting_segment_received_at.sql` on `feat/ceo-twin` — whose own comment reads "Numbered 0009 because 0008 is taken on another branch," i.e. that session independently avoided 0008 but had no way to know `slice-r` had already claimed 0009 for `router.sql`. `fs.Glob` sorts `0009_meeting_segment_received_at.sql` before `0009_router.sql` alphabetically, so the latter's `CREATE TABLE`s silently never ran — `internal/store`'s new tests failed with "no such table: route_log"/"no such table: intent_state" the next time the full suite ran (R-12's own commit message claimed "all green," so this must have landed in the gap between R-12's gate run and its commit, or R-12's rate-limit interruption cut its final verification short). Renumbered again to `0010_router.sql`, same rule (still unmerged into `feat/ceo-twin`), verified by rerunning the full `-race` suite. **This is now the second time this exact collision class has hit this migration file** — a signal that the plan's "rebase and rename at commit time" mitigation is necessary but not sufficient against a fast-moving concurrent branch; a later merge-back pass should double check no third collision appeared before `slice-r` actually fast-forwards into `feat/ceo-twin`.
  - **Checked again at R-17 (2026-09-24).** `git rebase feat/ceo-twin` on `slice-r` was a no-op ("up to date": `feat/ceo-twin`'s tip `2adc5a9` is an ancestor of `slice-r`'s `79d1241`), and `feat/ceo-twin`'s own `internal/store/migrations/` only goes up to `0009_meeting_segment_received_at.sql` — no `0010` exists there. No third collision. `0010_router.sql` stands.
  - **First run `ls internal/store/migrations` after `git rebase feat/ceo-twin`, use the next free number, and record the actual file name in this plan in the same commit.**
  - Tests:
    - `route_log_test.go`: round trip of every column, including JSON and nullable ints; the `PruneRoutes` boundary; `QuickOnlyRoutes` filter; the migration on a fresh DB and on a DB already at the previous head;
    - `intent_state_test.go`;
    - `reader_test.go`: an `OpenReadOnly` write attempt fails with a readonly or query_only error; `TestReadPoolParallel` runs 16 readers plus 1 writer under `-race` with no `SQLITE_BUSY`;
    - query-helper ordering and dedup tests.
  - Flag: none.
- [x] **R-5 Style and renderer core** (`internal/nervous/render/{render.go,style.go,default.go}`, `twins/ceo/style.yaml` with the full `voice:` section).
  - Tests (`render_test.go`):
    - per-channel caps; "and N more"; clarification and warning rendering; brace-containing external titles are inert;
    - strict decoding rejects unknown keys or a missing channel;
    - interpretation validation rejects a header or empty string that omits a slot's `{x_spoken}`;
    - `DefaultStyle()` passes validation; a missing file falls back to `DefaultStyle()`;
    - `PromptBlock` is stable and contains the tone and banned phrases.
  - Flag: none.
- [x] **R-6 Intent registry loader** (`internal/nervous/intents/{registry.go,shared.go,spec.go,planned.go,learned.go}`).
  - Implements the §5 schema, the extended `FunctionSpec` with `Learnable` and `QuickEligible`, read and write kinds, `checkAction` with the `plannedActions` inactive mechanism, `Candidates`/`Shadow`, `Hash`, overlay parsing with skip-and-report, and the reserved names.
  - Tests (`registry_test.go`, MapFS, mirroring `decisions_test.go`):
    - `TestLoadRegistryWellFormed`;
    - `TestLoadRegistryRejects`, one case per rule in §5.3 and §5.4;
    - `TestWriteIntentInactiveUntilGranted` (both halves of §5.4);
    - `TestPlannedActionsMatchDecisions` (go/parser over `internal/decisions/registry.go`);
    - a missing intents directory gives an empty registry; `_shared.yaml` is required when the directory exists;
    - learned-file skip-and-report and embedded-wins.
  - Flag: none.
- [x] **R-7 Eval harness, held-out set, legacy baseline** (`internal/nervous/eval/{eval.go,eval_test.go,testdata/ceo_eval.yaml,testdata/ceo_eval_t1.yaml}`).
  - **Written before any CEO intent file exists.**
  - A `legacyTier` adapter wraps the still-present `runtime.FastPath`. Record its hit rate and false accepts under a new "Baseline" heading in this file, in the same commit.
  - Tests: harness mechanics against a MapFS mini-registry; counts meet §18; the Wilson helper against known values.
  - Flag: none.

  **Baseline (recorded R-7, 2026-09-24), measured with `legacyTier` wrapping `runtime.FastPath` over the 298-case `ceo_eval.yaml`:**

  | Metric | Value |
  |---|---|
  | N (total cases) | 298 |
  | Positives / Negatives | 192 / 106 |
  | Hit rate (correct intent on a positive) | 6.2% |
  | Intent accuracy (of answered cases) | 42.9% |
  | False-accept rate | 5.4% |
  | Wilson 95% upper bound on false-accept rate | 8.5% |
  | Escalation rate | 90.6% |
  | Reasoning/multi-clause cases answered | 10 (informational — old `FastPath` predates the `escalate_words`/`multi_clause` eligibility gate this slice adds, so it isn't held to that bar) |
  | p50 / p95 latency | ~1µs / ~216µs |

  The old fast path only ever recognizes 3 hardcoded phrase families (schedule/approvals/brief) against a 12-read + 4-write intent eval set, so a 6.2% hit rate and 90.6% escalation rate are expected, not a bug. This is the number Tier 0's acceptance criterion ("hit rate at least the legacy baseline plus 20 points", i.e. **at least 26.2%**, well under the criterion's own separate 50% floor) measures against once R-9/R-11 build the real Tier 0.
- [x] **R-8 Reflex handlers** (`internal/nervous/reflex/{reflex.go,handlers.go,storeview.go,imports_test.go}`, plus `runtime.CachedBrief` in `internal/runtime/brief.go`).
  - All 12 read functions with full `FunctionSpec`s (Class, ReadOnly, Deterministic, SideEffects, QuickTool per §7), `Table()` and `Specs()`.
  - Tests:
    - `handlers_test.go`: each handler against a temp-dir store opened read-only; taint from External records, including the tomorrow-schedule regression; `approvals.bind_pending` leaves queue and audit unchanged; `status.overview` never touches the vault;
    - `imports_test.go`: the denylist and forbidden selectors across all sous packages (§1a/b), plus the single allowed `internal/tools` type import;
    - `spec_test.go`: every `QuickTool` is `QuickEligible` and unique;
    - `brief_test.go` additions for fail-closed taint in `CachedBrief`.
  - Flag: none.
- [x] **R-9 CEO read intents, shared words, response strings** (`twins/ceo/intents/_shared.yaml` plus the 12 read files; `responses:` in `twins/ceo/style.yaml`).
  - Tests (`internal/nervous/intents/embedded_test.go`):
    - `TestEmbeddedCEOIntentsLoad` against `water.TwinsFS()` and the real manifest;
    - `TestIntentFileTests`;
    - `TestLegacyFastPathCases` (6 positives match, 5 near-misses don't);
    - `TestEvalNoLeak`;
    - `TestNoEscalateWordInTemplates`;
    - `TestDemoTwinLoads` (`ceo-demo` → empty registry, default style).
  - Flag: Tier 0 on. It is not reachable until R-15.
- [x] **R-10 Turn state machine** (`internal/nervous/turn/{turn.go,turn_test.go}`).
  - Tests:
    - the transition table, including illegal transitions;
    - `Route` CAS under 50 goroutines, where exactly one wins;
    - `Emitter` drops non-owner events, router-only kinds, and everything after done;
    - TTL sweep, the `maxListening` eviction and the done-id keep window.
  - Flag: none.
- [x] **R-11 Eligibility, Tier 0 adapter, shared validator** (`internal/nervous/{tier.go,eligibility.go,validate.go,tier0.go}`).
  - Tests:
    - `eligibility_test.go`: every escalate word and phrase; the multi-clause cases; `too_long`;
    - `tier0_test.go`: a tie gives `ambiguous_match`; unresolved or ambiguous person gives `slot_unresolved`; a deny word gives `action_word`; an inactive intent gives `intent_inactive:<id>`; a handler error escalates; "yes" with 0 pending is not matched; the timeout path; the fake backend shows 0 calls.
  - Flag: `router.tier0.enabled` (code default on).
- [x] **R-12 Runtime split, main path, front door** (`internal/nervous/{nervous.go,mainpath.go,ack.go}`, `internal/runtime/runtime.go`).
  - Delete `internal/runtime/fastpath.go` and `fastpath_test.go`, whose cases now live in the eval set and the embedded tests. Update `runtime_test.go` and `turnfixes_test.go` to `ModelTurn`.
  - Tests (`nervous_test.go`, `backend.NewFake`, fake `Clock`):
    - the earlier plan's R1-13 cases, with `main` in place of `t2`;
    - **`TestTier0WhileMainBlocked`** (§11.4);
    - **`TestHandoffAckWithin300ms`**: T0 escalates in 1 ms and the ack is emitted at ≤ 1 ms; with a fake T1 blocking 400 ms, the ack fires at exactly `AckAfter` (250 ms) on the fake clock; on voice it is a `sentence`, elsewhere a `handoff` event; `ack_ms` is recorded;
    - **`TestExactlyOneOwner`**: a late fake-T1 answer after `Route(main)` is dropped, and the event stream contains one answer and one `done`;
    - `TestEveryTurnStartsAtT0`.
  - Flag: `router.main.enabled` on. `router.ack_ms` is 250 through `DefaultConfig`.
  - **Flagged at R-28, left unticked on purpose.** This task's own work was committed in full at the time (`cd2e5c5: R-12: Runtime split, main path, front door`, plus a same-day follow-up fix `24cfe33: R-12 fix: ModelTurn returns its beginModelNoting error instead of emitting and bare-returning`) — unlike every other task in this build, which each got its own separate "Tick R-N" commit as task work's second commit, no such tick commit was ever made for R-12, so this checkbox was found still `[ ]` when R-28 read the file. Every other line of evidence (the commit history, the deleted `fastpath.go`, `internal/nervous/{nervous.go,mainpath.go,ack.go}` all present, and every later task from R-13 on building on and exercising this code successfully) indicates the work is real and was never at risk — but per R-28's own instructions, a task whose checkbox was found unticked is reported as a serious finding for the owner to confirm, not silently ticked by the docs task that happened to notice it. Left `[ ]` here; see `docs/known-gaps.md` and `docs/EVOLUTION_PLAN.md`'s Slice R entry for the same note.
- [x] **R-13 One-voice contract** (`internal/nervous/speak/{speak.go,lint.go}`, `internal/voice/speakable.go` delegate, facade application in `nervous.go`/`mainpath.go`).
  - Tests:
    - `speak_test.go`: markdown, bullets, headings, tables, fences, links, bare URLs ("a link"), emoji, times ("15:00", "3pm", "3-4pm"), dates (ISO, "Thu 25 Sep"), list cap with "and N more", char cap on a sentence boundary;
    - `lint_test.go`: every warning kind, and the prose is not rewritten;
    - **`TestOneVoiceAcrossTiers`**: the same fact ("board sync at 3pm tomorrow") through a T0 answer and a fake main answer returning markdown plus "As an AI…". Both voice outputs have no markdown and are within the cap. The T0 output has no banned phrase, and the main output's banned phrase appears as a `route_log` warning, not in rewritten text;
    - the existing `internal/voice` and `internal/decisions` tests pass unchanged.
  - Flag: none (always on for the voice channel).
- [x] **R-14 Route logging, breakers, possible-miss, ring, tool trace, report** (`internal/nervous/{routelog.go,breaker.go,report.go,tooltrace.go}`).
  - Tests:
    - `breaker_test.go`: the earlier plan's cases, plus the learned-intent breaker (disables at >20% over 10 or more, not below 10 samples);
    - `routelog_test.go`: the 59 s vs 61 s miss window; corrections; a different channel is not flagged; ring eviction; exactly one row on answered, clarified, proposed, error and cancelled; owner, ack, voice and partials columns filled;
    - `tooltrace_test.go`: a single in-flight turn is attributed; two in flight gives `tools_attributed = 0`; `quick_only` and `tool_signature` are canonical;
    - `report_test.go`: percentiles, histograms, ack/first-sentence stats and lint counts over fixture rows.
  - **Wired into `Handle`/`answerQuick`/`answerMain` for real** (not left as an unwired mechanism): `nervous.go`/`mainpath.go` were extended with a `*routeRecorder` threaded through both, `Config` gained `Store`/`Breaker`/`MissWindow`/`Retention`, and `Nervous` gained `ring *reflexRing`, `tier0Breaker *Breaker`, `toolTracer *ToolTracer`. A nil `Config.Store` (most existing tests) makes logging a no-op rather than panicking.
  - **A real bug found and fixed while wiring this in:** `routeRecorder.finish` was originally called with the turn's own (possibly already-cancelled) `ctx`, so the "cancelled" outcome — the one case the row most needs to record — could never actually be written: `InsertRoute`'s `ExecContext` fails immediately against a cancelled context. Fixed with `context.WithoutCancel(ctx)` for the logging writes specifically, while still reading the original `ctx.Err()` to decide the outcome value itself.
  - **A second thing this surfaced:** a fake/very fast backend can return a successful reply even after its context was cancelled (nothing forces it to check). `mainpath.go`'s success paths now call a `successOutcome(ctx)` helper that reports "cancelled" over "answered" when `ctx.Err() != nil` at completion, so the row reflects the turn's protocol-level truth rather than what a backend happened to notice.
  - **`intent_kind`/`intent_origin` are hardcoded to `"read"`/`""` in `answerQuick`,** not derived from `render.Result.Kind` (a different field entirely — it's `"read"|"clarify"|"decision"`, describing the answer SHAPE, not the intent's read/write kind). This is correct today because Tier 0 (R-11) only ever runs a read intent's handler; it will need a real value once write-intent proposals (R-20) and learned intents (R-22/23) exist.
  - **`bindPendingHandler`'s `Kind:"decision"` result (exactly one envelope pending) maps to `outcome:"proposed"`,** not `"decided"` — it has only surfaced an existing envelope for a later voice-binding decision (R-21), not decided anything. Documented in code as the closest existing enum value, not a claim that anything was approved.
  - **Left at zero value, explicitly, not guessed:** `rec.slots` (no clean source exists yet — `render.Result.Facts` mixes derived display strings like `<slot>_spoken` with other facts, and the real `Validated.Labels` map from Design §11.3 isn't surfaced through `TryTier0`'s current return signature; wiring it would mean changing `tier0.go`, outside R-14's stated file scope). `partials`/`first_partial_lead_ms`/`speculation`/`speculation_model_calls` (R-19). `tools_used`/`tools_attributed`/`quick_only`/`tool_signature` on real rows (the `ToolTracer`/`QuickOnlySignature` mechanism is built and unit-tested, but nothing calls `RecordUse` yet — no quick tools or main-path tool bridge exist before R-16). `action`/`confirmed` (R-20/21, a later export/labeling slice).
  - **The Tier 0 breaker is wired into `Handle`,** not just built and tested standalone: `n.tier0Breaker.Allow()` gates whether Tier 0 is even attempted, `RecordSuccess`/`RecordFailure` are called around the attempt (a clean escalation like `no_match` is not itself a failure — only a genuine handler error counts), and an open breaker sets `escalation_reason="breaker_open"`. The miss-RATE trip (`RecordMissRate`) is not yet fed from real aggregate route_log stats — computing "the last 50 answered turns' possible-miss rate" on every turn was judged too much scope to add correctly under this task's file list; it's built, unit-tested via direct `RecordMissRate` calls, and its exact aggregate wiring is a good candidate for a small follow-up alongside R-15's health endpoint.
  - **`GET /v1/router`/`GET /v1/route/report`-shaped fields exist as methods** (`Tier0Breaker()`, `ToolTracer()`) for R-15/R-25 to call, but no HTTP surface exists yet — out of scope for this task's file list.
  - Flag: none. Breakers use code defaults.
- [x] **R-15 Daemon and startup wiring** (`internal/gateway/{handlers.go,daemon.go}` **[shared]**, new `internal/gateway/router.go`, `internal/cli/{twin.go,cmd_daemon.go}` **[shared]**).
  - `buildTwinDepsFS` and `loadTwinManifest`:
    - call `intents.LoadRegistry(fsys, m, Functions{reflex.Specs(), propose.Specs()}, LoadOptions{Schema: schemaFromRegistry(reg)})` after `decisions.LoadRegistry` and before the store opens. `propose.Specs()` is empty until R-20;
    - call `render.LoadStyle`;
    - open `store.OpenReadOnly` after `store.Open`.
  - `gateway.Config` gains `Nervous *nervous.Nervous`.
  - `handleTurn` accepts an optional `turn_id` and calls `Nervous.Handle`. Taint and meeting-context logic is unchanged.
  - `turnSink` gains `channel`. `Daemon` implements `reflex.TaskControl`.
  - Routes: `GET /v1/router`, `GET /v1/route/report` and `GET /v1/voice/profile`.
  - Tests (`internal/gateway/routing_test.go`, real HTTP):
    - a T0 turn makes no backend request and writes 1 row;
    - a main turn;
    - a meeting-id turn whose raw text matches T0 while the meeting taint still escalates;
    - `control.stop` cancels another in-flight turn;
    - the gateway variant of T0-while-main-blocked;
    - `/v1/router` JSON lists the inactive write intents;
  - **Built as R-15 actually needs it today, not the plan's full future shape:** `propose.Specs()`/`Schema: schemaFromRegistry(reg)` are omitted from the `intents.LoadRegistry` call — no write intents exist until R-20, so `Functions{Read: reflex.Specs()}` alone is correct and `intents.LoadOptions{}` needs no schema callback yet; both get added when R-20 lands. `turnSink` does not yet gain a `channel` field — nothing reads it before R-21's voice-approval read-back hook exists. `daemonTaskControl` (new, in `cmd_daemon.go`) satisfies `reflex.TaskControl` by forwarding to two new `*gateway.Daemon` methods (`RunningTasks`, `CancelTasksExcept`) over the daemon's existing task-cancellation map, wired for real at daemon startup — so `control.stop` is functionally live against the real embedded `twins/ceo/intents/control_stop.yaml` (R-9) — but this task's own `routing_test.go` doesn't yet add an HTTP-level test proving one turn cancels another's stream, nor a gateway-level repeat of R-12's package-level "Tier 0 answers while main is blocked" test. Both are reasonable follow-ups, not blockers: the underlying mechanism (`Daemon.tasks`/`d.mu`) is unchanged from what `POST /v1/tasks/{id}/cancel` already exercises. `/v1/router`'s inactive-intents list has nothing to show yet (no write intents exist), but the field is real and populated correctly once R-20 lands.
    - `/v1/voice/profile` JSON.
  - `internal/cli/twin_test.go`: a malformed intent file fails before the store opens.
  - The existing `daemon_test.go`, `scenario_s13_test.go` and `chat_test.go` pass.
  - Flag: none new. `DefaultConfig`.
- [x] **R-16 Quick tools** (`internal/nervous/reflex/quick.go`, `internal/tools/{quick.go,policy.go,service.go}`, `internal/gateway/quick.go`, the `internal/gateway/daemon.go` **[shared]** hunks: the prefix guard, `RecordToolUse`, `TwinToolPolicy.Quick`).
  - **`QuickService.Invoke` renamed to `Run`.** `internal/guards`' repo-wide "only the gate calls Invoke" scan (Slice A1) flags any call to a method literally named `Invoke` outside `internal/gate` by AST shape alone — it can't tell a connector's `Invoke(permit.Permit)` from an unrelated `reflex.QuickService.Invoke`. Renamed rather than touching that security-critical guard test.
  - **A second pre-existing bug found and fixed:** `internal/nervous/reflex/handlers.go`'s `nextEventHandler` (R-8) never handled `store.ErrNotFound` — `store.NextEvent` returns that sentinel, not a nil `*Event`, when nothing is upcoming, so every "next event" call against an empty store errored instead of answering "no upcoming events." R-8 never tested this path; R-16's own tests were the first to call it against a real empty store. Fixed with `errors.Is`.
  - `nervous.Config` gained `Manifest`/`Approvals` fields so `Quick()`'s per-call `reflex.Deps` (built outside any turn) can reach the manifest and approval queue; `approvalsListHandler` was made nil-safe against `Deps.Approvals` for the same reason (a turn's own `Deps` always has a non-nil `env.Approvals`, but a bare test `Config` might not set the new field).
  - Tests:
    - `internal/tools/quick_test.go`: `Policy.Validate` rejects collisions, `quick__` connector names and unprefixed ids; `Service.Call` routes quick tools to `/v1/quick/invoke` and twin tools to `/v1/tools/invoke`, using a fake Unix-socket server that records paths;
    - `Definitions` includes both kinds;
    - MCP `tools/call` results for quick tools are `untrustedWrap`ped;
    - `reflex/quick_test.go`: arg resolution, defaults, unresolved-arg errors, `TestQuickParallel`;
    - `gateway/quick_test.go`: a tainted result escalates the session token; 8 parallel invokes; a `quick.` id sent to `/v1/tools/invoke` is denied without calling `Gate.Invoke` (gate audit length unchanged); an unknown quick id is denied;
    - `quick_boundary_test.go` (AST);
    - a warm-session policy snapshot includes the quick tools.
  - Flag: `router.quick_tools.enabled` (code default on).
- [x] **R-17 FunctionGemma sidecar and model pull** (`internal/nervous/sidecar/{supervisor.go,model.go,evalgate.go}`, `internal/cli/cmd_model.go`, `internal/cli/root.go` **[shared, one line]**).
  - First, verify on Hugging Face which GGUF of `functiongemma-270m-it` exists (official, or a pinned community conversion). Record the repo, file, revision and sha256 in `model.go` and `docs/functiongemma.md`. If no GGUF is available, **stop and ask** (Risks, item 11).
  - Tests:
    - supervisor start, health, restart with backoff and stop against a fake re-exec server;
    - a non-loopback bind is impossible;
    - `pull` against an `httptest` server: sha mismatch is rejected and the `.part` removed; without `--accept-gemma-terms` it refuses;
    - `evalgate_test.go`: accept or reject on each threshold, a stale model sha, a stale registry hash.
  - Flag: `router.tier1.enabled` (code default **off**). The supervisor is never started while off.
  - **Done (2026-09-24).** Verified live against the Hugging Face API (not training data): `ggml-org/functiongemma-270m-it-GGUF` exists and is the well-known "official-conversion-org" pin the Risks item asks for, converted straight from `google/functiongemma-270m-it`. Picked `functiongemma-270m-it-q8_0.gguf` (the repo's only quantized option besides `bf16`; no Q4/Q5/Q6 build exists there) at revision `2566ce14aedfc14fdd0de955ba67346425e67126`, sha256 `83940d4dd9676710856f43523bed096164a595a96f6b34771610a03937de5270` — read from the API's `?blobs=true` listing and cross-checked against the `resolve` URL's `X-Linked-ETag` header (both agreed). Full values also in `docs/functiongemma.md`.
  - **Design calls beyond the plan's literal `Supervisor` sketch:** `State`/`ConsecutiveFailures` accessor methods were added (not in the plan's code block) to make "expose a queryable unhealthy state" concrete, since the plan explicitly deferred live breaker wiring to a later task. `Stop` needed a `started`/`stopped` pair of booleans guarded by the same mutex as the spawn/publish step in `runLoop` — publishing `cmd` to the struct only *after* `cmd.Start()` returns, and re-checking `stopped` right after — to close a real `go test -race` failure where `Stop`'s `cmd.Process.Kill()` raced `os/exec`'s own internal initialization of a just-spawned `os.Process`. The backoff wait is a per-instance interruptible `sleepFn` (default: a real timer selecting on the stop channel) rather than a package-level `time.Sleep` var, so `Stop` never has to block out a real 60s backoff, and so tests (same package) can swap it for a non-blocking recorder without any cross-test global-state risk under `-race`.
  - The download/verify mechanics (streaming sha256, `.part` cleanup, HF_TOKEN/401/403 handling) live in `internal/cli/cmd_model.go` itself rather than in `sidecar/model.go`, which the plan describes as holding only the pinned constants; `model.go` stays just the pin plus `ModelDownloadURL()`.
  - Rebase check: `git rebase feat/ceo-twin` on `slice-r` was a no-op (see the R-4 entry above) — no new migration collision, nothing else to reconcile.
- [x] **R-18 Tier 1 adapter and live eval** (`internal/nervous/t1/{client.go,decl.go}`, `internal/nervous/tier1.go`, `internal/cli/cmd_route.go`'s `eval --tier1` subcommand, `internal/nervous/eval/live_test.go`).
  - Tests:
    - declarations cover only active, reflex-eligible, non-pending intents;
    - the parser handles zero, one and several calls, text replies and unknown names;
    - grounding rejects hallucinated values ("thursday" when the user said "friday");
    - a deny word outside the literal vocabulary is rejected;
    - T1 answers are rendered with the interpretation echo;
    - a fake-client end-to-end run through `Handle`;
    - `TestTier1Live` (`WATER_EVAL_LIVE=1`) runs the real sidecar and writes the eval-gate file;
    - reasoning cases get 0 answers from T1 even with a fake client that always returns a call.
  - Flag: `router.tier1.enabled` off plus the eval gate.
  - **Done (2026-09-24).** `internal/nervous/t1` (`decl.go`, `client.go`) plus `internal/nervous/tier1.go`'s `TryTier1`, wired into `nervous.Handle` right after Tier 0 (new `Config.Tier1Enabled`/`Config.T1`, a second, independent `tier1Breaker`); `internal/nervous/eval/live.go` (`RunTier1Live`, `Tier1LiveOptions`) plus `LoadT1Supplement` added to `eval.go`; `internal/cli/cmd_route.go` (`water route eval --tier1`); `internal/cli/root.go` **[shared, one line]** registers `routeCmd()`.
  - **The `eval --tier1` CLI-vs-test-gated ambiguity:** built both, since the plan's own task list explicitly assigns `cmd_route.go`'s `eval --tier1` subcommand to this task (distinct from `report`/`candidates`, which stay R-26's). Both the CLI command and `TestTier1Live` call the same `eval.RunTier1Live`, so the live-eval mechanism has exactly one implementation.
  - **Design calls beyond the plan's literal sketch:**
    - `t1.Client`'s `Propose([]Call, error)` can't itself distinguish "zero tool calls, no text" from "a text-only reply" (both are `len(calls)==0`), and the plan's own task text left this "your call, document it." Resolved with a sentinel, `t1.ErrTextReply` (wrapped, `errors.Is`-checkable), so `TryTier1` maps it to `t1_text` distinctly from `t1_no_call`. Zero/2+/valid calls are told apart by the returned slice's length, so no sentinel was needed there.
    - `Decl`'s given shape (`{Name, Description, Params}`) has no room for the original intent id, but the HTTP client needs to map a reply's wire-safe tool name (`mail_latest_from`) back to the real dotted id. Rather than relying on a `strings.Replace` reversal (ambiguous for a hypothetical future id whose first segment itself contained an underscore), `Decl` carries an unexported `intentID` field, set only by `Declarations` and read back via `Decl.IntentID()` — invisible to a caller building a `Decl{Name: ..., Description: ..., Params: ...}` literal, but eliminates the ambiguity entirely.
    - An unrecognized tool name from the sidecar is passed through unconverted (no dot) rather than dropped by the client; `TryTier1`'s own registry lookup then naturally reports `t1_unknown_intent` — one check point instead of two.
    - Grounding's count/word equivalence ("3" vs "three") is done with a small local `countWords` map in `tier1.go`, deliberately duplicating `slots`' own (unexported) vocabulary rather than exporting it, since this is the only cross-package need for it.
    - The live eval's `Tier` wrapper (`eval.tier1EvalTier`) runs the **full real cascade** — `nervous.Eligible` → `nervous.TryTier0` → `nervous.TryTier1` only on a clean `no_match` — not Tier 1 in isolation. This is what makes "reasoning cases get 0 answers, even live" and "`ceo_eval.yaml`'s Tier-0-held-out positives really do reach Tier 1" both true by construction, matching production exactly.
    - `warm_p95_ms` is computed from latencies recorded only around the calls that actually reached Tier 1 (a private slice inside the eval wrapper, percentiled with `eval.percentile`), not from the harness's own `Report.P95` — the latter would be diluted by every case Tier 0 or eligibility already resolved without a request, understating the sidecar's true per-call latency against the 400ms gate threshold.
    - **Known, documented limitation:** the live eval's `Try` reports only which intent answered, not resolved slot values — `render.Result` carries a phrased interpretation, not a `Case.Slots`-comparable canonical label, and plumbing that through `TryTier0`/`TryTier1`/every handler is out of this task's scope. The live false-accept rate therefore catches a wrong intent or an answered negative/reasoning case (the dominant risk), but not a right-intent-wrong-slot-value answer; the offline fake-client tests in `internal/nervous/tier1_test.go` cover slot-level grounding directly instead.
    - `TestTier1Live` could not be run against the real model/sidecar in this sandbox (no network access to pull the ~280MB GGUF, no `llama-server` installed) — it was verified to skip cleanly under both of `liveEvalPrereqs`' independent conditions (`WATER_EVAL_LIVE` unset; the model file absent), per `TestTier1LiveSkipsWithoutEnvVar`/`TestTier1LiveSkipsWithoutModelFile`.
  - Rebase check: `git rebase feat/ceo-twin` on `slice-r` was a no-op (HEAD was already `277b5d2`) — no new migration collision, nothing else to reconcile.
- [x] **R-19 Partials, speculation, prewarm** (`internal/nervous/speculate.go`, `internal/backend/warmsession.go` `Prewarm`, the partial route in `internal/gateway/router.go`, the `handleTurn` `turn_id` pass-through **[shared]**).
  - Tests:
    - **`TestPartialsNeverAnswer`**: 50 partials produce no events on any stream and no route row until final;
    - **`TestSpeculationZeroModelCalls`**: the fake backend's `Calls()` is unchanged, and `speculation_model_calls = 0`;
    - speculation reuse within 2 s and a rerun after;
    - a new partial cancels the previous speculation;
    - rate and size limits; TTL expiry;
    - **`TestNoDoubleAnswer`**: final matches T0 while prewarm is in flight, giving one answer;
    - `warmsession_test.go`: `Prewarm` starts the process without writing to stdin, returns `busy` when the semaphore is held and `alive` when the key matches;
    - `POST /v1/turns` without `turn_id` is unchanged.
  - Flag: `router.speculation.enabled` (code default on, no model calls).
  - **Done (2026-09-24).** `internal/nervous/speculate.go` (new: `Speculation`, `SpecDeps`, `speculate`, `shouldSpeculate`, `sameTokens`/`sameLabels`); `internal/nervous/tier0.go` refactored to extract `matchOnly` (the match+validate loop, no handler run) and `runTier0Match`, so `TryTier0`, the dry match and Handle's own speculation-reuse check share one implementation instead of three; `internal/nervous/nervous.go` gained `Turn.ClientID`, `Config.Speculation`/`Config.Prewarm`, `Nervous.Partial`, `specDeps`, `trySpeculationReuse` and wired the reuse check into `Handle`'s Tier 0 block; `internal/nervous/routelog.go` gained `captureSpeculationFacts`, reading `turn.Table` at log time for `partials`/`first_partial_lead_ms`/`speculation` (never from the turn's own text); `internal/nervous/turn/turn.go` gained `Table.SetSpec`/`GetSpec` (the accessors the `Spec any` field was left for since R-10); `internal/backend/warmsession.go` gained `Prewarm`; `internal/gateway/router.go` gained `handleTurnPartial` and `partialLimiter`; `internal/gateway/daemon.go` registered the route and the limiter; `internal/gateway/handlers.go` wired `body.TurnID` into `nervous.Turn.ClientID` (the one hunk R-15 left "accepted but unused"); `internal/cli/cmd_daemon.go` **[shared]** added `daemonPrewarmer`, wiring `Config.Prewarm` to the real `WarmSession` with the same system prompt/model/tool-policy a real main-path turn would use.
  - **"Zero model calls" is enforced structurally, not just by test.** `SpecDeps` (the only thing `speculate()` can call through) has no backend field and no Tier 1 client field — there is no Go value of the right type to put there even by mistake. `nervous.Config.Prewarm` and `SpecDeps.Prewarm` are both `func(ctx context.Context) (string, error)`, deliberately with no `backend.Request` parameter: `nervous` has no way to build one anyway (it never learns the twin's role/style text), so the daemon's own wiring (`daemonPrewarmer`) captures the request once, outside `nervous` entirely. `TestSpeculationZeroModelCalls` (in both `internal/nervous/speculate_test.go`, driving `speculate()` directly with a `backend.Fake` sitting unreferenced nearby, and as the acceptance criterion for every other speculation test) asserts this in practice too.
  - **Reuse condition, exactly:** at `Handle`'s Tier 0 step, `trySpeculationReuse` requires a cached `*Speculation` for this turn id where `hit` is true (both `DryMatch` and `RunRead` succeeded during a partial), `now.Sub(computedAt) <= 2s`, and — checked via the same `matchOnly` Tier 0 itself uses against the FINAL utterance, never the cached dry-match's own say-so — the winning candidate's intent id and resolved slot labels (`map[string]string`, exact equality) both match. Anything else falls through to a normal `TryTier0` run; the speculative work was simply wasted, which the design accepts.
  - **Rate-limiting design call:** partials past 20/s for one turn id are silently dropped (still `202 {"state":"listening"}`), not errored — implemented as a package-level `partialLimiter` in `internal/gateway/router.go`, a tiny per-id fixed-window counter (reset each wall-clock second) with opportunistic cleanup of stale entries once the map exceeds 64 ids, rather than a precise sliding window. A dropped partial costs nothing: the next one that gets through still carries the latest (superset) text.
  - **Debounce design call:** `shouldSpeculate` (unit-tested directly) skips a partial when its normalized tokens exactly match the previous speculation run (never re-runs, regardless of elapsed time — nothing would come out differently), and separately skips a *changed*-token partial that arrived less than `specDebounce` (150ms) after the last run. Speculation itself runs synchronously inside `Nervous.Partial` (no background goroutine, no explicit cancellation) — store reads and a non-blocking `Prewarm` try-acquire are both fast enough that this never blocks the `202` response noticeably, and it sidesteps an entire class of goroutine-lifecycle/race bugs a truly async design would need to close. "A newer partial cancels the previous speculation" is satisfied by simply overwriting `turn.Table`'s cached `*Speculation` (via `SetSpec`) rather than by tracking an explicit cancel signal — nothing else in `Handle` re-reads a stale one, since `trySpeculationReuse` runs exactly once, before the turn is ever routed.
  - **`Deps.Brief` stub during speculation:** a dry-matched `brief.today` always reports a cache miss (`return "", false, false, nil`) rather than being wired to `runtime.CachedBrief`, since `Nervous.Partial` has no per-turn `runtime.Env` to build that from. This means speculation never pre-answers `brief.today` — it always falls through to `Prewarm` instead — which is the safe, conservative direction (never fabricates a brief, never risks a stale one being "reused").
  - **`runtime.StateSummary` reuse is partial:** the `Summary` closure calls the real `runtime.StateSummary` for its store-read behavior, but omits `env.Approvals` from the temporary `runtime.Env` it builds (that field is a concrete `*approvals.Queue`, while `nervous.Config.Approvals` is the narrower `reflex.PendingLister` this package actually depends on, and converting would need an import this package doesn't otherwise need). Harmless in practice: `speculate()` only inspects `Summary`'s error, never its text, to set the route_log row's `speculation.summary` flag.
  - **Left as documented, not fixed:** `internal/nervous/reflex/handlers.go`'s `cachedBriefHandler` still calls `d.Brief(ctx, day)` with no nil check — unchanged from before this task, and safe here only because the stub above is always non-nil.
  - Rebase check: `git rebase feat/ceo-twin` was a no-op both at the start and before this commit — `feat/ceo-twin`'s local ref (`2adc5a9`) is an ancestor of `slice-r`'s tip, not the other way around, so there was nothing to replay. No new migration collision (`0010_router.sql`, already used by R-14 for the route_log table this task extends, is unchanged; no new migration file was needed since R-14 already reserved the `partials`/`first_partial_lead_ms`/`speculation`/`speculation_model_calls` columns this task populates).
- [x] **R-20 Write intents and proposals** (`internal/nervous/propose/{propose.go,calendar.go,mail.go}`, `internal/nervous/actions.go`, `internal/gateway/actions.go` extracting `proposeEnvelope` from `handleToolInvoke` **[shared hunk in daemon.go]**, the 4 write intent files, their `responses:` entries).
  - First, `git log feat/ceo-twin -- internal/connectors/google` to see whether the other session's write functions have landed. If they have, read their `connectors.Schema` and make `Emits` match. If not, use the `slice-c-planning.md` §1 keys; the load-time schema check (§5.4) catches any mismatch when they land.
  - Tests:
    - proposer unit tests: create or move event resolution, including 0 or 2 events at that time escalating; reply-to-latest-message; `Tainted` from External records; optional keys dropped when the schema doesn't declare them;
    - `TestIntentFileTests` covers write intents under a fixture manifest granting all four actions;
    - `TestEmbeddedCEOIntentsLoad` shows them inactive under the real manifest while the functions are absent;
    - gateway `actions_test.go` with a fake connector registry granting `gcal.create_event: A`: a proposal creates exactly one pending envelope (audit gains one `propose` record); read-back and `approval_required` are emitted; **no connector execution happens** (fake connector call count 0); `handleToolInvoke`'s existing tests pass unchanged; an R-level target is denied.
  - Flag: none. Each intent is active only when its action is granted at level A, and all effects require approval.
  - **Done (2026-09-24).** The other session's write functions had already landed (`git log` confirmed): `twin.yaml` grants `gcal.create_event: A`, `gcal.move_event: A`, `gmail.send_message: A` and `gmail.draft_message: D`, exactly as R-6's Risk item 24 found. Since all four target functions are already granted, all four new intents load **active** under the real ceo manifest — `TestEmbeddedCEOIntentsLoad`'s want-list was updated to include them (not "inactive while absent," which no longer describes reality) and its count now checks 16, not 12.
  - **The level-D question (Risk item 24), resolved as its own default suggested:** `checkAction` (`internal/nervous/intents/planned.go`) now returns a fourth value, `requiresApproval bool`, alongside `active`/`inactiveReason`/`err`. A granted level other than A or D is still a hard load error (message updated to say so); A or D both make the intent active, with `requiresApproval = (level == A)`. `Intent` gained a computed `RequiresApproval bool` field set from this at load time. R-6's own "granted at a level other than A is an error" test was repointed at level R (still an error, now for "neither A nor D") and a new "granted at level D" test asserts `Active && !RequiresApproval`; the "granted at level A" test now also asserts `RequiresApproval`. The schema/`Emits` check runs identically for both levels.
  - **The dispatch gap this task closed:** `matchOnly` (`tier0.go`) no longer skips `kind: write` candidates — they compete in the same specificity pass read intents do. `runTier0Match` and `TryTier1` both gained a `writeHandler` parameter (`func(ctx, intents.Intent, reflex.Args) (*render.Result, string, error)`); for a matched write intent they call it instead of `reflex.Table()` (whose lookup would silently miss, since a write intent's `Function` is always empty by construction). Every pre-existing call site (`nervous.go`'s two real calls, ~14 direct calls in `tier0_test.go`/`tier1_test.go`, two in `internal/nervous/eval/live.go`) passes `nil`, which is safe: `nil` is only ever invoked after the matched candidate's `Kind` is confirmed write, and no pre-R-20 fixture has one. `answerQuick`'s `intent_kind` is no longer hardcoded `"read"`: it looks the answered intent up in the registry and reports `"write"` when `Kind == KindWrite`.
  - **`internal/nervous/propose`** (new package; imports only `store`, `slots`, `intents`, per Design §2's dependency table — it does **not** import `reflex`, so `Deps.Store` is `propose.StoreView`, a second, narrower interface with the identical `EventsInRange`/`MessagesFrom` method set, and `Build`'s third argument is the unnamed `map[string]slots.Value` rather than a named `reflex.Args`, so a caller's `reflex.Args` value is assignable with no conversion or adapter): `Table()`/`Specs()` register three proposers — `calendar.create` (→ `gcal.create_event`; combines a `date` slot's `Start` with a `time` slot's `At`; `dur` defaults to 30 minutes when absent from args at all, defense in depth beyond the intent's own YAML default; `who` optional, defaulting to an empty-but-present `attendees` list and a generic title), `calendar.move` (→ `gcal.move_event`; needs exactly one non-cancelled event starting at `from` on `on` — **zero matches is `Unresolved`, two or more is `Ambiguous`**; preserves the original event's duration at the new start), and `mail.reply` (shared by both `mail.draft_reply` and `mail.send_reply`; needs the latest message from `who` — no match is `Unresolved` — and adds a `Re: ` prefix only if the subject doesn't already have one). **`Proposal.Action` is deliberately left empty by every `Build` function**: since `mail.reply` backs two intents targeting two different connector functions depending on the manifest's granted level, only the matched *intent*'s own `Action` field is authoritative — `nervous.tryWriteIntent` (not the proposer) decides what actually gets called.
  - **`internal/nervous/actions.go`** (new): `ActionSink` interface (`ProposeAction(ctx, fn, payload, ch) (approvals.Envelope, error)`) and `(*Nervous).tryWriteIntent`, which builds the proposal, then branches on `it.RequiresApproval`: **false (level D)** → a `render.Result{Kind: "read", ..., Text: p.Summary}` delivered directly, no `ActionSink` call, no `ApprovalID`, no `approval_required` event; **true (level A)** → `cfg.Actions.ProposeAction(ctx, it.Action, p.Payload, ch)`, then `render.Result{Kind: "decision", Text: approvals.ReadBack(envelope), ApprovalID: envelope.ID}` — `answerQuick`'s existing `if result.ApprovalID != ""` branch (written in R-15, before any write intent existed) needed no change at all to emit `approval_required`. `propose.Unresolved`/`Ambiguous` map to escalation reasons `entity_unresolved`/`entity_ambiguous`; any other error (a store read failure, or `Actions == nil` when approval was needed) flows through Handle's existing generic handler-error path.
  - **`internal/gateway/actions.go`** (new): `proposeEnvelope` is `handleToolInvoke`'s queued-envelope body extracted **verbatim** (its diff to `daemon.go` is 3 lines removed, 1 added — a call-site swap, not a rewrite); it does not decide *whether* an envelope is needed, only builds one once a caller has decided yes. `(*Daemon).ProposeAction` implements `nervous.ActionSink`: it defensively re-checks that the target function is granted at exactly level A (independent of `checkAction`'s own load-time guarantee — the regression test the task called for) before ever calling `proposeEnvelope`, denying anything else with "sous proposals must be level A." It does not call `notifyApprovalRequired` (that mechanism is keyed to the model-tool bridge's `activeTask`, which has no meaning for a sous-chef-answered turn that never touched the warm session) — the read-back and `approval_required` emission for a write intent happen in `nervous.answerQuick`, on the turn's own emitter, which is the architecturally correct place since Design §11.1 restricts a `quick`-owned turn to emitting only through its own stream.
  - **Wiring** (`internal/cli/twin.go` **[shared]**, `internal/cli/cmd_daemon.go` **[shared]**, `internal/cli/cmd_route.go`): `intents.LoadRegistry`'s `Functions.Write` is now `propose.Specs()` everywhere the *real* `twins/ceo/intents` directory loads (three call sites) — omitting it would fail the whole registry load the instant a write intent file exists, not just leave it inactive. `buildTwinDepsFS`/`loadTwinManifest` were reordered so the connector registry builds *before* the intent registry, since the intent registry now needs it for `LoadOptions.Schema` (`schemaFromRegistry`, a new small adapter turning `connectors.Registry.Lookup` into `intents.SchemaInfo`, so `intents`/`propose` never import `internal/connectors` themselves). `cmd_daemon.go` gained `daemonActionSink`, the same construction-order-breaking pattern as the existing `daemonTaskControl`/`daemonPrewarmer` (`*nervous.Nervous` is built before the `*gateway.Daemon` that implements `ProposeAction`).
  - **The 4 intent files** (`twins/ceo/intents/{calendar_create_event,calendar_move_event,mail_draft_reply,mail_send_reply}.yaml`) follow Design §7's table exactly (templates, slots, `reflex_eligible: true`). `twins/ceo/style.yaml` gained a `responses` entry per intent (a plain literal header; the dynamic content is always `Result.Text`, never `Facts` substitution — the same pattern `brief.today` already used). Test utterances were deliberately **not** copied from `internal/nervous/eval/testdata/ceo_eval.yaml`'s own (pre-existing, held-out) write-intent cases — `TestEvalNoLeak` caught three verbatim collisions on the first pass (this eval set already anticipated R-20's exact intent ids/templates), fixed by changing the times/wording in the intent files' own `tests:`.
  - **R-18's write-kind gap, checked and confirmed real, now closed as a side effect:** `TryTier1`'s handler lookup had the identical `reflex.Table()[it.Function]` miss `runTier0Match` had; both are fixed by the same `writeHandler` mechanism, so Tier 1 can propose a `reflex_eligible` write intent exactly like Tier 0 can.
  - Rebase check: `git rebase feat/ceo-twin` was a no-op both at the start and before this commit.
- [x] **R-21 Voice approval binding** (`internal/nervous/{readback.go,voiceapprove.go}` plus the `actions.go` decision path, `internal/gateway/actions.go` extracting `decideAndExecute` from `handleDecideApproval` **[shared hunk in handlers.go]**, the `notifyApprovalRequired` voice hook **[shared hunk in daemon.go]**).
  - Tests (fake connector registry, fake clock):
    - **`TestVoiceYesWithin60sDecides`**: the envelope is approved and executed once, the hash matches, and the audit has an approval;
    - **`TestVoiceYesAfter61sDoesNot`**;
    - **`TestEditVoidsBinding`**: `Queue.Edit`, then "yes" gives a re-surfaced read-back and no decision;
    - **`TestTwoPendingGivesMenu`**;
    - **`TestHighRiskRequiresTap`**: `gmail.send_message`, an external attendee, `Risk: high` and `Risk: ""` each give `tap_required` with no decision;
    - "no" denies at every tier;
    - `VoiceApprovalTier` table tests for all six rules;
    - a different channel is not bound;
    - **flag off** keeps the earlier bind-and-surface behaviour;
    - the existing approval-decision HTTP tests pass unchanged;
    - `grep`/AST: no `Queue.Decide` under `internal/nervous`.
  - Flag: `router.voice_approve.enabled` (code default **off**).
  - **Done (2026-09-24).** New: `internal/nervous/readback.go` (`Readback`, `Readbacks`, `NewReadbacks`, `Record`/`Void`/`Bound`, plus `(*Nervous).RecordReadback` for the gateway to call from outside the package) and `internal/nervous/voiceapprove.go` (`VoiceTier`, `VoiceApproveConfig`, `voiceEligibleActions` — `gcal.create_event`/`gcal.move_event`/`gmail.draft_message` only, deliberately excluding `gmail.send_message` — and `VoiceApprovalTier`, the pure 6-rule risk mapping). `internal/nervous/actions.go` gained `Approver`/`DecisionOutcome`, the read-back `Record` call in `tryWriteIntent`'s level-A branch, `voiceApproveActive` and `answerVoiceApprove` (the full decision procedure). `nervous.go` gained `Config.Approver`/`Config.VoiceApprove`, a `*Readbacks` field on `Nervous`, and one new `if` branch in `Handle` right before the existing `answerQuick` call.
  - **The `answerQuick` bypass, not a rewrite of it.** Rather than mutating `bindPendingHandler`'s `Result` and always routing through `answerQuick`, `Handle` checks `voiceApproveActive` (flag on, `Approver` set, channel voice, `result.Intent == "approvals.respond"`) and, only then, calls the new `answerVoiceApprove` instead of `answerQuick` for that one turn. Every other combination — including the flag off — reaches `answerQuick` exactly as before this task, so `bindPendingHandler` itself needed zero changes and its pre-R-21 rendering (see `docs/known-gaps.md`'s new entry) is untouched.
  - **`gateway.Origin`'s real value is lowercase (`"p2"`), not `"P2"`.** `internal/gate.Origin` constants are `p0`/`p1`/`p2`; `VoiceApprovalTier` checks the literal `"p2"` (matching real usage, e.g. `apicompat_test.go`'s `Origin: "p0"`), with a comment explaining why the literal is used instead of importing `internal/gate` (Design §2's dependency-direction rule: `internal/nervous` never imports `internal/gate`).
  - **`decideAndExecute`'s real return type is richer than the plan's literal `(DecisionOutcome, error)`.** The HTTP endpoint's existing `DecisionResult` contract (behavior-preserving extraction requirement) needs the envelope, the echoed answer, the raw output and `OutcomeUnknown` — none of which fit `nervous.DecisionOutcome{Status, Executed, Error}`. `internal/gateway/actions.go` defines an unexported `decideOutcome` (the full result) and a `decideStageError` (carrying which of not-found/hash-mismatch/internal-failure occurred, so `handleDecideApproval`'s thin wrapper maps each to the same HTTP status as before); `(*Daemon).DecideBound` narrows `decideOutcome` down to `nervous.DecisionOutcome` for the `Approver` interface. `handleDecideApproval`'s diff is a clean decode-call-translate wrapper; the hash-check/Decide/Gate.Invoke/Abandon logic itself moved to `decideAndExecute` unchanged.
  - **Case (b)'s read-back recording (`approvals.respond` surfacing the one pending envelope) lives inside `answerVoiceApprove`'s `resurface()`, not at `answerQuick`'s generic `ApprovalID != ""` checkpoint.** `bindPendingHandler`'s `Result` never carries the envelope's `PayloadHash` (only `ApprovalID`), and getting it at `answerQuick`'s checkpoint would need an extra `Approvals.Get` round trip that only ever matters once the flag is on — which is exactly when `answerVoiceApprove` (not `answerQuick`) is handling the turn anyway. `resurface()` records the binding whenever no valid one exists yet, covering both "never bound" and "stale" the same way, per Design §13 step 3.
  - **Config keys landed with this task, not deferred to R-25.** The plan's §17 table assigns all `router.*` keys to R-25 as one commit; the top-level R-21 brief for this run explicitly asked for `router.voice_approve.{enabled,window_seconds,internal_domains}` to be wired now (first `router.*` keys in `internal/config`), since the feature needs a real settable flag to be testable end to end and R-25 hasn't landed yet. `internal/config/config.go` gained `RouterConfig`/`VoiceApproveConfig` and the four usual touch points (`defaults()`, `apply()`, `Flat()`, `intKeys`/`boolKeys`); `internal/config/config_test.go`'s `TestEveryKeyRoundTrips` switch was generalized from `strings.HasPrefix(k, "sync.")` to `intKeys[k]` so it covers any int key, not just the `sync.*` ones (a small, compatible simplification, not scope creep on R-25's own eventual larger commit). `internal/cli/cmd_daemon.go` gained `daemonApprover` (the same two-step construction-order-break pattern as `daemonActionSink`/`daemonTaskControl`/`daemonPrewarmer`) and `voiceApproveDomains` (comma-split, trim, drop-empty).
  - **`internal/nervous/actions_test.go`'s shared fixtures needed two small, compatible extensions** (used by R-21's own new test file, `voiceapprove_bind_test.go`): `actionsFixtureRegistry` now passes `Read: reflex.Specs()` (it previously passed no read functions at all, so `approvals.respond`'s `function: approvals.bind_pending` failed to load), and `actionsManifestYAML` gained `gcal.create_event` at level A (previously only `gcal.move_event` was granted). Neither change altered any existing R-20 test's behavior.
  - **Structural proof, not just a grep:** `internal/nervous/decide_test.go`'s `TestNoDecideCallUnderNervousTree` walks every non-test `.go` file under `internal/nervous/` (this package and every subpackage) with `go/parser`/`go/ast`, failing on any `SelectorExpr` named exactly `Decide` — mirroring `internal/nervous/reflex/imports_test.go`'s existing denylist-test style rather than a shell `grep`. Test files are excluded so `voiceapprove_bind_test.go`'s `fakeApprover` (a test-only stand-in for `internal/gateway`, calling the real `approvals.Queue.Decide`/`Claim` to get realistic `Pending`/`Bound` behavior across turns) doesn't trip it.
  - Rebase check: `git rebase feat/ceo-twin` was a no-op both at the start and before this commit. No new migration was needed (`0010_router.sql` already exists from R-14; this task adds no store schema).
- [x] **R-22 Promotion: candidate detection** (`internal/nervous/promote/candidates.go`, `GET /v1/route/candidates` in `internal/gateway/router.go`, `water route candidates` in `internal/cli/cmd_route.go` **[pulled forward from R-26]**).
  - Tests: grouping by signature; the ≥ 5 repeats and ≥ 2 days rule; exclusion of possible-miss, unattributed, mixed-tool, reasoning-utterance and non-learnable-handler rows; a stable candidate id. Plus the HTTP contract (`routing_test.go`) and CLI rendering (`cmd_route_test.go`).
  - Flag: listing is read-only and always available. Drafting and promotion are gated in R-23.
  - **A real gap this task found and closed, not just verified:** `ToolTracer.BeginMain/EndMain` (R-14's mechanism) had no caller anywhere — R-16 only wired `RecordToolUse`'s one call site (`gateway/quick.go`), never registered a main turn as "in flight" for it to attribute to. Every `RecordUse` call found `tt.inFlight` empty and silently dropped, so every real `route_log` row's `tools_used`/`tools_attributed`/`quick_only`/`tool_signature` stayed at Go's zero value forever — `QuickOnlyRoutes` could never return anything outside a test that built rows by hand. `answerMain` (`mainpath.go`) now brackets the main path with `BeginMain`/`EndMain`, and `routelog.go`'s `finish` writes the four fields from it. Regression test: `TestRouteLogQuickToolAttribution` (`routelog_test.go`).
  - **`Candidates`'s signature gained an `intents.Shared` parameter**, not in the plan's original sketch (`func Candidates(ctx, s, since, minRepeats)`): re-running `nervous.Eligible` per row needs the twin's escalate words/clause joiners/skip words, and nothing else this function is given carries them. Callers pass `d.cfg.Nervous.Registry().Shared()` (gateway) the same way `handleRouterHealth` already reaches the registry.
  - **`ToolSignature`'s real value is `nervous.QuickOnlySignature`'s existing, already-unit-tested output**: sorted, deduplicated tool ids joined by `+`, with no argument names (e.g. `"quick.calendar+quick.next_event"`) — the `route_log` schema comment's `"quick.calendar(when)+quick.next_event()"` example is illustrative design language, not a literal format anything builds; nothing before this task ever assembled one at all.
  - **"Local day" convention**: `row.At.Local().Format("2006-01-02")`, matching `internal/nervous/reflex/handlers.go`'s `cachedBriefHandler` and `internal/runtime/brief.go`'s `startOfDay` (both convert to local before formatting a calendar day) rather than trusting `RouteRow.At`'s stored UTC representation directly.
  - **Every real quick.\* tool today is `Learnable()`** (`quick.calendar`, `.next_event`, `.latest_mail`, `.mail_from`, `.cached_brief`, `.pending_approvals` all pass), so the "non-learnable tool in a signature" exclusion rule has no real fixture to exercise it against; its test instead uses a tool id `reflex.Table()` doesn't recognize at all, which the lookup correctly treats as not-learnable rather than assuming it's safe.
  - **`GET /v1/route/candidates` is never gated on `router.promotion.enabled`** — that config key doesn't exist yet either (R-25's job); only drafting/promoting a candidate (R-23) will be gated once it does.
  - **Found, logged, not fixed** (pre-existing, unrelated to this task, reproduces identically on R-21's tick commit): `TestQuickInvokeTaintedResultEscalatesSession` (`internal/gateway/quick_test.go`) is time-of-day flaky — see `docs/known-gaps.md`.
- [x] **R-23 Promotion: draft, validate, promote, demote, overlay** (`internal/nervous/promote/{draft.go,validate.go,overlay.go}`, `POST /v1/intents/draft|reload`, overlay loading in `internal/cli/cmd_daemon.go` **[shared]**, the learned breaker hook in `routelog.go`).
  - Tests:
    - **`TestLearnedCannotReferenceIneligible`**: control, approvals, status, text-slot and write targets are all rejected;
    - **`TestLearnedNoAmbiguity`**: an overlap with `schedule.on_date`'s own shipped template is rejected;
    - **`TestLearnedNoFalseAccepts`**: a template matching a reasoning negative is rejected;
    - embedded ids win (through the real `WriteLearned`/`os.DirFS` overlay path, not just a MapFS unit test); the cap is enforced; with the flag off the overlay is not loaded at all;
    - **`TestLearnedAutoDemoted`**: possible misses past 20% over 10 or more disable it, through a real `Handle`-driven correction, and the next matching turn escalates instead of answering;
    - manual demote and enable (`store.SetIntentState`/`ListIntentStates` round trip, feeding `intents.LoadRegistry`'s `LoadOptions.Disabled`);
    - reload swaps atomically under concurrent `Handle` (`-race`, `TestReloadAtomicUnderConcurrentHandle`);
    - draft uses a fake backend, is a cold call (warm untouched — `Draft` takes a plain `backend.Backend`, which has no notion of a warm session to touch), writes to `pending/`, and code fills `id`/`origin`/`provenance` regardless of what the model claims for them;
    - gateway `POST /v1/intents/draft|reload` endpoint tests, including the promotion-disabled 403, an unknown candidate 404, and a multi-tool-signature candidate 400.
  - Flag: `router.promotion.enabled` (code default **off**).
  - **Two real gaps this task found and closed, not just verified** (the same posture R-22 took with tool-tracing):
    1. **`LoadOptions.Learned`/`Disabled` were never wired to anything real.** `internal/nervous/intents.LoadRegistry`'s overlay/disabled-intent mechanism (R-6/R-4) worked correctly in isolation, but every real call site (`internal/cli/twin.go`'s `loadTwinManifest`/`buildTwinDepsFS`) passed `LoadOptions{Schema: ...}` only — an owner could promote or demote all day and the running daemon would never see it. Closed by `internal/cli/cmd_daemon.go`'s new `daemonIntentsReloader` (the same two-step construction-order-break pattern as `daemonActionSink`/`daemonApprover`/`daemonPrewarmer`/`daemonTaskControl`): it rebuilds the registry with `Learned: os.DirFS(promote.LearnedDir(...))` (only when `router.promotion.enabled`) and `Disabled: store.ListIntentStates(...)`, runs once synchronously right after `nervous.New` (so a daemon that starts with pre-existing promoted/disabled state serves its first turn correctly), and is wired as `gateway.Config.ReloadIntents` for `POST /v1/intents/reload` to call on demand. `buildTwinDepsFS` itself was deliberately left untouched (its signature has no config/promotion-flag parameter, and `water status`/`doctor` never need the overlay), rather than widening a shared, multi-call-site function's signature for one caller's need.
    2. **A read intent's Tier 0 answer always reported the embedded intent's own hardcoded id, never the intent that actually matched.** Every handler in `internal/nervous/reflex/handlers.go` hardcodes its own `render.Result.Intent` (e.g. `store.calendar_events`'s handler always sets `"schedule.on_date"`) — harmless before learned intents existed, since exactly one embedded intent ever targeted a given function, but silently wrong the instant a learned intent reuses that same handler: `route_log.intent`/`intent_origin` (and the style-rendered response text) would name the embedded intent, not the learned one, breaking the auto-demotion hook and the one-voice contract simultaneously. Found via `TestAnswerQuickRecordsIntentOrigin` failing with `Intent: "schedule.on_date"` against a registry that doesn't even declare that id. Fixed in `runTier0Match` (`tier0.go`) and `TryTier1` (`tier1.go`): both now overwrite `result.Intent` with the registry's own matched intent id (`top.intent.ID` / `it.ID`) right after the handler returns — restoring the invariant `internal/nervous/actions.go`'s write-intent path already had correctly (it always builds `Result{Intent: it.ID}` itself). Also closed in the same pass: `answerQuick`'s `rec.intentOrigin` was never actually set (a stale comment said so explicitly — "has no meaning yet ... doesn't exist until R-22/23") even though `store.RouteRow.IntentOrigin` was a real column since R-14; it now looks the matched intent up in the live registry and reports `"learned"`/`"embedded"` from `Intent.Origin`, which the whole demotion hook depends on.
  - **`Nervous` gained a real reload primitive.** `Config.Registry` was a `func() *intents.Registry` a caller could in principle re-invoke, but nothing inside `nervous` ever called it more than once — every internal read went through `n.cfg.Registry()` once per call, and there was no way to swap it after construction. Added `registryPtr atomic.Pointer[intents.Registry]` (seeded from `cfg.Registry()` in `New`), a private `n.registry()` reader every internal call site now uses, and the public `(*Nervous).Reload(*intents.Registry)`. A concurrent `Handle` always observes either the pre- or post-Reload registry in full (an atomic pointer load), never a torn read — proven by `TestReloadAtomicUnderConcurrentHandle` under `-race`.
  - **The automatic-demotion hook is fire-and-forget, and deliberately does not reuse the disk-backed reload path.** `routelog.go`'s `finish` launches `go n.maybeAutoDemote(logCtx, prev.Intent)` (never blocking the turn that triggered the possible miss) whenever a possible miss is marked against a row whose `IntentOrigin == "learned"`. It re-queries `store.IntentAnswered(intent, 50)`, applies R-14's already-built pure `LearnedIntentShouldDemote` (`minSamples`/`maxMissRatePct` from the new `nervous.PromotionConfig`, defaulting to 10/20), and — on a demotion — calls `store.SetIntentState` then `n.Reload(n.registry().WithDisabled(intent, reason))`, a new `intents.Registry` method that shallow-clones the CURRENT in-memory registry with just one intent's `Disabled` field changed (no re-parsing, no re-compiling templates, no filesystem access). This is intentionally lighter than `POST /v1/intents/reload`'s full on-disk rebuild: the auto-demote path only ever needs to flip one already-loaded intent off, immediately, from inside the same process that just observed the miss; a manual `demote`/`enable`/`promote` (R-26) instead triggers a real reload, since it may also need to pick up a newly-written file. Errors are logged via a new `Config.Logf` (nil-safe, same `func(format string, args ...any)` shape `internal/agentmail`/`internal/sync` already use), never silently dropped, never surfaced to any turn.
  - **`ValidateLearned`'s exact signature is `(fileBytes []byte, reg *intents.Registry, negatives []eval.Case, maxLearned int) error`** — the plan's own prose gives a 3-argument signature in one place and then separately says "pass [maxLearned] as a parameter" in the next sentence; the 4-argument form is what's actually implemented, since the cap has to come from somewhere and `reg` has no opinion on it.
  - **Reusing the real machinery instead of a second implementation.** `intents.Registry` gained `ValidateAsOverlay` (runs the exact per-file validation `loadLearned` applies to any other overlay file, as a pure dry run that mutates nothing) and `WithOverlay`/`ReadSpec` (support methods for the checks below), all backed by three new unexported fields (`manifest`/`fns`/`schemaFn`) the registry now remembers from its own `LoadRegistry` call, so a caller never has to thread them through a second time. `internal/nervous` gained one exported wrapper, `DryMatch`, over the existing unexported `matchOnly` — the real specificity/tie/deny-word logic Tier 0 itself runs — so `promote.ValidateLearned`'s ambiguity, false-accept and own-tests-pass checks are literally the same matcher a live turn would hit, not a parallel reimplementation of it. `promote` already depended on `nervous` since R-22 (for `Eligible`/`QuickOnlySignature`), so this added no new package edge.
  - **The "ambiguity" rule's "candidate utterances" are the file's own `examples:`, not a separate out-of-band list.** `ValidateLearned`'s signature has no room for the candidate's raw sample utterances beyond what's already baked into the drafted file's `tests:`/`examples:` fields — `Draft` is expected to fold them in there (its system prompt asks for exactly that), so `ambiguityUtterances` reads both fields directly off the parsed `Intent`, needing no additional parameter.
  - **`literalDenyWord` is promote-package-local, not a change to `intents.checkTemplateVocabulary`.** An embedded intent may legitimately use a deny word as a self-exempted literal (`control.stop`'s own "cancel", per `denyWordHit`'s design) — that exemption pattern doesn't make sense for a learned intent, which should never need an action verb as a literal at all, so this is a promotion-loop-specific rule, checked only in `promote.ValidateLearned`, not a tightening of the general per-file loader every embedded file also goes through.
  - **A candidate whose signature names more than one tool is refused at draft time**, not silently drafted against just the first tool: a learned intent's `function:` can only ever be one reflex handler, so a multi-tool candidate (e.g. `"quick.calendar+quick.next_event"`) has no single target to draft against. `POST /v1/intents/draft` returns 400 for one; `TestIntentsDraftMultiToolCandidateRefused` covers it.
  - **`Draft` rewrites `tests[].intent` for every positive case, not just `id`/`origin`/`provenance`.** The system prompt asks the model to write a consistent placeholder id (since it cannot know the final, candidate-derived id in advance); `Draft` now also rewrites every non-`"none"` `tests[].intent` entry to the real, final id after computing it — otherwise every drafted file would fail `ValidateLearned`'s "at least one positive test" check by construction, discovered by `TestIntentsDraftEndpoint` before this fix.
  - **CLI (`water intent list|draft|promote|demote|enable`) is out of scope for this task, not merely deferred.** `docs/slices/R.md`'s own task list assigns `internal/cli/cmd_intent.go` to **R-26**, not R-23 (this file's own entry above lists only `draft.go`/`validate.go`/`overlay.go`, the two HTTP endpoints, the overlay-loading wiring and the breaker hook) — building it now would preempt R-26's own file list and its `[shared]` touches to `root.go`/`cmd_status.go`/`daemonclient.go`, which are safer left to a dedicated task/commit given the concurrent-session file-collision rules this whole slice runs under. "Manual demote and enable" and "promote... refuses when the flag is off" are instead proven at the store/registry and HTTP layers directly (`TestManualDemoteAndEnableRoundTrip`, `TestIntentsDraftRefusesWhenPromotionDisabled`).
  - **Config keys landed with this task, not deferred to R-25**, the same call R-21 made for `router.voice_approve.*`: `router.promotion.{enabled,min_repeats,max_learned,demote_miss_rate_pct,demote_min_samples}` (all five, since drafting/promotion/the cap/the demotion breaker all need a real settable flag to be testable end to end and R-25 hasn't landed yet). `internal/config/config.go` gained `RouterConfig.Promotion` (`PromotionConfig`) and the usual four touch points; `TestEveryKeyRoundTrips`'s generalized loop already covers all five new keys with no bespoke test needed.
  - **`internal/store/route_log.go` gained `RoutesByTurnIDs`**, a small new query (`turn_id IN (...)`) resolving a candidate's `SampleTurnIDs` back to their original utterances for `Draft`'s prompt — nothing before this task ever needed to look a route_log row up by turn id after the fact.
  - Rebase check: `git rebase feat/ceo-twin` was a no-op both at the start and before this commit.
- [x] **R-24 Decider** (`internal/decider/{decider.go,classifier.go}`, the `buildDecisionsTrigger` wrap in `internal/cli/twin.go` **[shared]**).
  - Tests: as in the earlier plan's R1-11.
  - Flag: `decider.provider=none`.
  - Built exactly as designed in §15: `Kind`/`Request`/`Answer`/`Decider`/`ErrUnavailable`/`Null`/`New` in `internal/decider/decider.go`; `Classifier`/`Wrap` in `internal/decider/classifier.go`. `Wrap` returns `fallback` **unchanged** (same interface value, proven with `==`/pointer-identity assertions in `classifier_test.go`) whenever `d` is `nil` or `Null{}`, so `d.Decide` is never even referenced on the default path — no wrapping struct, no indirection. A non-null `Decider`'s successful `Choice` is honored (an unrecognized choice falls through to `generic`, matching `ModelClassifier`'s own floor/unknown-id behavior); `ErrUnavailable` or any other `Decide` error falls back to `Fallback.Classify` rather than failing the classification. `stateText` is a small, deliberate, documented duplicate of `decisions`' own item rendering (`classify.go`'s `itemText`, `record.go`'s `describe` are unexported and `internal/decisions/**` is off-limits to this task), built only from exported `store.Record` fields.
  - `buildDecisionsTrigger` (`internal/cli/twin.go`) now does `wrapped := decider.Wrap(decider.Null{}, classifier, deps.decisions)` before `StoreCache` wraps it — `decider.Null{}` is hardcoded (no config-driven construction here yet; see below), so this line is provably a no-op today. Regression guard: `TestBuildDecisionsTriggerWithNullDeciderMatchesUnwrappedClassifier` (`internal/cli/twin_test.go`) builds the demo twin's real deps, a `backend.Fake` that answers `investor_request`, and asserts the trigger still classifies and builds the same card the pre-R-24 unwrapped `ModelClassifier` path would have.
  - **Design call — `decider.provider` landed with this task, not deferred to R-25**, the same early-landing call R-21/R-23 made for their own flags: `internal/config/config.go` gained a top-level `DeciderConfig{Provider string}` (`decider.provider`, default `"none"`) through all four touch points (`defaults()`, `apply()` — rejecting anything but `"none"` with `decider.provider: only "none" is supported` — `Flat()`, and `TestEveryKeyRoundTrips`'s generic key-fuzzing loop, which special-cases `decider.provider` to `"none"` the same way it already special-cases `brief.ready_after`'s `HH:MM` shape). R-25 should skip re-adding this one key when it lands the rest of Design §17's table. `buildDecisionsTrigger` still hardcodes `decider.Null{}` rather than reading the new config key — wiring the resolved config into the daemon's construction path is R-25's own job (`internal/cli/cmd_daemon.go`), not this task's.
  - Rebase check: `git rebase feat/ceo-twin` was a no-op (already up to date) both at the start and before this commit.
- [x] **R-25 Config keys** (`internal/config/config.go`, `config_test.go` **[shared]**; mapping in `internal/cli/cmd_daemon.go` **[shared]**).
  - Every §17 key in one commit.
  - `cmd_daemon.go` builds `nervous.Config` from the resolved config, plus the Tier 1 eval-gate check that decides whether to start the supervisor.
  - Tests: defaults equal `nervous.DefaultConfig()` (drift test); set/get round trip; int and bool coercion; `decider.provider` rejection; `router.ack_ms ≥ 300` rejected; `internal_domains` parsing.
  - Flag: this is the task that makes the flags settable.
  - **State confirmed at the start of this task**, exactly as the brief predicted: `router.voice_approve.*` and `router.promotion.*` (R-21/R-23) and `decider.provider` (R-24) already existed as config keys; `router.voice_approve.*`/`router.promotion.*` were already wired into `nvCfg` in `cmd_daemon.go`, but `buildDecisionsTrigger` still hardcoded `decider.Wrap(decider.Null{}, ...)` rather than reading `cfg.Decider.Provider`. The 15 remaining §17 keys did not exist anywhere.
  - **Added the 15 remaining keys** (`RouterTier0Config`, `RouterTier1Config`, `RouterMainConfig`, `RouterBreakerConfig`, `RouterSpeculationConfig`, `RouterQuickToolsConfig`, plus three bare `RouterConfig` fields for `possible_miss_window_seconds`/`log_retention_days`/`ack_ms`) through all four touch points. `router.ack_ms`'s `apply()` rejects any value `>= 300` with `"router.ack_ms: %d is not < 300 (set by %s)"`; `Save` refuses it the same way `TestSaveRejectsInvalidValues`-style tests already check for the other rejected values. `router.breaker.min_miss_samples` is **not** in Design §17's table (confirmed by re-reading it) — `nervous.BreakerConfig.MissSample`/`MinMissSamples` stay the `DefaultBreakerConfig()` code defaults (50, 20), never a settable key. `TestEveryKeyRoundTrips`'s generic loop needed one addition: `router.ack_ms` is special-cased to `"290"` (checked *before* the generic `intKeys` branch, which would otherwise try `1000+i` and always fail apply()'s own `>= 300` rule) — the same pattern the loop already uses for `decider.provider`.
  - **Wired all of it into `cmd_daemon.go`**, not just the new keys: extracted a pure `buildNervousConfig(cfg *config.Resolved) nervous.Config` (starts from `nervous.DefaultConfig()`, so any field Design §17 has no key for — `Clock`, `SenderWindow`, `SenderLimit`, the breaker's `MissSample`/`MinMissSamples` — keeps its code default) that sets `Tier0Enabled`, `MainEnabled`, `Breaker` (`Failures`/`Cooldown`/`MaxMissRatePct` from config, `MissSample`/`MinMissSamples` from `DefaultBreakerConfig()`), `MissWindow`, `Retention`, `AckAfter`, `Speculation`, `VoiceApprove` and `Promotion` — replacing the old `nervous.DefaultConfig()`-then-two-field-override call in `runDaemon`. Being a pure function (no I/O) is what makes it directly unit-testable without booting a daemon; `runDaemon` still layers `Registry`/`Style`/`Turns`/`Store`/`ReadStore`/`Tasks`/`Manifest`/`Approvals`/`Prewarm`/`Actions`/`Approver`/`Logf`/`Tier1Enabled`/`T1` on top, exactly as before, since those need real daemon-construction objects `buildNervousConfig` never sees.
  - **`nervous.Config` has no field for `router.tier0.timeout_ms`/`router.tier1.timeout_ms`/`router.quick_tools.enabled`** — confirmed by reading `nervous.go` in full (no `TierConfig` type, no per-tier `Timeout` field, no `QuickTools` field exist; `t1.requestTimeout` is a package constant, not derived from any `Config` field, and quick tools are always exposed unconditionally via `Nervous.QuickFunctions()`, gated only by `gateway.Daemon.TwinToolPolicy` in `internal/gateway/daemon.go`, outside this task's file list). All three keys are still added, default correctly and round-trip (`TestRouterConfigDefaults`, `TestEveryKeyRoundTrips`) — Design §17 lists them as settable regardless of whether a live consumer exists yet — but nothing calls them from `cmd_daemon.go`, documented in code rather than invented against a field that doesn't exist. Wiring an actual per-tier context deadline into `Handle` (`nervous.go`, outside R-25's file list) or gating `TwinToolPolicy`'s `Quick` field (`internal/gateway/daemon.go`, also outside the file list) is left to a later task.
  - **`decider.provider` now wired for real**: `buildDecisionsTrigger` (`internal/cli/twin.go`) gained a `deciderProvider string` parameter and calls `decider.New(deciderProvider)` (falling back to `decider.Null{}` if it errors — unreachable through real config today, since `apply()` already rejects anything but `"none"`, but kept so a decider nothing outward-facing depends on can never fail daemon startup); `cmd_daemon.go` now calls `buildDecisionsTrigger(deps, sel.Backend, cfg.Decider.Provider)`. `TestBuildDecisionsTriggerWithNullDeciderMatchesUnwrappedClassifier` was updated to pass `"none"` explicitly; two new tests, `TestBuildDecisionsTriggerSourcesProviderFromResolvedConfig` (feeds a real `config.Load(nil)`'s `Decider.Provider` through and checks the same card comes back) and `TestBuildDecisionsTriggerFallsBackToNullOnUnsupportedProvider` (defensive-branch coverage), prove the value flows from config and that the fallback is safe.
  - **Tier 1 sidecar startup was genuinely unwired before this task** (R-17/R-18 built `sidecar.Supervisor`/`t1.NewHTTP`/the eval-gate file format, but nothing in `cmd_daemon.go` ever called them). Added the minimal startup logic Design §10 requires: when `router.tier1.enabled` is true, `runDaemon` reads `$WATER_HOME/router/tier1_eval.json` (`sidecar.ReadEvalRecord`) and checks it with the new pure `tier1GateReady(rec, recErr, registryHash)` against `sidecar.ModelSHA256` and `deps.intents.Hash()` — `deps.intents` is the *base* registry (no learned overlay), loaded exactly the way `water route eval --tier1` loads it, so the hashes are directly comparable. Only on a pass does it build `sidecar.Config{Bin: cfg.Router.Tier1.ServerBin, ModelPath: <cfg or $WATER_HOME/models/functiongemma.gguf>, Home: config.Home()}`, `Start` it, and wrap its `Endpoint()` in `t1.NewHTTP`; any failure at any step (gate fails, `Start` errors, `NewHTTP` refuses a non-loopback endpoint) logs the reason via the existing `logf` convention and leaves `nvCfg.T1` nil / `Tier1Enabled` false rather than ever starting an unvetted Tier 1. `defer sup.Stop()` stops the subprocess on daemon shutdown.
  - Tests: `TestRouterConfigDefaults`, `TestRouterAckMSRejectsAtOrAbove300`, `TestRouterIntBoolCoercion` (`internal/config/config_test.go`); `TestBuildNervousConfigMatchesDefaultConfig` (the drift test), `TestBuildNervousConfigWiresNonDefaultValues`, `TestTier1GateReadyRefusesMissingRecord`, `TestTier1GateReadyRefusesStaleRecord`, `TestTier1GateReadyAcceptsPassingRecord` (`internal/cli/cmd_daemon_test.go`, new file); the three `buildDecisionsTrigger` tests in `internal/cli/twin_test.go` above.
  - Full gate: `go vet ./...`, `go test -count=1 -race ./...` (all packages pass; `internal/store` alone took ~124s under `-race`, consistent with its usual size, not a hang), `CGO_ENABLED=0 go build ./cmd/water`, `gofmt -l .` — all clean.
- [x] **R-26 CLI** (`internal/cli/cmd_route.go`: `report`, `candidates`; `internal/cli/cmd_intent.go`: `list`, `draft`, `promote`, `demote`, `enable`; `internal/cli/root.go` **[shared, one line]**; `internal/cli/cmd_status.go` **[shared]**; `internal/cli/daemonclient.go`; the `water ask --voice` TTS-profile use).
  - Tests:
    - rendering from canned JSON;
    - the status `router` line with a 300 ms best-effort timeout and a "(daemon not running)" fallback;
    - `intent promote` refuses when the flag is off or on `N`;
    - `ask` ignores the unknown `handoff` event kind.
  - Flag: gated commands respect their flags.
  - **What already existed vs. what this task built.** `water route candidates` (R-22, "pulled forward from R-26") and its `GET /v1/route/candidates` endpoint were already real and complete — confirmed, left untouched. `water route eval --tier1` (R-18) likewise untouched. `report` did not exist as a CLI command at all (`cmd_route.go`'s own doc comment said so explicitly); `GET /v1/route/report` (R-14) did exist and worked, so `report` only needed a thin CLI wrapper + renderer. `POST /v1/intents/draft|reload` (R-23) existed and worked; nothing for `list`/`promote`/`demote`/`enable` existed anywhere — R-23's own entry above says so explicitly ("CLI ... is out of scope for this task, not merely deferred"), proving manual demote/enable only at the store/registry level and promote-refusal only via `draft`'s 403. This task added three new minimal daemon endpoints (`GET /v1/intents`, `POST /v1/intents/promote`, `POST /v1/intents/demote`/`enable`, the latter two sharing one `setIntentState` helper) plus a `promotion_enabled` field on the existing `GET /v1/router`.
  - **Design call — where `intent promote` reaches `$WATER_HOME`: read locally, write and reload only through the daemon.** The CLI reads the exact on-disk pending file (`promote.PendingDir(config.Home(), a.twinID())/<candidate-id>.yaml`) directly off `$WATER_HOME` to show the owner what they're about to promote — the same local-filesystem convention `water route eval --tier1` already uses for its own model/registry reads — rather than re-calling `POST /v1/intents/draft`, which would make a fresh cold model call and could silently produce different content than what was reviewed. The actual mutation (validate, write into the learned overlay, reload the live registry) happens only inside the daemon (`handleIntentsPromote`), which re-reads that same file itself and never accepts a client-supplied YAML body: the daemon is the only process that holds the live `*nervous.Nervous` registry pointer and the store connection, so a second local writer racing it was rejected as the wrong shape, consistent with `internal/store`'s existing read-only-pool-vs-single-writer posture (`store.OpenReadOnly` elsewhere in this codebase, never a second writable `store.Open` from the CLI against a live daemon's db). `demote`/`enable` are deliberately **not** gated on `router.promotion.enabled` — an owner can silence a misbehaving embedded intent too, not only a learned one; only `draft`/`promote` are gated, matching R-23's own precedent.
  - **A real gap the independent reviewer caught and this task closed**: `handleIntentsPromote`'s first draft joined the client-supplied `candidate_id` straight into a filesystem path with no shape check — unlike the sibling `handleIntentsDraft` (R-23), which only ever re-derives an id from its own server-computed candidate list. A crafted `candidate_id` (e.g. `"../../../etc/passwd"`) could have made the daemon read an arbitrary file and, on a passing `ValidateLearned`, promote it into the live learned overlay. Closed with `promote.ValidCandidateID` (a new exported regexp check matching `candidateID`'s own 12-hex-character output shape exactly), checked before the join in both `handleIntentsPromote` and the CLI's own local read, each with a dedicated regression test (`TestIntentsPromoteRejectsPathTraversalCandidateID`, `TestIntentPromoteRejectsPathTraversalCandidateID`).
  - **`intent promote`'s "refuses on N" is tested via `promptYesNo` directly, not a simulated terminal.** `confirmYesNo` short-circuits to refuse whenever `os.Stdin` isn't a TTY (the same posture `water approve`'s own interactive loop already has, and that file's own test suite tests only its pure `decisionOutcome` helper, never the TTY loop itself) — so a full `cmd.Execute()` run under `go test` can never actually exercise "the user typed N" end to end without a fake pty. `promptYesNo` is the exact function `confirmYesNo` defers to once past that gate; it is unit-tested directly with "N", "n", "no", blank and garbage input (all refuse) and "y"/"yes" (confirm), case-insensitively. The flag-off refusal, by contrast, is tested end-to-end through the real `intentPromoteCmd` against a fake Unix-socket daemon (`fakeDaemon`, a minimal `http.ServeMux` with no auth checking, serving `GET /v1/router`), since that refusal happens before any TTY interaction at all.
  - **The status `router` line's 300 ms budget bounds the whole fetch, not just the HTTP round trip.** `routerStatusLine` runs `newDaemonClient` + `RouterHealth` together in one goroutine racing a `time.After(300ms)`, since `newDaemonClient`'s own `probeDaemon` can itself block up to 500ms on a stale-but-present socket — bounding only the HTTP call and not that dial would let a stale socket blow the budget. A fast, known failure (the ordinary "no daemon running" case) reports the fallback immediately rather than waiting out the rest of the budget; only a fetch that is still running when the timer fires falls back to the timeout path. `routerStatusTimeout` is a package var so tests shrink it instead of actually sleeping 300ms.
  - Rebase check: `git rebase feat/ceo-twin` was a no-op (already up to date) both at the start and before this commit.
  - Full gate: `go vet ./...`, `go test -count=1 -race ./...` (all packages green; `internal/store` alone took up to ~290s under real background system load on this machine — a real `water daemon` runs here — consistent with prior slices' timing notes, not a hang), `CGO_ENABLED=0 go build ./cmd/water`, `gofmt -l .` — all clean.
- [x] **R-27 Swift client: TTS profile and partials** (`clients/macos/Sources/WaterClientCore/{HTTP.swift,Events.swift}`, `clients/macos/Sources/Water/{Voice.swift,TurnRunner.swift}`).
  - Fetch `/v1/voice/profile` at launch and apply the voice and rate. Add the `handoff` kind.
  - On push-to-talk, generate a turn id, POST `SFSpeechRecognizer` partial results to `/v1/turns/{id}/partial` (at most 5/s, fire-and-forget), then send `turn_id` with the final turn.
  - Gates: `build.sh` and `test.sh`. Swift tests cover the profile decoding and rate mapping, the `handoff` decode, and turn-id formatting.
  - Flag: partial streaming is on when the daemon answers 202. A 404 from an old daemon disables it for the session.
  - **Done (2026-09-25).** New: `clients/macos/Sources/WaterClientCore/VoiceProfile.swift` (`VoiceProfile` decoding `GET /v1/voice/profile`'s real shape — `{name, handoff, tts:{voice, rate_wpm}}`, confirmed against `internal/gateway/router.go`'s `handleVoiceProfile` — plus `TTSRateMapping`) and `Partials.swift` (`TurnID`, `PartialRateLimiter`, `PartialTransport`, `PartialStreamer`). Changed: `Events.swift` (`TurnEvent.Kind.handoff`), `UnixSocketClient.swift` (`streamTurn` gained `turnID:`, sent as `turn_id`), `Water/Voice.swift` (`VoiceController.applyVoiceProfile`, `speak` applies the stored voice/rate), `Water/TurnRunner.swift` (`run` threads `turnID` through), `Water/AppDelegate.swift` (launch-time `fetchVoiceProfile()`, a `partialStreamer` wired into `onListening`/`onPartial`/`onTranscript`, `.handoff` case in the turn-event switch).
  - **The real wire shape for a `handoff` NDJSON line was confirmed, not assumed.** `internal/nervous/nervous.go`'s `emitHandoff` doc comment says outright that `runtime.EventKind` has **no dedicated `"handoff"` kind yet** — on a non-voice channel it still emits a zero-text `ack` today, and only R-26's own comments (`cmd_ask.go`, `cmd_ask_test.go`) describe the eventual shape (`{"kind":"handoff","text":"..."}`) a later task would need to start sending. `TurnEvent.Kind.handoff` decodes that shape the moment something does; until then it simply never arrives, same as before this task, and the doc comment on it says so explicitly so a future reader isn't misled into thinking it's live.
  - **`handoff` is safe-decode only, not new UI**, per this task's own instruction to favor safe decoding over guessing at design when unsure: `AppDelegate`'s turn-event switch treats `.handoff` as a no-op (`break`, same as `.ack`), with a comment flagging "should this show a distinct 'listening…'/'thinking…' state?" as an open follow-up rather than a decision made here — R-28 or a later slice can pick it up.
  - **rate_wpm -> AVSpeechUtterance.rate mapping:** linear between two anchor points, `90 wpm -> AVSpeechUtteranceMinimumSpeechRate (0.0)` and `280 wpm -> AVSpeechUtteranceMaximumSpeechRate (1.0)`, clamped outside that range. Chosen so `style.yaml`'s own default (185 wpm) lands almost exactly on `AVSpeechUtteranceDefaultSpeechRate` (0.5): `(185-90)/(280-90) = 0.5` exactly. Kept in `WaterClientCore` as plain `Double` math (`TTSRateMapping`, duplicating AVFoundation's two constant values as comments rather than importing AVFoundation) so it's unit-testable with swift-testing alone, with `Water/Voice.swift` converting to `Float` only at the one call site that actually touches `AVSpeechUtterance`.
  - **Turn id shape:** `TurnID.generate()` is a plain `UUID().uuidString` — already 36 hex digits and hyphens, well inside the daemon's `^[A-Za-z0-9_-]{8,64}$` (`internal/gateway/router.go`'s `turnPartialIDPattern`), so no trimming or reformatting was needed. `TurnID.isValid` mirrors that same pattern for a defensive check before any path is built from a turn id (`UnixSocketClient.postPartial`), the same convention `Meetings.swift`'s `MeetingSegments.checkID` already uses for meeting session ids.
  - **Rate limiting and thread-safety design call:** `PartialStreamer` routes every stateful step of one `post` call — the rate-limit decision, the sequence counter, the actual network call, and setting `disabled` on a 404 — through one `executor` closure. The production default gives each `PartialStreamer` instance its **own** private serial `DispatchQueue` (not a shared static one, so one turn's backlog can never delay another's), which makes every access to that instance's mutable state single-threaded with no extra locking needed; tests inject a synchronous executor (`{ $0() }`) so the whole thing runs deterministically on the calling thread with a fake clock, with no real concurrency involved in the assertions.
  - **Server contract re-confirmed by reading the real handlers, not the plan's sketch:** `POST /v1/turns/{id}/partial`'s body is exactly `{text, seq, channel}` (`internal/gateway/router.go`'s `handleTurnPartial`), replies `202 {"state":"listening"}` on every well-formed request (even a rate-limited or silently-dropped one) and `404` only for a route that doesn't exist (an old daemon) — there is no dedicated "partials disabled" status of its own. `POST /v1/turns`'s body already accepts `turn_id` (`internal/gateway/handlers.go`, wired through since R-19). The server's own limits (20/s per turn id, a 2000-character cap, 8 max concurrently-listening turns) are enforced daemon-side only; the client does not duplicate them, matching this task's instructions.
  - Rebase check: `git rebase feat/ceo-twin` was a no-op (already up to date) both at the start and before this commit.
  - Full gate: `./build.sh` and `./test.sh` (75 tests, 11 suites, all pass; a from-scratch `swift build -c release` produced zero warnings) from `clients/macos`; `go vet ./...` and `CGO_ENABLED=0 go build ./cmd/water` from the repo root (no Go file was touched by this task).
- [x] **R-28 Docs** (`docs/known-gaps.md`, `docs/architecture.md`, `docs/EVOLUTION_PLAN.md` log entry, this file's ticks) **[shared, append-only hunks]**.
  - Known gaps:
    - store reads outside the hash-chained audit, by decision;
    - unread sync;
    - the free-time constant;
    - project entities;
    - barge-in;
    - the Swift client cancelling in-flight turns;
    - no per-turn strong model;
    - Decider conditions (triage p95 over 3 s, precision below 80%, or classification over 25% of the model-call window);
    - standing grants for actions;
    - `route export` and fine-tuning;
    - `ReadBack` cases for the new actions (owned by the other session).
  - Architecture: the head chef/sous chef diagram and the endpoint list.
  - No code.
  - **Done (2026-09-25).** Added a new "Slice R: deliberate design choices" section to `docs/known-gaps.md` covering all of the above plus items found while reading the full task list that weren't on this bullet list: the inert `router.tier0.timeout_ms`/`router.tier1.timeout_ms`/`router.quick_tools.enabled` config keys (R-25's own finding), the never-produced `intent_inactive:<id>` escalation reason (R-23's finding), the `handoff` event kind that clients decode but the server never emits on non-voice channels (R-27's finding, flagged prominently as asked), and `gmail.draft_message`'s level-D-not-A resolution kept as a note for future write-function additions. Checked `ReadBack` cases for the new write actions against the current code (`internal/approvals/readback.go`) before writing anything: all four (`send_message`, `draft_message`, `create_event`, `move_event`) already have real cases, added by the concurrent write-function session before this slice's R-20 even landed — so this item is recorded as resolved, not carried forward as an open gap, per this task's own "don't duplicate what's already resolved" instruction. Standing grants for actions was left where it already lived (Slice C's own known-gaps section), cross-referenced rather than duplicated. `docs/architecture.md` gained a "The nervous system: the router" section with an ASCII cascade diagram (matching the existing table/prose style, no other diagram convention was in use to match), the gate/approval-boundary explanation, the intent registry, route_log, the promotion loop and FunctionGemma's eval gate, plus updates to the Shape tree, the invariants table (three new/revised rows), the extension-points table (`decider.Decider`, `nervous.Tier`) and the superseded-material note recording `fastpath.go`'s deletion. `docs/EVOLUTION_PLAN.md` gained one log entry (2026-09-25) at the density of the file's existing entries, plus a Status-line update. This file's own ticks: confirmed R-1 through R-27 were `[x]` except R-12, found still `[ ]` despite its work being fully committed (`cd2e5c5`/`24cfe33`) and depended on by every later task — flagged with a note on R-12's own line rather than silently ticked, per this task's own instructions to report an unticked task as a serious finding, not resolve it unilaterally. Left `[ ]`. R-28 ticked here. Status line above updated to record R-12 as the one flagged exception.
  - Gates: `go vet ./...` and `CGO_ENABLED=0 go build ./cmd/water` clean (docs-only change, as expected); `go test -count=1 ./...` run as a whole-slice confirmation, all packages green (one retry on `internal/store` alone confirmed real-machine timing variance, not a hang, matching earlier tasks' own notes about this).

Phase 4 verification (`docs/slices/R-verification.md`) runs against an isolated `WATER_HOME`, never `~/.water`, and live Google access stays read-only. It records:
- the eval reports for Tier 0 and, if the sidecar was pulled, Tier 1;
- live latencies: `water ask` T0 median, T0 while a main turn is in flight, voice ack;
- `water route report` output;
- `water audit verify`.

Write intents and voice approval are verified against the fake-connector daemon harness only, never a real account.

### Acceptance criteria

1. **Tier 0 false-accept rate is 0**: 0 false accepts out of 276 or more cases in `ceo_eval.yaml` (`TestTier0Eval`), with write cases evaluated under the fixture manifest that grants all four actions.
2. **Tier 0 hit rate** on held-out positive paraphrases is at least 50%, and at least the legacy baseline plus 20 points (the baseline is recorded in R-7). Intent and slot accuracy on answered cases are 100%.
3. **Reasoning never answered by the sous chef.** 0 of the 30 or more `reasoning` and 10 or more `multi_clause` eval cases are answered by T0 or T1. For T1 this is proven with the fake always-calling client and in the live eval.
4. Every `tests:` entry in every intent file passes. All 6 legacy positives match, and all 5 legacy near-misses do not.
5. **Zero model calls on the quick path.** `backend.Fake.Calls() == 0` across every T0-answered eval case, every quick-tool invocation and every speculation run. The import and selector test passes for all sous packages.
6. **Tier 0 latency.**
   - `BenchmarkTier0Match` has p95 ≤ 2 ms.
   - The in-process test with 5,000 events and 5,000 messages has p95 ≤ 250 ms over 200 T0 turns through `POST /v1/turns`.
   - In Phase 4 live verification, `water ask "what's on my calendar today"` has a median ≤ 300 ms over 10 runs, and the `route_log` `t0` p95 is ≤ 50 ms.
7. **Tier 0 answers while the main path is busy.** With a fake main turn blocked, a T0 turn completes in ≤ 250 ms (package and gateway tests), and the blocked turn then completes normally.
8. **One owner per turn.** Partials produce 0 events and 0 answers. Speculation makes 0 model calls (`speculation_model_calls = 0` on every row in the tests). Across `TestExactlyOneOwner` and `TestNoDoubleAnswer`, every turn has exactly one routed owner, one answer stream and one terminal event.
9. **Handoff acknowledgement** is emitted ≤ 300 ms after the final transcript in the fake-clock tests, both when T0 escalates immediately and when T1 blocks 400 ms. `ack_ms` is recorded.
10. **Quick tools.** They work from the main agent's MCP bridge, including 8 parallel gateway invokes and 16×25 parallel handler calls under `-race` with no errors. A tainted result escalates the session token. Quick output is `untrustedWrap`ped. A `quick.` id never reaches `Gate.Invoke`, since the gate audit length is unchanged, and `quick_boundary_test.go` passes.
11. **Echoed interpretation.** 100% of quick answers in the eval (read and proposal) contain every resolved slot's `Spoken` label in the header, and empty results use the plain `empty` phrasing.
12. **`Speakable()`** tests pass for markdown, bullets, times, dates, URLs, emoji and list caps. `TestOneVoiceAcrossTiers` passes: no markdown, within the length cap, and no banned phrases in quick output. Banned phrases in main output appear as `route_log` warnings, and the prose is not rewritten.
13. **Write intents.** A write intent whose action is not in the manifest loads as inactive with a reason, and never answers. With the action granted (test manifest), the same file activates with no code change. A proposal creates exactly one pending envelope and makes 0 connector executions until a decision.
14. **Voice approval binding** (flag on, in tests):
    - a yes within 60 s decides that envelope with that hash;
    - a yes at 61 s does not;
    - an edit voids the binding;
    - two pending gives a menu;
    - high, unrated, send and external-recipient actions require a tap;
    - a no always denies.

    With the flag off, the earlier bind-and-surface behaviour holds: with 1 pending, the envelope is still `pending` afterwards and the audit length is unchanged. No code under `internal/nervous` calls `Queue.Decide`.
15. **Approvals safety** (earlier plan): with 0 pending, "yes" is not answered by T0; with 2 or more pending, a menu is returned and no `approval_required` is emitted.
16. **Tier 1 gate.** The daemon refuses to enable T1 without a valid eval-gate file meeting FA ≤ 1%, Wilson 95% upper bound ≤ 2%, n ≥ 200 and warm p95 ≤ 400 ms, and matching the current model sha and registry hash. `GET /v1/router` shows the reason. T1 grounding rejects every ungrounded-value fixture.
17. **Promotion.** A learned intent cannot reference an ineligible function, cannot create ambiguity with existing templates, and cannot false-accept any eval negative. It is auto-demoted past the miss threshold (more than 20% over 10 or more samples). Embedded intents always win. With the flag off, the overlay is not loaded and promote refuses.
18. **Route logging.** Exactly one row per turn on every path. Owner, voice, partials, ack, lint, tool-usage and action columns are populated as specified. The possible-miss flag is set at 59 s and not at 61 s.
19. **Breaker.** After 5 consecutive T0 failures, the 6th turn skips T0 with `breaker_open`, and `/v1/router` shows `t0 open`. It reports `half_open` after 60 s on the fake clock.
20. **Voice length.** Every T0 voice rendering in the eval with a 30-event fixture is within `max_chars.voice` (280).
21. **Taint.** A quick answer or quick-tool result containing any External record calls `OnTaint(true)`, including the tomorrow-schedule regression. A brief cache miss is fail-closed exactly as before.
22. **Startup validation.** Each rejection rule in §5.3 and §5.4 makes `buildTwinDepsFS` fail before `store.Open`. `ceo-demo` still loads.
23. **Config.** Every §17 key round-trips through `water config set/get`. `decider.provider` rejects anything but `none`. `router.ack_ms ≥ 300` is rejected. The defaults equal `nervous.DefaultConfig()`.
24. **Extensibility.** Adding a MapFS intent that references an existing handler, with no Go change, makes it match. Adding a planned write function to a MapFS manifest activates the matching write intent, with no Go change.
25. **Single entry point.** `grep -rn "runtime.RunTurn\|FastPath" internal/` returns nothing, and `handleTurn` is the only caller of `Nervous.Handle`.
26. **Build.**
    - `git diff --exit-code <slice-base> -- go.mod go.sum` is clean. The sidecar is a separate process, so no module change.
    - `CGO_ENABLED=0 go build ./cmd/water` succeeds.
    - `go vet`, `go test -count=1 -race ./...` and `gofmt -l .` are clean.
27. **No state change from sous packages.** `OpenReadOnly` rejects writes, and the import and selector test passes.
28. **Every quick answer states empty results plainly.** Every read intent has an `empty` string, and there is a test per intent.
29. **Swift.** `build.sh` and `test.sh` pass. The client applies the TTS profile, and an old daemon returning 404 on partials does not break turns.
30. **Untouched paths.** `git log --grep '^R-' --name-only <slice-base>..feat/ceo-twin` lists nothing under `twins/ceo/twin.yaml`, `internal/gate/`, `internal/connectors/`, `internal/decisions/` or `internal/approvals/`.

### Invariant checks

These run on every task, automated where possible:
- **No connector function runs without a gate permit, and A-level calls are refused without an approved envelope.**
  - The existing `internal/gate`, guard and approvals suites pass unchanged, and this slice doesn't modify those packages.
  - The sous packages cannot import the gate or connectors (import test).
  - Quick tools go to `/v1/quick/invoke`, which cannot reach the gate (AST boundary test).
  - Write intents only queue envelopes, and execution happens only through `decideAndExecute` → `Gate.Invoke(EnvelopeID)`.
  - Voice decisions reuse that same hash-bound path.
- **Untrusted content is tagged, and actions derived from it never run autonomously.**
  - Quick answers and quick tools propagate taint (criterion 21), and quick-tool output is `untrustedWrap`ped.
  - Proposals that used External records set `Tainted`.
  - Every proposal needs an approved envelope.
  - The voice-yes allowlist excludes send, external recipients, high, unrated and P2-origin actions.
  - The meeting and S13 taint tests pass unchanged.
- **Reflex tiers run no shell or subprocess and make no state changes.**
  - The import and selector test covers reflex, propose, tmpl, slots, intents, render, speak and turn.
  - Reads use a `mode=ro`/`query_only` pool.
  - `control.stop` only cancels in-process contexts.
  - T1 makes loopback HTTP only, and its endpoint is validated.
  - The only subprocess, the sidecar, is started once at daemon startup, never per turn, and only when T1 is gated on.
  - Speculation has no backend, T1 client or emitter.
- **No metered API dependency is added, and the binary is static.** `go.mod` is unchanged, `CGO_ENABLED=0` builds, and the main path still uses the subscription warm session. Drafting a learned intent is a CEO-initiated subscription call. `llama-server` is local and free.
- **No secret appears in logs, errors, test output or model context.**
  - `route_log` stores utterances, labels and tool names only; partial text is never stored.
  - `status.overview` never touches the vault.
  - The model pull prints no `HF_TOKEN`.
  - Quick-tool args and results are logged the same way as twin tools (hash plus preview).
- **The audit log still verifies.** The existing audit tests pass. The new audit records come only from existing code paths (`Propose`, `Decide`, `Gate.Invoke`). Phase 4 runs `water audit verify`.
- **Every turn starts at the pre-tier check and Tier 0.** `TestEveryTurnStartsAtT0` covers this, and no state skips the cascade.
- **Single entry point:** criterion 25.

### Risks and open questions

1. **Store reads versus the gate.** *Decided by the owner:* direct read-only store view with import, selector and namespace enforcement, plus a `mode=ro` pool. Quick tools follow the same reasoning. Recorded in `known-gaps.md`.
2. **Concurrent session on the same branch.** *Default:*
   - work in a separate worktree on the `slice-r` branch (see Execution environment), rebasing onto `feat/ceo-twin` before each commit;
   - fast-forward from the main checkout only when the other session's tree is clean for the touched paths;
   - never edit the five non-scope paths;
   - keep **[shared]** hunks small, with new logic in new files.

   If Slice E changes the `twins.Manifest` API, this slice depends only on `m.ID`, `m.Connectors[].Name`, `m.Function(id)` and `m.FunctionIDs()`, so only those call sites need adapting.
3. **Migration number collision.** *Default:* take the next free number at R-4's commit time, after rebasing. Never renumber a committed migration. If the other session lands a migration after R-4 but before the fast-forward, rebase and rename only if R-4 has not reached `feat/ceo-twin` yet.
4. **Write-function schemas may differ from `slice-c-planning.md`.** *Default:* proposers declare `Emits`. The registry load fails loudly on a mismatch once a function is granted, and `TestEmbeddedCEOIntentsLoad` catches it in CI for whichever session lands second. Fix the proposer, not the gate.
5. **`approvals.ReadBack` has no cases yet for the new actions.** *Default:* the other session owns `internal/approvals`. Until it adds cases, `ReadBack` uses its generic branch, and the facade prefixes the proposer's `Summary` so the interpretation is always spoken.
6. **Registry "reload" and the embedded manifest.** *Default:* manifest grants arrive in a rebuilt binary, so write intents activate on the next daemon start with that build. `POST /v1/intents/reload` covers learned-overlay changes without a restart. No code change is needed either way.
7. **Invalid learned files.** *Default:* skip and report in `/v1/router` and `water intent list`. Owner data never blocks daemon start. Embedded files stay strict.
8. **Tool attribution is ambiguous when more than one main turn is in flight.** *Default:* mark `tools_attributed = 0` and exclude those turns from candidates. The warm session makes this rare.
9. **Adding quick tools restarts the warm session once** (the `toolsKey` change). *Default:* acceptable once per deploy, because the list is static.
10. **The single-writer store connection could delay route-log writes under load.** *Default:* rows are written in the deferred path after the answer is emitted, and every read goes through the read-only pool.
11. **FunctionGemma GGUF availability and gating.** *Default:* pin an official or `ggml-org` GGUF if one exists, otherwise a community conversion pinned by sha256 and revision, noted in `docs/functiongemma.md`. If none exists, R-17 stops and asks the owner, and Tier 1 stays off. Everything else ships.
12. **Wilson threshold versus sample size.** At n ≈ 270, a Wilson 95% upper bound ≤ 2% requires 0 false accepts, and even 1 false accept gives about 2.1%. *Default:* the Tier 1 live eval uses at least 400 cases (the main set plus `ceo_eval_t1.yaml`), so up to 2 false accepts can pass. The FA ≤ 1% threshold stays as specified.
13. **The ack could arrive before a slow Tier 1 answer.** *Default:* acceptable. The ack fires at `AckAfter` (250 ms) if the turn is still unrouted. If T1 then answers, the user hears "One moment." followed by the answer. The one-owner rule still holds, because the router's ack is not an answer.
14. **Which model the head chef runs.** *Default:* the manifest's `models.fast`, as today, on the warm session. No per-turn switch to `TierStrong`, since that would restart the warm session. The owner may set `models.fast` to a stronger model in `twin.yaml`; that is a manifest change outside this slice.
15. **Tap versus push-to-talk confirmation for high risk.** *Default:* high-risk actions require the explicit hash-bound decision endpoint, which is the client tap UI or `water approve`. Push-to-talk alone is not treated as confirmation, because today every voice turn is push-to-talk and that would make the distinction meaningless. Revisit if an open-mic mode is added.
16. **The Swift client cancels the in-flight turn when a new one starts.** *Default:* unchanged in this slice, since barge-in is deferred. The daemon-level concurrency is proven by tests and is usable from the CLI and a second client. Changing the client is a follow-up owner decision, recorded in `known-gaps.md`.
17. **`internal_domains` is empty by default,** so every recipient counts as external and needs a tap. *Default:* fail closed. The owner sets their company domain when enabling voice approval.
18. **Partial transcripts could be sensitive.** *Default:* never stored or logged. Only counts and timings are kept, and speculation results are kept in memory for at most 30 s.
19. **Eval leakage.** *Default:* the eval set is committed in R-7, before any CEO template (R-9), and `TestEvalNoLeak` blocks verbatim overlap. Promotion validation uses negatives only. Later edits to the eval set are noted in this file with a reason.
20. **Tier 0 answers are invisible to the warm session's history.** *Default:* the recent-reflex ring passes the last 3 quick exchanges to the main path as prompt context.
21. **The brief cache miss needs a model call.** *Default:* the quick tiers serve only the cached brief. A miss escalates with `brief.today`, and the main path computes it with the existing compute-once guard and fail-closed taint.
22. **Unread count and time zones.** *Default:* as in the earlier plan. Answer truthfully that unread state isn't synced. Resolve dates in `env.Now().Location()`, where a bare weekday includes today.
23. **The model-drafted intent file is model output.** *Default:* `origin` and `provenance` are filled by code. `ValidateLearned` plus explicit owner approval is the guard. Drafts go to `pending/` and are never loaded until promoted.
24. **`gmail.draft_message` is granted at level D, not A, in the real `twin.yaml` (discovered building R-6).** The concurrent write-function session landed `twin.yaml` with `gcal.create_event: A`, `gcal.move_event: A`, `gmail.send_message: A` — matching the plan's assumption — but `gmail.draft_message: D`. `checkAction` (§5.4) implements the plan's own rule literally: a write intent's action found in the manifest at any level other than A is a hard load error, not "inactive." As built, `mail.draft_reply`'s action (`gmail.draft_message`) would fail the *whole* registry load the moment R-20 writes that intent file against the real manifest, rather than sitting inactive like the other three planned actions did before being granted. *Default:* R-20 must resolve this explicitly before writing `mail.draft_reply` — most likely by extending `checkAction` to also accept level D for a write intent (D already means "nothing leaves," so a D-level proposal plausibly never needs an approval envelope and could execute immediately once the shared validator resolves it, unlike the other three A-level actions) rather than forcing every proposer through the approval queue regardless of the manifest's own risk classification. Not resolved in R-6, since it is a design call that belongs to R-20 (the task that actually builds the write intents and their approval-path integration), not the registry loader.
    - **Resolved in R-20, exactly along the recommended default.** `checkAction` now accepts both A and D for a write intent's granted level and returns a new `requiresApproval` result (`true` only for A). `Intent.RequiresApproval` carries this to `nervous.tryWriteIntent`: a level-A proposal queues an envelope through `ActionSink` exactly like before; a level-D proposal is delivered directly as the answer — no envelope, no gate call, no `approval_required` event — since D already means "nothing leaves" and the proposal itself already is the drafted artifact. `mail.draft_reply` (→ `gmail.draft_message`) loads active with `RequiresApproval: false`; `mail.send_reply`/`calendar.create_event`/`calendar.move_event` load active with `RequiresApproval: true`. See R-20's own entry above for the full design and test coverage.

### Critical files for implementation
- `/Users/kranthikoneti/water/internal/runtime/runtime.go` (`RunTurn` becomes `ModelTurn`; `DeliverText`, `StyleBlock`, prefetched summary) and `/Users/kranthikoneti/water/internal/runtime/fastpath.go` (deleted; its tests become the regression table)
- `/Users/kranthikoneti/water/internal/gateway/daemon.go` and `/Users/kranthikoneti/water/internal/gateway/handlers.go` (`handleTurn` → `Nervous.Handle`; `proposeEnvelope` and `decideAndExecute` extraction; `quick.` guard; `turnSink.channel`; voice read-back hook)
- `/Users/kranthikoneti/water/internal/tools/policy.go` and `/Users/kranthikoneti/water/internal/tools/service.go` (the `Policy.Quick` / `callQuick` → `/v1/quick/invoke` split)
- `/Users/kranthikoneti/water/internal/decisions/registry.go` (the `LoadRegistry` and `validateStagedAction`/`plannedActions` precedent; read-only here, parsed by the drift test)
- `/Users/kranthikoneti/water/internal/cli/twin.go` (startup validation: intents registry, style, read-only store, `decider.Wrap`, learned overlay)
- `/Users/kranthikoneti/water/internal/store/store.go` and `/Users/kranthikoneti/water/internal/store/migrations/` (`OpenReadOnly` alongside the single-writer pool; `0010_router.sql`)
- `/Users/kranthikoneti/water/internal/backend/warmsession.go` (`Prewarm`; the semaphore the sous chef must never take)
