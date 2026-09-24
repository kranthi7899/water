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
  runtime/    context assembly, fast path, model-call streaming, brief
  gateway/    the daemon itself: HTTP handlers, model-tool bridge, sync
  sync/       background refresher (P2, two tickers: mail, events)
  tools/      MCP bridge the model's subprocess talks to (twin mode only)

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
| The fast path answers schedule/approvals/brief questions from the store with zero model calls, except the one documented brief exception | `runtime.fastpath.go` | runtime package tests |
| A background sync tick with no stored credential skips quietly (one log line), never spams denials | `internal/sync` | sync package tests |

## One conversation turn

1. A client (`water chat`/`ask`, later the Swift app) sends a turn over the
   daemon's Unix socket.
2. `runtime` assembles context: `twins/ceo/role.md` plus a state summary from
   the store. The fast path answers directly (no model call) for a handful
   of deterministic question shapes (schedule, pending approvals, morning
   brief cache hit); everything else goes to the model.
3. A warm `claude` subprocess (one per daemon, restarted on crash/scope
   change) streams the reply sentence by sentence back to the client.
4. If the model calls a connector tool, the call crosses the MCP bridge
   (`internal/tools`) to the daemon's gate, which authorizes, executes,
   audits, and normalizes the result into the store before returning it
   (wrapped in UNTRUSTED markers when the function is `External`).
5. A call that needs an envelope (level A, or S while tainted) is staged
   into the approval queue instead of executing; `water approve` (or, once
   Slice B ships, a spoken yes/no) decides it. Deciding "yes" executes the
   action exactly once, in that same call.

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
