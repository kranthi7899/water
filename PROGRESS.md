# water — build progress

This file used to duplicate the full build log for the four-role council
that Water was rebuilt away from. That log is preserved at
`docs/archive/` (see below) rather than kept here, since it describes a
CLI, orchestrator, and persona stack that no longer exist in the tree.

**Live source of truth: `docs/EVOLUTION_PLAN.md`.** It carries the current
status line, the slice order, and a dated log entry for every slice —
what was built, what was decided, what's left undone, and what to verify
by hand. `docs/CONTEXT.md` is the project's current state and design
principles. Read those two first; this file is a pointer plus a
high-level timeline, not a restatement.

## Timeline

- **2026-09-14 — Phase 3 (master build v2), council era.** The four-role
  (CEO/COO/CTO/Design) council: persona/identity/experience, hierarchy
  orchestration with checkpointing, the MCP tool layer, per-role voice,
  the terminal chat TUI with themes, and distribution tooling. Full detail
  archived at `docs/archive/` (see below); none of this code remains.
- **2026-09-15 — Judgment and rollout passes, council era.** A judgment
  pass against public `codex`/`hermes-agent` source, a persona audit, a
  reasoning-layer rollout across all four roles, and a Claude-Code usage
  parity sweep — all against the council architecture above.
- **2026-09-23 — Step 0.** Decision to rebuild Water as a single personal
  CEO twin. `docs/CONTEXT.md`, `CLAUDE.md`, and `docs/EVOLUTION_PLAN.md`
  written; branch `feat/ceo-twin` created.
- **2026-09-23 — A1.** Foundation: `gate` (R/D/A/S/B levels, default deny,
  untrusted tagging, rate/usage caps), `approvals` (payload-hash envelopes,
  code-generated read-backs), `audit` (hash-chained, fails closed), `store`
  (SQLite, normalized records), `connectors` (contract + fake), `vault`.
- **2026-09-23 — Owner decision.** The council is deleted outright, not
  kept dormant. Water is a single personal agent from here on.
- **2026-09-23 — A2.** `water daemon` (streaming, approvals, state, cancel
  over a 0600 Unix socket); `water chat`/`ask`/`approve`; a warm `claude`
  subprocess reused across turns; the council code removed.
- **2026-09-23 — A3.** Real, read-only Google Calendar/Gmail/Drive
  connectors replace the in-memory fakes; `water connect google`;
  background sync (P2); a taint-propagation fix and a fast-path
  correctness fix.
- **2026-09-24 — A4.** Incremental sync (Calendar sync-token, Gmail
  historyId) on two independent tickers; the morning brief pipeline
  (signals computed in code, the model only phrases them).
- **Next: Slice B (voice and clients).** See `docs/EVOLUTION_PLAN.md`'s
  log for current status — an interim macOS Shortcut exists; the real
  Swift menu-bar app (text pop-up first, voice second) is not yet built.
  Specs for the slices after B (C: decisions, M: live-meeting assistance)
  are written and blocked on B per the owner's own sequencing: see
  `docs/slices/C.md`, `docs/slices/M.md`, `docs/slice-c-planning.md`.

## Archived council-era material

Full detail on everything above the "Step 0" line, plus adjacent
investigation/decision notes that assumed the deleted architecture, is
kept for history rather than restated:

- `docs/archive/decisions-council-era.md`
- `docs/archive/tools-council-era.md`
- `docs/archive/voice-council-era.md`
- `docs/archive/judgment-pass-v2.md`
- `docs/archive/persona-audit-prompt.md`
- `docs/archive/reasoning-layer-ceo-pilot-prompt.md`
- `docs/archive/reasoning-layer-remaining-roles-prompt.md`
- `docs/archive/known-gaps-persona-council-era.md`
