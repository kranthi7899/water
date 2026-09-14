# water — build progress

Running log of what exists, what was verified, and what's next. Update this file at the end of
each work session rather than trusting memory or git history alone.

---

## Status: Phase 1 (skeleton) — COMPLETE — 2026-09-13

Every interface from the build plan exists with at least one working implementation. Personas
are deliberately blank (Phase 2 work). All Phase 1 gates pass against a real Claude Pro
subscription.

### What was built

| Area | Package | Contents |
|---|---|---|
| Backends | `internal/backend` | `Backend` interface, `Registry` + single `Select()` precedence function, `ClaudeSubscription`, `CodexSubscription`, `API` (metered), env-scrubbing (`ScrubbedEnv`/`LeakedKeys`), runtime `--help` flag detection, `Fake` for tests |
| Persona | `internal/persona` | `Source` (Embedded/Dir/Overlay), frontmatter parsing, `Skill` discovery + `SkillSelector` (keyword default), hidden `.index.json` traceability index |
| Roles | `internal/roles` | folder discovery, manifest validation, singleton/orchestrator invariants enforced at load, memory scoping |
| Memory | `internal/memory` | `Provider` interface, `Scoped` binding (no role parameter anywhere on it), bounds (`ErrBoundsExceeded`, never silent truncation), `Markdown` provider (Hermes pattern) |
| Orchestrator | `internal/orchestrator` | mutex-guarded `State`, `AgentMessage` outbox, `Executor` (bounded parallelism, step guard), `CEOFanoutRouter` (visit-count driven, checkpoint-resumable), `NoopCheckpointer` |
| Agent node | `internal/agent` | the one place prompts are assembled; enforces memory isolation and inbox filtering |
| Surfaces | `internal/surface` | `Surface` interface, `Terminal` (lipgloss, etched visual identity), `JSON` |
| Config | `internal/config` | layered (defaults → file → env → flags) with per-key provenance, schema migration stub |
| Tracing | `internal/trace` | JSONL per-run trace, end-of-run `Stats` (metered call count must be 0 under defaults) |
| Voice | `internal/voice` | `Provider` interface, `Noop` only |
| CLI | `internal/cli` | full cobra tree: `onboard`, `doctor`, `status`, `run`, `orchestrate`, `memory`, `config`, `version`; hidden stubs for `voice`/`dashboard` |
| Guards | `internal/guards` | the three required guard tests, written before final wiring |

~5,400 lines of Go. Module name: `water`. Entry point: `cmd/water/main.go`. Agents tree is
`//go:embed`-ed via `embed.go` at the repo root.

### Gate results (Part 13, Phase 1 checklist)

All run under a temporary `$WATER_HOME` (deleted after), against the real `claude` CLI logged
in with a Claude Pro subscription. `codex` is not installed on this machine.

| Gate | Result |
|---|---|
| `go build` produces a working binary; `water --help` lists the full tree | ✅ |
| `onboard` detects claude (Pro, logged in) / codex (absent) correctly | ✅ |
| `doctor` warns when `ANTHROPIC_API_KEY` is exported alongside a subscription backend | ✅ |
| `run ceo "hello"` completes a round trip via claude-subscription | ✅ (3.4s, 0 metered) |
| `orchestrate "<brief>"` runs ceo → parallel fan-out → ceo synthesis → prints FinalOutput | ✅ (5 calls, 0 metered, 7 messages traced) |
| Adding a 5th dummy role folder is picked up by `water status` with zero code changes | ✅ (via `--agents-dir`) |
| Two `singleton: true` roles fails loudly at load | ✅ |
| Run summary reports 0 metered calls under default config | ✅ |
| All three guard tests pass | ✅ |

### Fixes made during verification

- **Connector leakage**: the first real `orchestrate` run showed delegate subprocesses could see
  the user's Drive/Gmail/Calendar MCP connectors through the `claude` CLI. Fixed by adding
  `--strict-mcp-config` to the subprocess args in `internal/backend/claude.go`. Re-verified: no
  connector mentions in the trace afterward.
- **TTY detection**: switched from a raw `os.ModeCharDevice` check to
  `github.com/charmbracelet/x/term.IsTerminal` for correctness across platforms.
- **Message tracing**: added `State.Observe(fn)` so trace/surface hooks subscribe to
  `AppendMessage` directly instead of the CLI re-walking `State.Messages()` after the fact.
- **Token accounting**: `claude`'s JSON result usage includes `cache_read_input_tokens` /
  `cache_creation_input_tokens`; both are now folded into `Response.InputTokens`.

### Known gaps / deliberately deferred (per spec, not forgotten)

- Not yet a git repository — nothing has been committed.
- `agent-sdk`/goreleaser/`install.sh` (Phase 6) — not started.
- Voice `OSProvider`, dashboard, resumable checkpointer — interfaces exist, implementations are
  Phase 4/5 work.
- Persona content (`soul.md`, `experience.md`, `.index.json` entries, skills) is entirely blank —
  this is Phase 2, explicitly out of scope for the engineering build.

---

## Next up: Phase 2 — persona content pass (no engineering)

Per the build plan: write `soul.md` / `experience.md` for **CEO first**, build its `.index.json`
traceability mapping, author its first two skills, and write the CEO's understanding of its own
orchestration role into its `soul.md`. Gate: CEO produces role-appropriate output on five
held-out prompts, traceable via the hidden index, before any other role's content is written.

Do not touch the separate Python decide-then-ground memo pipeline — unrelated, out of scope
permanently.

---

## Environment notes (for whoever picks this up)

- Go was not preinstalled; installed via Homebrew on 2026-09-13.
  `go` lives at `/opt/homebrew/bin/go`, which is **not** on this shell's default `PATH` — prefix
  commands with `export PATH=/opt/homebrew/bin:$PATH`.
- `codex` CLI is not installed on this machine; only the `claude` CLI backend has been exercised
  end-to-end.
- Local dev loop: `go build -o bin/water ./cmd/water && ./bin/water doctor`. Test suite:
  `go test ./...`.
