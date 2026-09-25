# water — architecture notes

Water is a single personal digital twin (the CEO twin), not a multi-role
council. The four-role council, its persona/identity/orchestrator stack, and
the terminal-only TUI described in earlier versions of this file are
deleted, not dormant — see `docs/CONTEXT.md`'s amendment and
`docs/EVOLUTION_PLAN.md`'s keep/move/retire table. This file describes what
actually exists after A1–A4.

## Shape

```
water daemon                 # HTTP over a 0600 Unix socket, single-instance
  gate/       permission gate: manifest levels, rate/usage caps, taint
  approvals/  envelope queue: payload-hash binding, code-generated read-back
  audit/      append-only, hash-chained log; fails closed
  store/      SQLite (modernc.org/sqlite), normalized record types
  connectors/ one package per tool + the connector contract (google/*, fake)
  runtime/    context assembly, model-call streaming (runtime.ModelTurn), brief
  nervous/    the router: intent registry, Tier 0/1, quick tools, route log,
              write-intent proposals, voice approval binding, promotion loop
  gateway/    the daemon itself: HTTP handlers, model-tool bridge, sync
  sync/       background refresher (P2, two tickers: mail, events)
  tools/      MCP bridge the model's subprocess talks to (twin + quick tools)

water chat | ask | approve | connect google | daemon ...   # CLI clients
```

Runtime data lives outside the repo in `~/.water/` (SQLite db, audit log,
Unix socket). Secrets live in the macOS Keychain via `internal/vault`,
reached through `/usr/bin/security`, never in files or model context.

## Invariants enforced in code

| Invariant | Where enforced | Guarded by |
|---|---|---|
| A tool exists for the twin only if the manifest lists it at a stated level (default deny) | `twins.Manifest.Function`, `gate.authorize` | `TestEmbeddedCEOManifestLoads`, `TestEmbeddedCEOManifestBuildsAGate` |
| A manifest may only tighten a function's declared level (to A or B), never loosen it | `gate.New` | gate package tests |
| A connector can get its arguments/credential only by redeeming a one-time permit; only the gate can mint one | `permit.Permit.Open`, `gate/internal/mint` (import-restricted by Go's `internal/` rule) | a guard test that scans source to confirm only the gate imports the mint or calls `Invoke`, and every connector redeems as its first statement |
| Level A always needs an approved envelope; level S needs one whenever taint isn't Clean | `gate.NeedsEnvelope`, `gate.authorize` | gate package tests |
| Approvals bind the sha256 of the canonical payload; a mismatched call voids the approval; an edited envelope is a new hash | `approvals.PayloadHash`, `approvals.Queue.Decide`/`Edit` | approvals package tests |
| The spoken/read read-back is built by code from the structured payload that the hash binds — never by the model | `approvals.ReadBack` | approvals package tests |
| Every audit write happens before the effect it records; an audit failure fails the action; an approval that can't be audited reverts to denied | `gate.Invoke`, `approvals.Queue` | audit + gate package tests |
| `audit.Open` refuses to extend a chain that doesn't verify | `audit.Open` | audit package tests |
| External content is untrusted: any function marked `External: true` taints the session, sticky and never reset within it | `gateway.handleToolInvoke` → `escalateTaint` | A3 taint-fix tests |
| Auto mode (P2) never performs an outward action: only functions on `auto_allowlist`, and only at level R or D | `gate.authorize` (`c.Origin == P2` branch) | gate package tests; see `docs/slice-c-planning.md` for why this stays strict into Slice C |
| Rate and usage windows persist in the store, so caps survive a daemon restart | `gate.take`/`takeStoreLocked` | gate + store package tests |
| Metered model calls never run while the subscription CLI is available; metered keys never reach subprocesses | `backend.Select`, `backend.ScrubbedEnv` | backend package tests |
| Tier 0/Tier 1 answer schedule/approvals/brief/mail questions from the store with zero model calls, except the one documented cached-brief exception; quick tools and speculation make zero model calls too | `internal/nervous/{tier0.go,tier1.go,speculate.go}` | `TestTier0Eval`, `TestSpeculationZeroModelCalls`, the sous-package import/selector denylist test |
| The sous packages (`reflex`, `propose`, `tmpl`, `slots`, `intents`, `render`, `speak`, `turn`) can reach the store only through a read-only connection, never the gate or a connector, and make no mutating calls | `store.OpenReadOnly`; import/selector AST tests | `internal/nervous/reflex/imports_test.go`, `internal/nervous/decide_test.go` |
| A write-intent proposal only ever queues an envelope (level A) or delivers a drafted artifact directly (level D); execution happens only through the one hash-bound `decideAndExecute` → `Gate.Invoke` path, identical to a model-initiated tool call | `internal/nervous/actions.go`, `internal/gateway/actions.go` | `actions_test.go`, `voiceapprove_bind_test.go` |
| A background sync tick with no stored credential skips quietly (one log line), never spams denials | `internal/sync` | sync package tests |

## One conversation turn

Every turn from every channel (CLI, hotkey text-bar, voice, meeting mode)
goes through exactly one entry point: `gateway.handleTurn` →
`nervous.Handle`. There is no parallel fast-path anymore — `runtime.RunTurn`
and `internal/runtime/fastpath.go` were deleted in Slice R and replaced by
the router (`internal/nervous`) described below. `handleTurn` remains the
only caller of `Nervous.Handle`.

1. A client sends a turn over the daemon's Unix socket (`POST /v1/turns`,
   optionally preceded by speculative partials at `POST /v1/turns/{id}
   /partial`).
2. `nervous.Handle` opens the turn (turn state machine: `listening → final →
   routed(quick|main) → done`), emits an `ack`, and starts a handoff-
   acknowledgement timer (default 250ms).
3. **Eligibility.** A cheap, code-only check (too long, an escalate word
   like "should"/"why"/"compare", a multi-clause utterance) sends anything
   that isn't a clean, single-intent question straight to the main path —
   the sous chef never ranks or judges.
4. **Tier 0** (deterministic templates) tries to match an intent from the
   registry with zero model calls. A match routes the turn to the quick
   owner.
5. **Tier 1** (FunctionGemma, disabled by default) only runs when Tier 0
   found no match, and only when its live-eval gate has passed.
6. Whichever quick tier matches, the turn is routed to the **quick owner**
   and answered from the store — a read intent renders a phrased answer; a
   write intent builds a proposal and either queues an approval envelope
   (level A) or, for a level-D action, delivers the drafted artifact
   directly.
7. If neither quick tier matches (or eligibility already ruled it out), the
   turn routes to the **main owner**: `runtime.ModelTurn` over the warm
   `claude` subprocess, with quick tools available as MCP tools and
   connector tools reaching the gate exactly as before. The main path's
   replies stream sentence by sentence.
8. Any connector call the main path makes crosses the MCP bridge
   (`internal/tools`) to the daemon's gate, which authorizes, executes,
   audits and normalizes the result into the store (UNTRUSTED-wrapped when
   the function is `External`). A call that needs an envelope (level A, or
   S while tainted) is staged into the approval queue instead of executing;
   `water approve`, or (when `router.voice_approve.enabled`) a spoken
   yes/no bound to the read-back's payload hash, decides it. Deciding "yes"
   executes the action exactly once, in that same call.
9. Exactly one route_log row is written per turn, in a deferred step that
   also runs on panic or cancellation.

See "The nervous system" below for the router's internal shape, the intent
registry, and where the approval boundary actually sits.

## The nervous system: the router (`internal/nervous`)

Slice R replaced the old three-branch, hardcoded fast path with a real
router: a fixed, audited table of deterministic and small-model tiers (the
**sous chef**) in front of the full model (the **head chef**). The sous
chef only ever proposes an answer from a fixed handler table or escalates —
it never reasons, never ranks, and never calls a connector directly.

```
                         nervous.Handle(turn)
                                │
                     ┌──────────┴──────────┐
                     │   Eligible(turn) ?   │  too_long / escalate_word /
                     │  (pure, no model)    │  multi_clause -> skip to main
                     └──────────┬──────────┘
                     yes        │        no
          ┌──────────────────────────────────────────┐
          │                                            │
          ▼                                            │
   ┌─────────────┐   no match    ┌─────────────┐       │
   │   Tier 0     │──────────────▶│   Tier 1    │       │
   │  templates   │  (t0 only)    │FunctionGemma │       │
   │ (in-house Go)│               │ (off by      │       │
   │ 0 model calls│               │  default,    │       │
   └──────┬───────┘               │  eval-gated) │       │
          │ match                 └──────┬───────┘       │
          │                    match      │  no match /   │
          │                               │  escalate      │
          ▼                               ▼                ▼
   ┌────────────────────────────┐  ┌─────────────────────────────┐
   │        QUICK OWNER          │  │          MAIN OWNER          │
   │  read intent -> render      │  │  runtime.ModelTurn over the  │
   │  answer from the store      │  │  warm claude session, quick  │
   │                              │  │  tools + connector tools     │
   │  write intent -> proposal:  │  │  available via MCP           │
   │   level A -> queue envelope │  └──────────────┬────────────────┘
   │   level D -> deliver draft  │                 │ model calls a
   │       (both via the SAME    │                 │ connector tool
   │        proposeEnvelope path)│                 ▼
   └──────────────┬───────────────┐        ┌───────────────────┐
                  │ level A only   │        │   internal/gate    │
                  ▼                └───────▶│  authorize, permit, │
         ┌──────────────────┐              │  audit, execute      │
         │ approvals.Queue   │◀─────────────┤  (level A/S+taint    │
         │ (envelope, hash-  │  same path   │   needs an envelope) │
         │  bound read-back) │              └───────────┬───────────┘
         └─────────┬─────────┘                          │
                    │ water approve /                    │
                    │ voice yes-no bound to the hash      │
                    ▼                                     ▼
             decideAndExecute ──────────────────▶ Gate.Invoke(EnvelopeID)
```

**The gate/approval boundary, made explicit:** Tier 0, Tier 1 and quick
tools never reach `internal/gate` directly — they only ever read the store
through a read-only connection (`store.OpenReadOnly`) or, for a write
intent, hand a typed payload to the gateway's `ProposeAction`. Only two
things ever reach the gate: a write-intent proposal at level A (through
`proposeEnvelope`, queuing an envelope) and a model-initiated tool call
from the main path (through the identical `proposeEnvelope` extraction) —
both funnel into the same envelope queue and the same `decideAndExecute` →
`Gate.Invoke(EnvelopeID)` call once approved. A level-D write intent (today
only `gmail.draft_message`) skips the envelope entirely and is delivered
directly as the answer, since level D already means "nothing leaves."

**Quick tools** (`quick.*`, exposed to the head chef as `quick__*` MCP
tools) are the sous chef's own read-only handler table, reused as a
toolbox for the main agent. They are served on a separate daemon endpoint,
`POST /v1/quick/invoke`, whose handler is looked up only in
`reflex.Table()` — never in `connectors.Registry` — and cannot reach the
gate at all (enforced by an AST boundary test, not just convention).

### The intent registry

`twins/ceo/intents/*.yaml` (plus `_shared.yaml` for shared vocabulary:
skip/deny/escalate words, clause joiners) holds one file per intent,
validated at startup the same way `twins.Load`/`decisions.LoadRegistry`
validate their own files — strict decoding, and any bad file fails the
whole load. Two kinds:

- **Read intents** (12 shipped) target a reflex handler
  (`internal/nervous/reflex`) and only ever read the store.
- **Write intents** (4 shipped: create/move a calendar event, draft/send a
  reply) target a `connector.function` and only ever build a proposal. A
  write intent whose target function isn't granted yet in `twin.yaml` loads
  as **inactive** (not a load error) and sits in `Registry.Shadow()` —
  it activates automatically, with no code change, the next time the
  daemon starts with a manifest that grants it.

An optional **learned overlay** (`$WATER_HOME/twins/<id>/intents/learned/
*.yaml`, loaded only when `router.promotion.enabled`) adds intents the
promotion loop drafted and the owner approved; an embedded id always wins
on collision, and a malformed overlay file is skipped and reported, never
fatal to daemon startup.

### Route log and reporting

Migration `0010_router.sql` adds `route_log` (one row per turn: channel,
utterance, which tiers were attempted, who answered, the matched intent
and its origin, resolved slots, escalation reason, per-tier latency,
outcome, lint warnings, voice/partials/ack/first-sentence timing, tool
usage and attribution, and the resulting action/decision) and
`intent_state` (per-learned-intent enable/disable, for the demotion
breaker). `water route report` and `GET /v1/route/report` summarize it:
owner/tier splits, escalation-reason histograms, latency percentiles,
possible-miss counts, and quick-tool usage. `water route candidates` and
`GET /v1/route/candidates` read it directly to find promotion candidates,
and work even with the promotion loop turned off.

### The promotion (growth) loop — `router.promotion.enabled`, off by default

Growth here means new phrasings and bindings over the existing, audited
handler set — it never means a new capability. Candidates (repeated
main-path turns that only ever called quick, learnable tools, at least 5
times over at least 2 distinct days) are detected from `route_log`;
`water intent draft <candidate>` makes one CEO-initiated, cold model call
to write a proposed intent YAML into a `pending/` directory; `Validate
Learned` rejects it if it's ambiguous with an existing template, would
false-accept any held-out eval negative, targets anything but a read-only,
deterministic, side-effect-free handler, or exceeds the learned-intent cap
(20); `water intent promote <id>` then requires explicit owner
confirmation before the file moves into the live learned overlay. A learned
intent that starts missing too often (over 20% possible-miss rate across at
least 10 samples) is auto-demoted — disabled immediately, without a
restart. The whole loop is off by default and every promotion is a single,
individually-approved step; nothing here ever promotes a *write* intent.

### FunctionGemma (Tier 1) — `router.tier1.enabled`, off by default

A 270M-parameter function-calling model (`google/functiongemma-270m-it`)
served by a Homebrew `llama-server` sidecar the daemon supervises as a
local subprocess — not a Go module dependency, no cgo, never linked into
the binary. It only ever proposes a call from the same audited intent
table Tier 0 uses (grounded against the literal utterance, with the
model's own confidence ignored), and only for intents marked
`reflex_eligible`. The daemon will only start the sidecar and enable Tier 1
when **all** of: the config flag is on, the pinned model file is present
on disk, and a recorded live eval (`water route eval --tier1`) shows a
false-accept rate ≤ 1%, a Wilson 95% upper bound ≤ 2%, at least 200 (in
practice ≥ 400) cases, and a warm p95 latency ≤ 400ms — all matching the
current model sha256 and the current registry hash. `GET /v1/router`
reports which of those checks is failing when Tier 1 stays off.

Run for real during Slice R's Phase 4 verification (`docs/slices/
R-verification.md`): a live `water model pull functiongemma
--accept-gemma-terms` plus `water route eval --tier1` against the real
Homebrew `llama-server` sidecar surfaced that `llama-server` never
populates the OpenAI-style structured `tool_calls` field for this model —
the parser only read that field, so every real answer was silently
discarded and Tier 1 had never actually answered anything. Fixed (a raw
`<start_function_call>...` text-format fallback parser, plus a
request-level stop sequence that also cut latency 7-10x). The honest
post-fix numbers still fail the gate — a real 2.2% false-accept rate
(Wilson 95% upper 4.1%) and 558ms warm p95, both outside threshold — so
Tier 1 correctly stays off, now for legitimate reasons. See
`docs/known-gaps.md`'s Slice R section for the full story and what a
follow-up pass would need to try (a Metal-accelerated build; this Mac's
Homebrew bottle is CPU-only).

## Background plane

`internal/sync` runs two independent tickers tied to the daemon's shutdown
context: a mail ticker (default 60s, Gmail history-based incremental fetch)
and an events ticker (default 10m, Calendar sync-token incremental fetch;
also drives the morning-brief background precompute once past
`brief.ready_after`). Both call through the gate at origin P2 with Clean
taint, and both skip quietly when no Google credential is stored.

## Extension points

| Interface | Implementations | Add by |
|---|---|---|
| `backend.Backend` | claude-subscription, codex-subscription, api (opt-in, off by default) | new file + `backend.Default.Register` |
| `connectors.Connector` | fake, google/{gcal,gmail,gdrive} | a new package implementing the interface, registered in the twin's registry |
| `voice.Provider` | os (free, default), openai (opt-in metered) | `voice.Register` |
| `twins.Manifest` loader | ceo (embedded `twins/ceo/twin.yaml`) | a new twin directory; Slice E generalizes this to more than one twin |
| `decider.Decider` | `Null` (default; `decider.provider=none` is the only accepted value) | `internal/decider`; see `docs/known-gaps.md` for when a real adapter is warranted |
| `nervous.Tier` | `tier0` (templates), `tier1` (FunctionGemma, gated off by default), `main` (the warm session) | a new file in `internal/nervous` implementing `Tier`, wired into `Handle`'s cascade |

## Exit codes

0 ok · 1 error · 2 usage/prerequisite · 3 backend unavailable or metered
refused · 4 unconfigured · 5 interrupted by a subscription rate limit
(resumable)

## Superseded material

The hierarchy-router orchestration model, the persona/identity/skills
stack, the interactive local-workspace `apply_actions` approval flow, and
the terminal TUI's theme/layout engine described in earlier revisions of
this file no longer exist in the tree. They are not archived as design
docs — anything not kept survives only in git history and in
`docs/archive/*` for the adjacent prose docs (decisions, tools, voice,
judgment-pass, persona-audit, reasoning-layer prompts) that described them.

`internal/runtime/fastpath.go` (the three-branch, hardcoded schedule/
approvals/brief matcher described in earlier revisions of this file) is
gone as of Slice R, replaced end to end by `internal/nervous`'s registry-
driven Tier 0/Tier 1 cascade described above. `runtime.RunTurn` was renamed
`runtime.ModelTurn` and is now the main-path-only primitive `nervous`
calls; it is never called directly by a channel handler.
