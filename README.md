# water

A council of role-agents on the subscription you already pay for.

`water` is a single Go binary. It hosts N role-agents (a singleton CEO plus CFO, CTO and a
fourth role) with isolated per-role personas and memory, orchestrated through a native state
graph, running on the `claude` / `codex` subscription CLIs rather than metered API billing.

**Status: Phase 1 (skeleton).** Every interface exists; personas are deliberately blank.

## Build & run

    go build -o bin/water ./cmd/water
    bin/water onboard                 # detects claude/codex, checks auth, writes ~/.water/config.yaml
    bin/water doctor                  # backends, auth, metered-leak warning, roles, memory bounds
    bin/water status                  # discovered roles, selected backend, memory sizes
    bin/water run ceo "hello"         # single-role round trip
    bin/water orchestrate "<brief>"   # ceo → parallel fan-out → ceo synthesis
    bin/water memory ceo list|add|remove|prune
    bin/water config [get k | set k v]

`--json` on any command gives machine-readable output. `--backend`, `--allow-metered`,
`--quiet`, `--verbose`, `--yes`, `NO_COLOR` behave as you'd expect. Dev-only:
`--agents-dir <dir>` overlays a local agents tree over the embedded one.

Prerequisite: `claude` (logged in with a Claude subscription) or `codex` (signed in with
ChatGPT). Neither Python nor Node is needed to run `water` itself.

## Layout

    cmd/water/            entry point
    embed.go              //go:embed all:agents — adding a role is adding a folder
    agents/<slug>/        role.yaml, soul.md, experience.md, .index.json, skills/, memory/session.md
    internal/backend      Backend interface, registry, Select() precedence, claude/codex/api impls
    internal/persona      Source overlay (embedded ⟵ dir), frontmatter, skills + SkillSelector, hidden index
    internal/roles        discovery, manifest validation, singleton/orchestrator invariants
    internal/memory       MemoryProvider + Scoped binding + bounds; markdown provider
    internal/orchestrator State (mutex-guarded), Executor, Router (ceo-fanout), Checkpointer (noop)
    internal/agent        the role node: the ONE place prompts are assembled (isolation enforced here)
    internal/surface      Surface interface; terminal + json
    internal/trace        JSONL run traces + run stats
    internal/voice        VoiceProvider interface; noop
    internal/config       layered config with provenance, schema migration stub
    internal/cli          cobra command tree
    internal/guards       the three guard tests (metered-leak, memory-isolation, singleton)

See `docs/architecture.md` for the invariants and extension points.

## Tests

    go test ./...

The three guard tests in `internal/guards` protect the properties most likely to regress
silently: no metered call while a subscription backend is available; no role's prompt contains
another role's memory or messages not addressed to it; two singletons fail at load and only an
orchestrator can write FinalOutput.
