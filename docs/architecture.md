# water — architecture notes (Phase 1)

## Invariants enforced in code

| Invariant | Where enforced | Guarded by |
|---|---|---|
| No metered call while a non-metered backend is available | `backend.Select` (the only selection function) | `guards.TestGuard_MeteredLeak` |
| Metered keys never reach `claude`/`codex` subprocesses | `backend.ScrubbedEnv`, used by `runScrubbed` | `guards.TestGuard_ScrubbedEnv` |
| A node sees only its own memory | `roles.Role.Memory()` returns `memory.Scoped` (no role parameter exists) | `guards.TestGuard_MemoryIsolation_*` |
| A node sees only messages addressed to it | `agent.Node` reads `State.Inbox(self)` and nothing else from shared state | `guards.TestGuard_MemoryIsolation_Prompts` |
| Exactly one singleton, at least one orchestrator, unique slugs | `roles.Load` fails with every problem listed | `guards.TestGuard_TwoSingletonsFailLoudly` |
| Only an orchestrator writes FinalOutput | `State.SetFinalOutput` runtime check | `guards.TestGuard_NonOrchestratorCannotWriteFinalOutput` |
| Memory never silently truncates | `memory.CheckBounds` → `ErrBoundsExceeded` on read and write | `memory.TestBoundsAreExplicitErrors` |

## Extension points (all interfaces exist today)

| # | Interface | Phase 1 implementations | Add later by |
|---|---|---|---|
| 1 | `backend.Backend` | claude-subscription, codex-subscription, api | new file + `Default.Register` |
| 2 | `memory.Provider` | markdown | `memory.Register(name, factory)` |
| 3 | Role | ceo, coo, cto, design | add a folder under `agents/` |
| 4 | `persona.SkillSelector` | keyword | implement + wire in `agent.Env.Selector` |
| 5 | `orchestrator.Router` | ceo-fanout | `orchestrator.RegisterRouter` |
| 6 | `voice.Provider` | noop | `voice.Register` |
| 7 | `surface.Surface` | terminal, json | `surface.Register` |
| — | `orchestrator.Checkpointer` | noop | swap in `cmd_orchestrate.go`; `--resume` already parsed |
| — | `persona.Source` | embedded, dir, overlay | `--agents-dir` today; a config key later |

## Data flow of one orchestrated run

1. `State` is created with the brief and the set of orchestrator slugs.
2. The executor injects the brief as `AgentMessage{From:"user", To:<entry>, Topic:"brief"}`. Even the
   CEO receives context only through its inbox.
3. Router `ceo-fanout` derives the phase from `State.Visits`: CEO (decompose) → all delegates in
   parallel (bounded by `orchestration.max_parallel`) → CEO (synthesis) → done.
4. Each node: memory snapshot (once per run per role) → `Inbox(self)` → `agent.Assemble` → `Backend.Run`
   → append typed messages / `SetFinalOutput`.
5. Every backend call and message is written to `~/.water/traces/<run-id>.jsonl`; the summary reports
   the metered call count, which must be 0 under default config.

## Persona files

Phase 1 ships scaffolds with `status: unwritten` frontmatter. The content pass (Phase 2) must keep
two rules, which are also written into the scaffold comments: `soul.md` frames the agent as
managing delegated work and instances toward a mission, not humans; routine/habit content is a
pattern to reason from, not an identity to adopt. `experience.md` stays uncited; its
sentence→record map lives in `.index.json` and is queried via `persona.Index.Lookup`.

Memory seeds live in `agents/<slug>/memory/session.md` (embedded, read-only) and are copied to
`~/.water/memory/<slug>/session.md` on first use, which is the writable copy `water memory` edits.

## Exit codes

0 ok · 1 error · 2 usage / prerequisite · 3 backend unavailable or metered refused · 4 unconfigured
