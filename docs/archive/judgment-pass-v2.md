# Infrastructure judgment pass v2

2026-09-14. Compared against the public source of `openai/codex` (commit `973ec29`) and
`NousResearch/hermes-agent` (commit `1ab32b2`), both shallow-cloned and read directly. No
reconstructed or decompiled Claude Code source was used. Every gap below was reproduced against
Water before it was fixed, and every fix has a test that fails on the old behaviour.

**Scorecard.** 13 areas: 3 adopt, 5 adapt, 5 reject. Debugging gaps (6a–6d) were fixed first, as
the brief asked.

| Area | Verdict | Change |
|---|---|---|
| 1 Permission model | adapt | shell allowlist refuses compound commands; no shell without an OS sandbox; confirm-each refused |
| 2 MCP handling | adapt | concurrent calls, per-call deadline, regular files only, exit on stdin close |
| 3 Session/resume | reject (confirmation) | none: Codex validates the consume-after-success fix |
| 4 Config layering | reject | none |
| 5a Memory isolation | adapt | water home protected from tool roots; cross-process memory lock; `/remember` refuses untrusted replies |
| 5b Curator | adapt | read-only `water skills` usage report; no archiving or consolidation |
| 5c Execution backends | reject for now | none; pattern recorded for when shell is granted |
| 5d Reflect / update-skill loop | reject | none; no evidence the earlier decision no longer holds |
| 5e External memory providers | reject | none |
| 6a Single-node replay | adopt | call records + `water replay` (`--print`, `--edit`) |
| 6b Verbose / live debug | adapt | `--debug` logs real subprocess flags, pid, duration, exit |
| 6c Actionable failures | adopt | real error line surfaced; backend, model and timeout setting named; duplicate output removed |
| 6d Stuck-run diagnosis | adopt | `water debug dump <run-id>`: live state + in-flight subprocesses + goroutines, never blocks |
| 6e Comparanda observability | covered by 6a, 5b | `codex debug prompt-input` → `replay --print`; Curator report → `water skills` |

---

## 1. Permission / approval model (vs. Codex)

**Codex.** Approval is a policy enum (`protocol/src/protocol.rs`: `untrusted`, `on-request`,
`granular`, `never`) combined with an exec-policy decision of `allow`, `prompt` or `forbidden`
(`execpolicy/src/decision.rs`). Ambiguous commands are not classified exhaustively. Codex parses a
few argument-sensitive cases to label actions (`shell-command/src/parse_command.rs` treats `sed` as
a mutation only with `-i`, and checks `xargs` subcommands), keeps a dangerous-command matcher for
things like forced `rm`, and for everything else relies on the sandbox:
`render_decision_for_unmatched_command` in `core/src/exec_policy.rs` allows an unmatched,
non-dangerous command in a restricted sandbox "and let the sandbox enforce restrictions", and falls
back to prompting when no sandbox backend exists. Mode changes mid-session go through the same
policy object. Decisions are logged as a `codex.tool_decision` telemetry event with tool name, call
id, decision and source (`otel/src/events/session_telemetry.rs`).

**Water.** Decisions are traced with a richer basis than Codex's (the exact rule and root), so
logging is not a gap, and Water has no mid-session escalation by design. The ambiguous-command case
was a real gap. The allowlist compared only the first word, so allowlisting `ls` authorised
`ls && touch …`, `ls $(…)` and `ls | sh`, and `sed` authorised `sed -i` (reproduced). On Linux
the sandbox wrapper silently returned the command unconfined. `confirm-each` behaved exactly like
the allowlist, because Water has no approval channel inside a tool call. No role is granted shell
today, so this was reachable only by configuration, but it would have been reached the first time
anyone granted it.

**Verdict: adapt.** Take Codex's principle, not its parser: argument effects are the sandbox's job,
and nothing runs where no sandbox exists. The allowlist now refuses shell metacharacters and execs
the words directly without a shell; any shell mode is refused when no OS sandbox is available;
`confirm-each` is refused with a reason. Test: `TestShellAllowlistRefusesCompound`.

## 2. MCP client/server handling (vs. Codex)

**Codex.** The MCP client bounds every stage of a server's life: a 30-second startup timeout and a
300-second default per-call timeout, taking the tighter of server and request deadlines
(`codex-mcp/src/rmcp_client.rs`, `binding.rs`); a stdio transport that closes the connection
rather than buffering a line over 8 MB (`rmcp-client/src/bounded_stdio_transport.rs`); and
request/response correlation through the `rmcp` service so concurrent calls do not wait on each
other.

**Water.** Water is the server side, and `mcp-serve` handled requests strictly one at a time with
no deadline. Reproduced with a named pipe under a declared root: one `read_file` blocked in
`open()` forever, a `ping` and a normal read queued behind it got no reply, and after stdin closed
the process stayed alive, so it would outlive the `claude` subprocess that spawned it. Reachable
with any FIFO, device or slow network mount under a root. Cross-node concurrency was already safe,
since each node call spawns its own server with its own log file.

**Verdict: adapt.** `tools/call` requests are handled concurrently with serialised writes; each
runs under a 60-second deadline that also abandons a blocked filesystem call; `read_file` refuses
anything that is not a regular file; the server returns as soon as stdin closes. Re-running the
reproduction: all three replies arrive immediately, the pipe gets a clear refusal, the process exits.
Tests: `TestMCPHungCallDoesNotBlockServer`, `TestReadNonRegularFileRefused`.

## 3. Session / resume (vs. Codex)

**Codex.** A session is a thread with a UUID encoded in its rollout file name
(`rollout/src/rollout_file_name.rs`), resumed by id or with `--last`. The rollout is an
append-only log of completed items; a turn is closed by an explicit `TurnComplete` event written
after it succeeds, or a `TurnAborted` marker on interrupt (`rollout/src/model_context.rs`). When a
snapshot ends mid-turn, resume and fork cut back to the last completed boundary
(`core/src/thread_manager.rs`, `ForkSnapshot::Interrupted`). Completion is recorded, never
inferred from work having started.

**Water.** The checkpoint bug Water fixed was exactly the inference Codex's design avoids: a hub's
inbox was marked consumed before its call succeeded. Water now records consumption after success,
counts a visit only for a completed node, repairs older checkpoints on restore, and requires the
caller to pass the run id back. Chat transcripts already behave like Codex's rollouts: a user line
with no assistant reply after it is dropped from the resumed context.

**Verdict: reject (confirmation).** Codex's approach validates the fix; no change.

## 4. Config layering (vs. Codex)

**Codex.** Layers with numeric precedence: packaged defaults, MDM, system file, enterprise-managed
bundle, user file with optional profile, per-project `.codex` folder, session flags
(`config/src/config_layer_source.rs`), with per-key origins recorded (`config/src/fingerprint.rs`)
and admin "requirements" that constrain values.

**Water.** Defaults, one file, `WATER_*` environment, flags, with per-key provenance shown by
`water config`.

**Verdict: reject.** Codex's extra layers solve fleet management and per-project settings; a
single-user CLI has neither, and per-project config would reintroduce the working-directory context
Water deliberately strips from subprocesses.

## 5a. Memory isolation — faithfulness check (vs. Hermes)

**Hermes.** Memory is `MEMORY.md` and `USER.md` in the active profile's home, resolved per call
(`tools/memory_tool.py`, `get_memory_dir()`), so isolation is by directory: one profile per home.
Within a profile, delegated subagents are denied the memory tool by a blocklist
(`tools/delegate_tool_toolsets.py`). The store (`tools/memory_tool_store.py`) goes further than
Water's borrowing captured: character budgets, a frozen system-prompt snapshot, an exclusive
`fcntl`/`msvcrt` file lock around writes, a refusal to write when the on-disk file would not
round-trip (manual edit or concurrent session), treating an unreadable file as an error rather than
empty, and a scan for injected instructions on write and on load. Separately,
`agent/file_safety.py` denies the agent's own file tools access to Hermes's home (credentials,
`state.db`, sessions), as defence in depth.

**Water.** For several agents in one process, Water's boundary is structurally stronger than
Hermes's: a scoped handle with no role argument, one prompt assembler, and a guard test that plants
a secret in each role. Three places relied on convention, and all three were reproduced.
(1) **Tool roots.** A declared root containing the water home (for example `tools.roots: "~"`) let
the CTO read Design's memory file and the signing keyring — invariant 2 broken through the tool
layer, the gap Hermes's `file_safety` closes. (2) **Cross-process writes.** Memory used an
in-process mutex only; two providers on one directory (two sessions of the same role) lost exactly
half of 80 concurrent writes. (3) **Implicit promotion.** `/remember` with no note stored the last
reply even when that reply had read an attachment, putting possibly injected text into every future
prompt. Water already met Hermes's read-failure guard (an unreadable file is an error, not empty).

**Verdict: adapt.** Tool policies now carry a protected-path list set to the water home, checked
after symlink resolution, for read, write and list. Memory writes take an exclusive advisory lock
per role. `/remember` without a note refuses when the last turn consumed untrusted content; an
explicit note is still allowed. Hermes's content-scanning is not adopted: Water's memory is written
only by the user, and Water prefers capability restriction to content filtering. Tests:
`TestToolRootsCannotReachWaterHome`, `TestConcurrentWritersLoseNothing` (fails with 40 of 80
entries when the lock is removed), `TestRememberRefusesUntrustedPromotion`.

## 5b. The Curator (vs. Hermes)

**Hermes.** `agent/curator.py` runs when the agent is idle and the last run is older than seven
days. It deterministically moves agent-created skills from active to stale (14 days unused) to
archived (30 days), never deletes, never touches pinned or cron-referenced skills, optionally runs a
model pass to consolidate overlapping skills (off by default), and writes `run.json` and
`REPORT.md` per run. It depends on per-skill usage records (`tools/skill_usage.py`).

**Water.** Water's promotion pipeline is real but narrower: `water experience grow` and
`candidates` exist and are tested, and promotion into a skill is a reviewed `persona edit`. There
was no consolidation, archival or usage evidence for skills at all. Most of the Curator does not
fit: Water's skills are hand-written, signed identity files, never agent-created, so automatic
archiving would violate invariant 4. The usage-evidence half fits well: Water's own frameworks carry
the rule "a tool that never fires is a drop candidate", and traces already record which skills
loaded on every call.

**Verdict: adapt (deterministic report only).** `water skills` aggregates loaded skills from
traces and chat transcripts and lists never-loaded skills per role. It writes nothing. On 47 real
assembled prompts it already surfaced two review items: Design's `fitts-law` and `hicks-law` have
never loaded, and the CEO's `disagree-and-commit` loaded 18 times despite a trigger meant for a
genuinely split team, which points to an over-broad description.

## 5c. Multi-backend execution (vs. Hermes)

**Hermes.** `tools/environments/base.py` defines `BaseEnvironment` with `_run_bash()` and
`cleanup()`, and implementations for local, Docker, SSH, Singularity, Modal, Daytona and Vercel,
chosen by config (`tools/terminal_tool_backends.py`). The Docker backend is hardened by default:
`--cap-drop ALL`, `no-new-privileges`, PID and resource limits, network off unless enabled,
read-only bind mounts, non-root user (`tools/environments/docker.py`).

**Water.** The pattern fits Water's one existing seam: `tools.Sandbox()` wraps `exec.Cmd`, and an
opt-in container backend would slot in there without becoming a default dependency. But no role has
shell access, and the section 1 fix now refuses shell wherever no sandbox exists. Building a backend
abstraction now would add surface with no user.

**Verdict: reject for now, with a recorded trigger.** When a role is first granted shell, implement a
`ShellBackend` interface behind `Sandbox()` with two implementations: the existing macOS Seatbelt
wrapper, and an opt-in Docker backend copying Hermes's hardening flags. Selection goes in
`role.yaml`; the one-binary install stays the default.

## 5d. Agent loop shape (vs. Hermes)

**Hermes.** After each turn, `agent/background_review.py` forks the agent in a background thread,
replays the conversation, asks whether any memory or skill should be saved or updated, and writes
directly into both stores under a tool whitelist. That is Plan, Execute, Reflect, Update skill in
practice, and it is autonomous.

**Water.** Water's node contract (load persona and frozen memory, read own inbox, call backend, emit
typed messages) deliberately has no reflect step; growth is offline and reviewed. Checked against
Water's actual data rather than Hermes having one: across every water home on this machine there are
3 real chat turns, 0 flagged replies, and no growth log on the shipped personas. The 6 CEO lessons
that meet the promotion threshold all came from the original research seeding, not from use.

**Verdict: reject.** No concrete case shows the earlier decision no longer holds. Revisit when flagged
replies accumulate and the manual `grow` step becomes the bottleneck; even then, the evidence would
support a thin bridge from `/flag` to `grow` inputs, not an autonomous writer.

## 5e. External memory providers (vs. Hermes)

**Hermes.** Optional semantic and graph memory plugins (Honcho, Mem0, Supermemory) behind a
provider setting.

**Verdict: reject, considered.** Water's memory is deliberately bounded, manually curated and
non-semantic: a fixed identity anchor is what keeps four roles distinct. Retrieval-based memory would
make what each role knows depend on similarity search, which is the drift the design exists to
prevent. The provider seam (`memory.Register`) remains if that judgment ever changes.

## 6a. Single-node replay

**Comparanda.** Codex ships `codex debug prompt-input`, which renders the model-visible input, and a
rollout-trace bundle with a reducer that rebuilds state from raw events
(`cli/src/main.rs` `DebugSubcommand`, `rollout-trace/src/lib.rs`).

**Water, before.** Traces recorded only the ids of skills, memory entries and inbox messages per
call, never the prompt text. One node's exact input could not be seen or replayed, so a fan-out bug
could only be chased by re-running the whole graph.

**Verdict: adopt.** Every model call now writes `<trace_dir>/<run-id>.calls/<seq>-<role>.json` (0600)
with the exact system and prompt, backend, model, tools, ids, response, error and duration, linked by
a `call_recorded` trace event. `water replay <run-id>` lists them; `replay <run-id> <seq|role>
--print` shows the model-visible input; `--edit` opens it in $EDITOR; the replay goes to one backend
call and is saved beside the original without touching the run, its checkpoint or any memory.
Verified on a real run: six calls listed, the CTO's input printed, call 4 replayed. Tests:
`TestRecordCallWritesReplayableInput`, `TestPickRecord`. Records hold persona and memory text,
which stays local like checkpoints.

## 6b. Verbose / live debug mode

**Water, before.** `-v` is not a stub: it prints each node's response when the node finishes. What
was missing for building was the layer below, which is where Water's worst bug lived: the connector
leak was a missing subprocess flag, invisible in both `-v` and the trace.

**Verdict: adapt.** `--debug` (or `WATER_DEBUG=1`) logs every subprocess start and exit: pid, the
real flags with prompt-bearing values replaced by their size, working directory, timeout, duration,
exit error and the last stderr line. Verified on a real Claude call:
`claude --print --output-format stream-json --verbose --system-prompt <9029 bytes> --tools "" --no-session-persistence --disable-slash-commands --strict-mcp-config -- <prompt 97 bytes>`. Test: `TestErrorSummaryFindsTheRealError` covers redaction.

## 6c. Actionable failure messages

**Water, before.** Spot-checked four real failure paths. Unknown role and unknown router were already
good. A bad Codex model reported only "Reading additional input from stdin..." because Codex prints a
banner first and the real cause as JSON on its last line. A call timeout said "timed out after 1s"
without naming the setting, and appended the model's partial output. `water run` printed every
failure and success twice.

**Verdict: adopt.** Errors now show the most informative line (last error line, JSON message
unwrapped), name the backend and the role.yaml model when one is set, name
`orchestration.call_timeout` on a timeout, and print once. Verified: "backend codex-subscription,
model \"no-such-model-xyz\" from role.yaml: … The 'no-such-model-xyz' model is not supported when
using Codex with a ChatGPT account."

## 6d. Stuck-run diagnosis

**Water, before.** The only way to see inside a hung run was to kill it. `go test -race` is clean
across the suite, so a hang would have been undiagnosable exactly when it mattered.

**Verdict: adopt.** A live orchestration writes a pid file and handles SIGUSR1.
`water debug dump <run-id>` signals it and prints a dump: active nodes and how long each has run,
model subprocesses in flight with pid and flags, what the router would schedule next, pending
assignments, the outbox tail, and every goroutine's stack. The state read gives up after two seconds
and says "state mutex held" rather than hanging the diagnostic too. Verified mid-run on a real
orchestration. Test: `TestTrySnapshotNeverBlocks`. Unix only; Windows reports that SIGUSR1 is
unavailable.

## 6e. The comparanda's own observability

**Codex** pairs OpenTelemetry events (`codex.tool_decision`, sandbox outcomes) with the rollout
trace and `debug prompt-input`. **Hermes** writes a per-run Curator `REPORT.md`, an `agent.log`, and
has `hermes doctor`. Water already had the doctor and trace equivalents. The two pieces worth taking
were taken: prompt-input as `replay --print` (6a) and a per-run-style usage report as `water skills`
(5b). OpenTelemetry is rejected: Water is a single-user local tool whose JSONL trace is already the
source for diagnose and the dashboard, and an exporter would add a network path to a tool that
promises nothing leaves the machine.
