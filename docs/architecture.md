# water — architecture notes

## Invariants enforced in code

| Invariant | Where enforced | Guarded by |
|---|---|---|
| No metered call while a non-metered backend is available | `backend.Select` (the only selection function; per-role inputs resolved inside it) | `TestGuard_MeteredLeak`, `TestPerRoleBackend` |
| Metered keys never reach subprocesses | `backend.ScrubbedEnv` | `TestGuard_ScrubbedEnv` |
| A node sees only its own memory and its own inbox | `agent.Assemble` is the one prompt assembler; `memory.Scoped` has no role parameter | `TestGuard_MemoryIsolation_*`, `TestConsultNoMemoryLeak` |
| Exactly one singleton, an orchestrator, unique slugs and role_ids | `roles.LoadWith` | `TestGuard_TwoSingletonsFailLoudly` |
| Only an orchestrator writes FinalOutput | `State.SetFinalOutput` | `TestGuard_NonOrchestratorCannotWriteFinalOutput` |
| Messages travel only declared edges | `State.AppendMessage` → `PermissionGraph.Check` | `TestSpecialistsCannotMessageEachOther`, `TestEscalationEdge` |
| Dissent/escalation reaches the CEO byte-identical | COO node forwards `Verbatim` messages mechanically before its own report | `TestDissentForwardedVerbatim` |
| A node that consumed external content cannot emit an unmarked message | `State.MarkUntrusted` + `AppendMessage` | `TestUntrustedMustBeMarked` |
| Persona files belong to their folder | `identity.Verify` in `persona.Load` | `identity` tests, `TestSkillSchemaValid` |
| `claude` always runs with `--strict-mcp-config` and `--tools ""` | `ClaudeSubscription.BuildArgs` | `TestStrictMCPConfigSurvives` |
| Tools: deny by default, roots explicit, symlinks/`..` confined | `tools.Policy.Authorize`, `ResolveWithinRoots` | `TestRoleWithoutToolsInvokesNothing`, `TestReadOutsideRootsFails` |
| Interactive write/command actions pause for a correlated one-time approval | `tools.ApprovalBroker` → `chat.updateApproval` | `TestWriteRequiresInteractiveApproval`, `TestInteractiveWorkspaceApprovalIsScopedToActiveRole` |
| Chat/input geometry is theme-invariant | `layout.Compute` takes no theme input | `TestThemeDoesNotMoveChat`, `TestHeroNeverCollides` |

## One orchestrated run (hierarchy router)

1. `State` is created with the brief; the brief is injected as `user → ceo [brief]`.
2. **CEO frames**: answers alone (`ROUTE: answer` → FinalOutput, run ends) or delegates
   (`ROUTE: delegate` → `direction` to COO, with the brief appended verbatim by Water).
3. **COO assigns**: `## cto` / `## design` sections become `assignment` messages with correlation
   ids and deadlines; a role without a section is left out.
4. **Specialists** run in parallel, reply with `deliverable` to COO; `DISSENT:` paragraphs become
   `Verbatim` dissent to COO; `ESCALATE:` paragraphs go straight to the CEO.
5. **COO verifies**: Water first forwards every verbatim message to the CEO with attribution;
   then the COO's `status` (each item `VERIFIED:` / `UNCONFIRMED:`) goes up. Follow-up
   assignments are allowed while rounds remain (`orchestration.max_rounds`).
6. **CEO adjudicates**: explicit decisions with tradeoffs and reversal conditions →
   `ROUTE: final` (or `ROUTE: redirect` once, if rounds remain).
7. After every superstep the `FileCheckpointer` writes `~/.water/checkpoints/<run>.json`;
   a node failure still saves completed siblings; `--resume <id>` re-derives the phase from
   visit counts and unconsumed inboxes and re-runs only what did not complete.

Phase is derived from State (`Visits`, `Unconsumed`, pending assignments by correlation id),
never from router-internal counters, which is what makes resume trivial. A step that changes
nothing is a stall (`ErrStalled`); the step budget and whole-run timeout bound the rest.

## Interactive session

`water chat` renders HEADER / HERO / CHAT / INPUT / STATUS regions computed by `layout.Compute`
from terminal size alone. Themes (`themes/<slug>.yaml`) supply palette and hero art; art is
scaled into a fixed hero box and dropped when the terminal is too small. Transcripts are per-role
JSONL under `~/.water/sessions/<role>/`; `Open` reads only the tail after the last summary
checkpoint, so compaction never needs the whole file.

When launched from a directory, chat also creates a session-only local workspace policy for its
active role. Reads/listing inside that directory are permitted; every write or shell command enters
a fixed-height approval card in CHAT and waits for `y`/`n`. The MCP child is connected to the TUI by
a private Unix socket, so it never prints its own prompt over the terminal. The policy is copied only
for the active role, so `/consult` cannot inherit it. Headless sessions and orchestration do not use
this overlay.

## Extension points

| Interface | Implementations | Add by |
|---|---|---|
| `backend.Backend` | claude-subscription, codex-subscription, api | new file + `Default.Register` |
| `memory.Provider` | markdown | `memory.Register` |
| Role | ceo, coo, cto, design | a folder under `agents/` (+ `themes/<slug>.yaml`) |
| `persona.SkillSelector` | description (default), keyword | `persona.Selectors` |
| `orchestrator.Router` | hierarchy (default), ceo-fanout | `orchestrator.RegisterRouter` |
| `orchestrator.Checkpointer` | file (default), noop | config `orchestration.checkpointer` |
| `voice.Provider` | os, noop | `voice.Register` |
| `surface.Surface` | terminal, json (dashboard reads traces) | `surface.Register` |
| `editor.InputEditor` | Bubbles v2 textarea | implement the interface |

## Exit codes

0 ok · 1 error · 2 usage / prerequisite · 3 backend unavailable, metered refused, or run failed
(checkpoint saved) · 4 unconfigured
