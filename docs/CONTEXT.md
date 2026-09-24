# Part A: project context (save to `docs/CONTEXT.md`)

## What Water is now

Water is a runtime for **digital twins**: an agent that serves one specific human in their role, inside that person's real work environment. The first and only twin for now is the **CEO twin**, serving the CEO of a small AI/software company (10 to 50 people, several parallel projects, investors and stakeholders).

The twin is an intelligent assistant with voice, not a persona. It must:

- Understand the responsibilities of the CEO role (decisions, briefs, budgets, priorities, investor communication) and the environment it sits in (projects, people, tools).
- Have access to the CEO's real workflow through connectors: calendar, email, docs, team chat, meetings and transcripts, issue tracking, code hosting, finance, CRM, presentations and dashboards, phone and SMS, social.
- Check email and schedule, capture meeting information, prepare research and decision packets ahead of time, and, with approval, send messages and emails, place calls, and schedule work.
- Respond in real time. Low latency is a hard requirement.
- Be reachable by CLI, a global voice hotkey, and a pop-up text bar. Later: phone.

The twin does **not** replicate one person's personal judgment style (deferred). It models the kinds of decisions a CEO faces. "Judgment learning" is limited to logging approvals, edits and denials, plus explicit corrections the CEO states.

## What this supersedes

- Multiple role agents (CEO, COO, CTO, Design, Researcher) are cut to **one CEO twin**. Keep the other role directories in the repo but mark them dormant. Do not delete them.
- The persona-embodiment approach is retired. The CEO `soul.md` should be rewritten around the role's responsibilities and the environment, not a personality. Move the Steve-Jobs-derived block and other persona material to `docs/archive/`.
- The CEO is no longer "top-level orchestrator of a team of agents". The twin serves a human. Keep the Go state-graph executor for background pipelines (decision packets, meeting recap, morning brief), not for role-to-role delegation.
- Terminal-only operation and terminal-native TTS are replaced by a **daemon plus multiple clients** architecture, with two-way voice through a native client.

## Design principles (non-negotiable)

1. **Code counts, the model judges.** Normalizing tool output, computing signals, and enforcing permissions are done in code. The model interprets and drafts.
2. **All outward actions pass one gate.** Every send, post, call, invite and write goes through the permission gate and the approval queue. No connector may bypass it.
3. **External content is untrusted data.** Anything from email, chat, documents or the web is tagged external. An action whose parameters derive from external content can never run autonomously.
4. **Two planes.** A background plane precomputes state (brief, open decisions, packets). A conversation plane answers quickly from that state with a small fast model and streamed output. Slow work runs in the background while the voice says "on it".
5. **A twin is a runtime plus a manifest.** Capabilities come from the manifest, not from code branches per role.
6. **Memory has tiers:** working context (assembled per turn), a state store (normalized records), and long-term memory (markdown files, written only from explicit CEO statements and approved corrections, each with provenance).
7. **Auth:** subscription login is primary for model access with raw API keys as fallback. Keep the model provider behind an interface: a fast small model for conversation, a stronger one for packet and playbook work.

## Access levels

| Level | Meaning |
|---|---|
| R | Read only |
| D | Prepare a draft; nothing leaves |
| A | Execute only after approval of the exact payload |
| S | Autonomous within limits (e.g. writing to its own state) |
| B | Blocked in v1 (money movement, permanent deletion, permission changes, merges) |

Default deny: a tool exists for a twin only if the manifest lists it at a stated level.

## Priority and modes

Task queue with three priorities:

- **P0**: the CEO's immediate request. Preempts everything at the next tool-call boundary.
- **P1**: tasks the CEO approved or scheduled.
- **P2**: auto-mode background work: read-only preparation and drafts only.

Modes: **interactive** (P0 and P1 only) and **auto** (adds P2). Auto mode never performs outward actions, never changes what the CEO asked for, and never switches focus on its own. If it believes something matters more, it queues a suggestion and surfaces it at a natural pause. Auto mode uses a tool allowlist and its own rate and spend caps.

## Where things live

```
water/                         # single repo
  cmd/water/                   # one Go binary: `water daemon | chat | ask | approve | twin ...`
  internal/
    runtime/                   # agent loop, model provider interface, context assembler
    gateway/                   # local API server used by all clients
    gate/                      # permission gate, risk tiers, untrusted-content tagging
    approvals/                 # draft envelope, approval queue, voice yes/no matcher
    audit/                     # append-only log
    connectors/                # one package per tool + the connector contract + MCP client
    store/                     # SQLite state store, normalized record types
    memory/                    # long-term markdown memory, provenance
    scheduler/                 # P0/P1/P2 queue, modes, action-plan scheduling
    pipelines/                 # Go state-graph executor jobs: brief, recap, packets
    playbooks/ recipes/ templates/   # loaders and validators for the plain files
    twins/                     # manifest loader, twin-to-twin transport
  twins/ceo/                   # the CEO twin: manifest + plain files (below)
    twin.yaml
    role.md                    # responsibilities and environment (replaces soul.md persona)
    tools/*.md                 # one profile per connector
    playbooks/*.md             # decision types
    recipes/*.md               # meeting recap, morning brief, meeting prep, ...
    templates/*.yaml           # outward message schemas
  clients/macos/               # Swift menu-bar app: hotkeys, pop-up bar, mic, speech
  docs/
```

Runtime data lives outside the repo in `~/.water/`: the SQLite database, long-term memory files, the audit log, run sockets, and per-twin data directories. Secrets are stored in the macOS Keychain, never in files and never in model context.

## Amendment (2026-09-23)

The owner decided, mid-way through Slice A2, to go further than "keep the
other roles in the repo but mark them dormant" (above): **the other roles
are deleted, not kept dormant.** `agents/coo`, `agents/cto`, `agents/design`
and their skills, and the four roles' persona files generally, are gone from
the working tree; there is no `docs/archive/personas/` — anything not kept
survives only in git history. Water is a single personal agent.

Two further consequences of that decision, both already reflected in the
code and in `docs/EVOLUTION_PLAN.md`'s keep/move/retire table:

- The Go state-graph executor (`internal/orchestrator`'s executor and
  checkpointer) is removed along with the rest of the council, not kept for
  pipelines as originally planned. A later slice's pipelines (brief, recap,
  packets) will be plain Go functions, not a state graph.
- Twin-to-twin messaging (Slice E) will be designed and built fresh on its
  own envelope when that slice starts, rather than reusing anything from the
  deleted `orchestrator.AgentMessage`/outbox.
