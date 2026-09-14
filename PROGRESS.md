# water — build progress

Running log of what exists, what was verified, and what's next. Update this file at the end of
each work session rather than trusting memory or git history alone.

---

## Status: Phase 3 (master build v2) — COMPLETE — 2026-09-14

Everything in the master build prompt is built: input editor, slash commands, sessions, skills,
identity binding, auth, hierarchy orchestration, tool layer (MCP), attachments, voice, dashboard,
distribution, visual design system, per-role backends. All 25 gate/guard tests pass. Decisions,
investigation findings and the Part 10 answers are in `docs/decisions.md`.

### What was built

| Part | Package(s) | Contents |
|---|---|---|
| 3A identity | `internal/identity`, `persona`, `roles` | role_id UUID in role.yaml; role_id/file_type/content_hash stamped in every persona file; HMAC keyring at `~/.water/keyring`; `water persona show/edit/sign/verify`; git-backed journal at `~/.water/persona-journal` |
| 3B auth | `internal/auth`, `cli/cmd_onboard` | `claude login` / `codex login` launched with inherited stdio, headless fallbacks; success gated on one real round trip; `onboard.verified_at` recorded |
| 3C checkpointer | `orchestrator/checkpoint.go` | `FileCheckpointer` (atomic JSON per run), save after every superstep and on node failure, `--resume <id>`, `--list` |
| 3D voice | `internal/voice/os.go` | `say` / `spd-say` / `espeak`; Listen is a documented no-op; `--voice` on chat/run |
| 3E dashboard | `internal/dashboard` | read-only HTTP: runs, outbox graph, timings, diagnostics, tool calls with decisions, persona file status; non-GET → 405 |
| 3F distribution | `.goreleaser.yaml`, `install.sh`, `.github/workflows` | darwin/linux × amd64/arm64; curl-bash installer with checksum check; CI on push, release on tag |
| 4.1 editor | `internal/editor` | Bubble Tea v2 + Bubbles v2 textarea behind `InputEditor`; uniseg cell math; Ctrl+J newline, Enter submit, Shift+Enter only after Kitty detection |
| 4.2 commands | `internal/chat/commands.go` | `/clear /compact /resume /delete /name /model /backend /status /help /quit /consult /switch /remember /why /skills /flag /attach /editor /agents` |
| 4.3 sessions | `internal/session` | per-role JSONL; count+age retention, pinned exempt; tail-bounded Open so compaction never loads the whole file; manual promotion only |
| 4.4 skills | `persona/skills.go`, `agents/*/skills` | Anthropic SKILL.md schema enforced; 22 skills restructured from the twin frameworks; `DescriptionSelector` default |
| 5 tools | `internal/tools`, `backend/claude.go`, `cli/cmd_mcp.go` | policy, root confinement (`..`, symlinks), MCP stdio server (`water mcp-serve`), `--tools "" --strict-mcp-config --mcp-config`, traced decisions, untrusted marking, macOS sandbox profile for the (unused) shell tool; attachments via stream-json |
| 6 orchestration | `orchestrator/edges.go`, `router_hierarchy.go`, `agent/node.go`, `diagnose` | permission-graph DAG, hierarchy router with rounds/steps/stall bounds, CEO answers alone, mechanical verbatim forwarding, epistemic status marks, capability manifests in role.yaml, seven diagnostics + `water diagnose` |
| 7 design | `internal/theme`, `internal/layout`, `themes/`, `chat/tui.go` | five theme YAMLs, colour degradation, deterministic regions, hero box fixed per size, agent picker |
| 8 backends | `backend/registry.go`, `cli/app.go` | per-role `backend:`/`model:` resolved inside `Select` (flag → role.yaml → config → auto); `water status` shows each role's backend and why |

### Gate results (real `claude` CLI, Claude Pro, fresh `$WATER_HOME` each)

| Gate | Result |
|---|---|
| `go test ./...` — all guard and gate tests | ✅ 25 tests across 16 packages |
| `water onboard`: detect → verify round trip → config | ✅ (2.7s, 0 metered) |
| MCP tool path: CTO reads a file under a declared root; trace has `tool_call` with decision+basis | ✅ |
| CEO (no tools block) reports NONE | ✅ |
| Image attachment via stream-json; text memo with injected instruction treated as data | ✅ |
| `water chat cto` through a pty: header, hero, `/status`, `/help`, `/quit` | ✅ |
| Hierarchy run A (payments migration): killed at the old 5m run ceiling mid-Design → `--resume` ran only Design → COO rollup → CEO final | ✅ 3 calls on resume, 0 metered; CEO wrote an explicit decision with reversal conditions |
| Hierarchy run B (onboarding rebuild): killed at 35s mid-COO → `--resume` re-ran COO, not the CEO frame; two assignment rounds; Design dissent forwarded verbatim; final status marked UNCONFIRMED ×4 | ✅ 8 calls, 0 metered, 16 messages; `diagnose`: dissent survival 2/2, every edge permitted, one first-round status flagged for missing marks |
| A killed run survives the subscription session limit (quota ran out between kill and resume) | ✅ resumed after the window reset |
| `water diagnose`, `water dashboard` over real traces; POST → 405 | ✅ |
| Codex backend | ⚠ not installed here; MCP tools on codex deliberately unwired |
| `goreleaser` / `install.sh` end-to-end | ⚠ no release tag yet; config and script parse |

### Bugs found by real runs and fixed during the campaign

- COO could not see the brief (only the CEO's summary) → Water now appends the brief verbatim
  under the CEO's direction.
- Hub nodes marked their inbox consumed before the backend call; a failure then made a resumed
  run look finished → consume-after-success, plus a restore-time repair; `TestResumeAfterHubFailure`.
- One `orchestration.timeout` governed both a single call and the whole run; a six-call run hit
  the 5m ceiling → `call_timeout` (4m) and run `timeout` (20m) are separate.
- A tool-less CTO emitted tool-call XML as text → every prompt now states exactly which tools
  exist (or that none do).
- Hero art sized the hero box, moving CHAT between themes → fixed hero box per terminal size.

### Environment notes

- Go at `/opt/homebrew/bin/go` (`export PATH=/opt/homebrew/bin:$PATH`). Module `water`, Go 1.27.
- Dev loop: `go build -o bin/water ./cmd/water && ./bin/water doctor`; tests `go test ./...`.
- Real runs under a scratch home: `WATER_HOME=$(mktemp -d) ./bin/water onboard --no-picker`.
- Persona files must be re-stamped after a manual edit: `./bin/water persona sign --no-signature`.
- Subscription session limits are real: a five-hour window ran out mid-campaign; runs checkpoint
  and resume cleanly across it.

## Next up

- Verify the Codex path on a machine with `codex` (MCP via `-c mcp_servers.*`, `--image`).
- Linux Landlock for the shell tool before any role is granted shell.
- Cut `v0.1.0` and enable the Homebrew tap in `.goreleaser.yaml`.
- Persona content pass on the Danone/Mylan CEO entry (see `docs/decisions.md` Q3).
