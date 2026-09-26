# Water — context primer

This is a self-contained architecture and context document for **Water**, a personal AI project belonging to Kranthi. It's meant to be uploaded as project knowledge into a Claude app/project so a fresh conversation has real grounding — everything here should make sense without access to the actual codebase.

## What Water is

Water is a runtime for a **digital twin**: one AI agent that serves one specific person (Kranthi) in a CEO-like role, with real access to his actual workflow — calendar, email, docs, code hosting (GitHub), issue tracking (Linear), CRM (HubSpot) — gated behind an explicit permission system, not a general-purpose chatbot and not a multi-agent "council." It started as a four-role council (CEO/COO/CTO/etc.) and was deliberately rebuilt into a single twin; the old roles were fully deleted, not kept dormant.

The twin is meant to:
- Understand a CEO's responsibilities (decisions, briefs, budgets, priorities, investor/stakeholder communication).
- Read real state through connectors (calendar, mail, docs, issues, PRs, deals) and, with approval, write to it (send mail, create events, place calls).
- Prepare things ahead of time (briefs, decision packets) rather than only reacting.
- Be reachable through multiple interfaces: a CLI, a global voice hotkey + pop-up text bar on macOS, and (in progress / under discussion) a phone-reachable chat surface and a visual dashboard.

It does **not** try to imitate Kranthi's personal judgment or writing voice — it models the kind of decisions a CEO role faces, and only logs approvals/edits/denials as a (currently limited) signal, not a personality clone.

## The one hard constraint that shapes everything: zero metered spend

There is no budget for paid APIs. Every model call goes through Kranthi's Claude subscription via the `claude` CLI (a warm, supervised subprocess) — never a pay-per-token API key as the default path. Voice is on-device macOS speech (free), never a paid STT/TTS service. This constraint is why the architecture looks the way it does in several places: a small on-device router (see "The nervous system" below) exists specifically to avoid burning subscription-plan usage on trivial questions; a from-scratch small local model (FunctionGemma, via a local `llama-server` sidecar) was evaluated for the same reason; and any paid service (e.g. a real phone-call API) is treated as a deliberate, explicitly-scoped, user-approved exception — never a default.

## Core safety model

Every function a twin can call is declared in a YAML manifest (`twin.yaml`) at one of five access levels, and nothing is reachable unless it's explicitly listed — **default deny**:

| Level | Meaning |
|---|---|
| R | Read only |
| D | Prepare a draft; nothing leaves |
| A | Execute only after explicit approval of the exact payload |
| S | Autonomous within limits (e.g. writing the twin's own internal state) |
| B | Blocked entirely for now (money movement, permanent deletion, permission changes) |

On top of that:
- **One gate, no bypass.** Every connector call — read or write — goes through a single permission gate that checks the manifest, rate/usage caps, and (for level A, or level S under tainted input) requires a pre-approved envelope. A connector can only get real credentials/arguments by redeeming a one-time permit that only the gate can mint.
- **Approval envelopes are payload-hash-bound and single-use.** The exact arguments are hashed; approving means approving that exact hash. Editing the payload voids the approval and requires a fresh decision. The spoken/read confirmation ("read-back") the human hears before approving is built by code from the structured payload, never composed by the model — so the model can't describe one action while a different one executes.
- **External content is permanently untrusted.** Anything read from email, chat, a document, or the web taints the session for the rest of that conversation — escalates once, never de-escalates. A tainted session can never trigger an outward action without going through the approval queue, even if the model "decides" to.
- **Everything is audited.** An append-only, hash-chained log records every call, decision, denial, execution and edit, before the effect it records happens — an audit-write failure fails the action rather than silently letting it through. The chain can be independently verified end to end.
- **Auto/background mode (P2 priority) never performs outward actions** — only read/draft functions on an explicit allowlist, ever.

## System shape

```
water daemon                 # HTTP over a 0600 Unix socket, single-instance, one process per twin
  gate/       permission gate: manifest levels, rate/usage caps, taint tracking
  approvals/  envelope queue: payload-hash binding, code-generated read-back
  audit/      append-only, hash-chained log; fails closed
  store/      SQLite (modernc.org/sqlite), normalized record types
  connectors/ one package per external tool + the shared connector contract
  runtime/    context assembly, the main model-call path (ModelTurn)
  nervous/    the router in front of the model: intent registry, fast
              deterministic tiers, quick tools, write-intent proposals
  gateway/    the daemon itself: HTTP handlers, model-tool bridge, sync
  sync/       background refresher (mail + calendar, read-only, P2)
  tools/      MCP bridge the model's own subprocess talks to

water chat | ask | approve | connect <service> | daemon | status | doctor ...   # CLI
```

Runtime data (SQLite db, audit log, Unix socket) lives outside the repo, under `~/.water/`. Every twin (see "Multiple twins" below) gets its own data directory so one twin's records never mix with another's. Secrets live in the macOS Keychain, reached through a small vault abstraction — never in files, never in model context, and every credential type redacts itself in logs/JSON by construction.

## One conversation turn — the router ("the nervous system")

Every turn, from every channel (CLI, hotkey text bar, voice, a live meeting), goes through exactly one entry point, which runs a **registry-driven cascade** in front of the full model — nicknamed "head chef and sous chef":

```
turn arrives
  → eligibility check (too long? an escalate word like "why"/"compare"? multi-clause?)
      → not eligible: straight to the main model
      → eligible:
          → Tier 0: deterministic in-house templates match against a registry
            of known intents (schedule lookups, pending approvals, brief
            questions, etc.) — zero model calls, ~ms latency
              → match: answer built straight from the store
              → no match: escalate to the main model
  → main model (the full Claude subscription session, warm/persistent):
      answers normally, with two kinds of tools available — "quick tools"
      (read-only, same handler table as the fast tier, cannot reach the
      gate) and real connector tools (reach the gate exactly like a
      model-initiated action always has)
```

Tier 0 **never** reaches the permission gate directly — it can only read the store through a read-only connection, or, for a "write intent" (e.g. "cancel my 3pm"), hand a typed proposal to the same approval-queue path a model tool call would use. Level-A actions always queue an approval envelope; level-D actions (e.g. drafting an email) deliver the draft directly since nothing leaves at that level anyway. This is enforced by both runtime checks and static analysis of what each package is allowed to import/call, not just convention.

Why this exists: it means trivial, repeated questions ("what's on my calendar," "any pending approvals") cost zero subscription usage and answer in milliseconds, while anything ambiguous or novel still gets the full model's judgment — and the safety boundary (gate + approval queue) is identical no matter which tier ultimately answers.

**Current status of the fast tier:** Tier 0 (deterministic templates) is live and used by default — it is the only fast tier now. Tier 1 (a local FunctionGemma small-model second tier) was retired: it never cleared its own safety bar, missing both thresholds in a real live evaluation (a 2.2% false-accept rate against a ≤1% gate, and a 558ms p95 latency against a ≤400ms gate). A separate "promotion loop" (learn new phrasings from repeated real usage, with an explicit approval step before anything goes live) and "voice approval binding" (say yes/no out loud instead of typing) both exist but are off by default.

## Connectors — what's real vs. demo

- **Real, live:** Google Calendar, Gmail (read + write, including a separate verified "agent" mailbox identity so outbound mail is never impersonating Kranthi), Google Drive (read), GitHub (PRs/issues, read-only, via a personal access token), Linear (issues, read-only), HubSpot (deals/contacts, read-only). Each optional connector fails gracefully with a clear "not connected" message if no credential is configured — it never blocks the daemon from starting.
- **Demo/fake:** an entirely separate, in-memory GitHub/Linear/HubSpot stand-in (canned data, no network, no OAuth) used only by an explicit `--demo` twin, for showcasing the shape of the product without needing real credentials.
- **Twin-to-twin messaging:** a twin can exchange messages with a *different* person's twin daemon over a direct socket connection — outbound is approval-gated exactly like any other outward action; inbound is unconditionally treated as untrusted external content.
- **Agent's own mailbox:** a second, real Gmail account/identity the agent can send *as* (via Gmail's "send mail as" alias feature) with an automatic disclosure signature, so recipients always know a message came from the AI, not a spoofed version of Kranthi.

## Multiple twins

The same runtime can load different `twins/<id>/` manifests — the real CEO twin, a demo twin (fake connectors, for showcasing), and (for the twin-to-twin messaging feature) a minimal second "counterparty" twin. Each gets its own isolated data directory (`~/.water/twins/<id>/...`) so records never cross-contaminate; the real twin keeps the original flat `~/.water/` layout unchanged.

## Client surfaces today

- **CLI** (`water` binary): `chat`, `ask`, `approve`, `status`, `doctor`, `connect <service>`, `daemon`, `twin ...` — the primary interface, talks to the daemon over the Unix socket.
- **Swift macOS menu-bar app**: a status-bar item, a floating text-input panel, and a **global, system-wide hotkey** for true hold-to-talk voice (press and hold, speak, release to send — works from anywhere on the Mac, not just when the app has focus). Uses on-device macOS speech recognition and synthesis only.
- **A standalone, explicitly out-of-band demo tool** (`blandcall`) that places real phone calls through a third-party AI voice-calling API (Bland), used only to demonstrate voice capability live (e.g. to a recruiter). This is deliberately kept **completely outside** the gated system — not a registered connector, never in any twin's manifest, never touches the permission gate — specifically so it can bypass the approval flow for a fast live demo without weakening the "nothing acts without an approved envelope" guarantee everywhere else. It enforces its own small, code-level call budget (a hard cap on total real calls placed) since it's a deliberate, bounded exception to the zero-metered-spend rule, not a standing feature.

## What's being actively discussed / not yet built

As of this writing, active design conversation (not yet built) covers:
- **A real visual interface** — drafts, briefs, pending decisions, and dashboards rendered somewhere persistent, likely a small local web view served by the daemon's existing HTTP API rather than a heavy native rebuild.
- **Session/thread boundaries** — today the daemon holds one conversation context per twin, one turn at a time; getting Claude-style separate conversation threads means deciding whether threads share authorization (yes — one CEO, one real set of outward actions) while getting separate conversational context, and whether they run concurrently or serialize.
- **Reachability from a phone** — likely via a Discord or Slack bot relay (both make an outbound-only connection to the platform's gateway, so no public port/TLS cert needed on the Mac) forwarding messages into the daemon's existing turn API. Actual PSTN phone calls, or platform voice-call participation beyond the scoped demo tool above, would each be a deliberate, explicitly-approved exception to the zero-metered-spend rule, evaluated case by case — not a default capability.
- A real ElevenLabs-quality voice was considered and explicitly **not** adopted as the system default (their free tier disallows commercial use and has a small monthly quota) — on-device voice remains the default; a paid voice would only ever be a similarly scoped, explicit exception for a specific moment, not a standing dependency.

## Working conventions worth knowing

- One focused "slice" of work happens at a time, on a dedicated branch, merged into `feat/ceo-twin` only after its own gates pass in isolation (`go vet`, the full race-enabled test suite, a static `CGO_ENABLED=0` build, `gofmt`).
- Multiple parallel tracks of work, when they happen, each get their own branch and their own isolated verification before merging — never pushed straight to the shared branch.
- Nothing is committed to git unless explicitly asked for; nothing is pushed to the remote unless explicitly asked for.
