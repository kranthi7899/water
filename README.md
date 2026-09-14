# water

A council of role-agents on the subscription you already pay for.

`water` is a single Go binary. It hosts four role-agents — a singleton CEO plus COO, CTO and
Design — each with an isolated persona and memory, orchestrated through a two-tier hierarchy
(CEO → COO → specialists → COO → CEO), running on the `claude` / `codex` subscription CLIs
rather than metered API billing.

## Install

    curl -fsSL https://raw.githubusercontent.com/kranthi7899/water/main/install.sh | bash
    water onboard          # detects claude/codex, signs you in, verifies one real round trip

One binary. No Docker, no Python, no Node. Prerequisite: `claude` (Claude subscription) or
`codex` (ChatGPT plan). From source: `go build -o bin/water ./cmd/water`.

## Use

    water                           # like `claude`: the agent picker, then a chat with one role
    water chat cto                  # straight into a session with one role
    water chat cto --resume <slug>  # continue a transcript
    water orchestrate "<brief>"     # CEO frames → COO assigns → CTO/Design → COO verifies → CEO decides
    water orchestrate --resume <id> # continue a killed run from its checkpoint
    water diagnose <run-id>         # topology diagnostics: dissent survival, unverified done, …
    water dashboard                 # read-only local web view of roles, runs, tool calls
    water run cto "…" --attach deck.pdf
    water memory ceo list|add|remove|prune
    water status · water doctor · water config

Slash commands in `chat`: `/help /clear /compact /resume /delete /name /model /backend /status
/consult <role> <q> /switch <role> /agents /remember /why /skills /flag /attach /editor /quit`.
Enter sends; Ctrl+J inserts a newline (Shift+Enter too, when the terminal supports the Kitty
keyboard protocol); Ctrl+X opens `$EDITOR`.

## Invariants (enforced in code, each with a guard test)

1. Subscription billing, not metered API — `backend.Select` refuses metered backends without opt-in.
2. Per-role memory isolation — a node's prompt can only contain its own memory and messages
   addressed to it; `/consult` answers arrive as messages, never as memory reads.
3. The CEO is a singleton, the only entry point, and the only writer of final output.
4. Identity files are fixed anchors — `role_id` + `content_hash` (+ HMAC on edited local trees).
5. Personas are embedded and hidden; `water persona` needs a real local `agents/` directory.
6. Tools are deny-by-default and role-scoped, served by Water over MCP, traced with decisions.
7. Themes change palette and ornament only; chat and input geometry is theme-invariant.

## Layout

    cmd/water/            entry point
    embed.go              //go:embed all:agents + themes
    agents/<slug>/        role.yaml, soul.md, experience.md, .index.json, skills/*/SKILL.md, memory/
    themes/<name>.yaml    palette + hero motif per role (water-base, ceo, coo, cto, design)
    internal/backend      Backend interface, registry, Select() precedence, claude/codex/api
    internal/tools        policy, MCP stdio server, root confinement, sandbox profile, tracing
    internal/orchestrator State, permission graph (DAG), hierarchy + fan-out routers, file checkpointer
    internal/agent        the ONE place prompts are assembled (isolation enforced here)
    internal/persona      sources, frontmatter, SKILL.md schema + selectors, hidden index
    internal/identity     role_id / content_hash / HMAC keyring, stamping, persona journal
    internal/roles        discovery, manifests, capability manifests, tool grants
    internal/memory       bounded per-role memory (markdown)
    internal/session      per-role JSONL transcripts, retention, tail-bounded compaction
    internal/chat         slash commands, attachments, the Bubble Tea v2 TUI and picker
    internal/editor       InputEditor over Bubbles textarea, uniseg cell math
    internal/layout       deterministic region engine (HEADER/HERO/CHAT/INPUT/STATUS)
    internal/theme        theme YAML, colour degradation (truecolor → 256 → 16 → none)
    internal/diagnose     the seven topology diagnostics over a trace + checkpoint
    internal/dashboard    read-only HTTP surface
    internal/auth         login triggering and the round-trip gate
    internal/voice        OS text-to-speech (say / espeak); listen is a documented no-op
    internal/guards       guard tests for every invariant and gate

See `docs/architecture.md`, `docs/tools.md`, and `docs/decisions.md` (investigations, Part 10
answers, and what the build prompt got wrong).

## Tests

    go test ./...

Guard tests cover: metered leak, memory isolation, singleton, resume-does-not-rerun, dissent
forwarded verbatim, specialists cannot message each other, escalation edge, per-role backend,
consult no-memory-leak, strict-mcp-config survives, role-without-tools invokes nothing, reads
outside roots fail (`..` and symlinks), untrusted messages must be marked, SKILL.md schema, input
wrap visible, wide cursor, theme does not move chat, theme degrades, hero never collides,
clear keeps transcript, pinned survives prune, compaction no deadlock.
