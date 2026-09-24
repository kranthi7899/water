# Evolution plan: council to CEO twin

Status: **A3 done (2026-09-23). Next: Slice A4 (morning brief).** Context: `docs/CONTEXT.md`.

## Constraints decided with the owner
- **Zero metered spend.** Model calls go through the Claude subscription (`claude` CLI) only. Voice is free and on-device: Apple `SFSpeechRecognizer` (on-device recognition) and `AVSpeechSynthesizer` / `say`. Twilio, the X API and paid speech services are deferred.
- **Latency comes from architecture.**
  - A warm `claude` stream-json process stays running between turns.
  - Replies are spoken sentence by sentence as they stream.
  - Schedule, brief and approvals questions are answered from the store without a model call.
  - Longer work gets an instant "on it" and runs in the background.
  - Targets: about 1s for state-backed answers, 1.5–3s when the model is needed.
- **Subscription usage windows replace a dollar budget.** Auto mode (P2) gets a per-window call cap, fed from `backend/ratelimit_store.go`.
- **Execution:** one remote cloud run per slice, reviewed before the next. Opus runs A1 (the security core). Sonnet runs the rest.

## Where the prompt's assumptions differ from the repo
- There is no Steve Jobs material in the repo or its history. "Persona material" means the CEO's `soul.md`, `experience.md`, `reasoning.md` and `.index.json`.
- There is no Researcher role. The dormant roles are COO, CTO and Design.
- Nothing streams today. `backend/exec.go` buffers the whole CLI output, and `backend/api.go` doesn't stream.
- There is no daemon, no Keychain code and no connector layer. The only Unix socket is the per-session approval broker.

## Keep / move / retire

**Amended by the owner, 2026-09-23 (during A2):** the council is **deleted
outright, not kept dormant**. Water is a single personal agent. The table
below reflects what actually happened, not the original A1-era plan.

| Existing | Fate |
|---|---|
| `agents/` (all four roles: souls, experience, reasoning, skills, memory seeds, role.yaml, `.index.json`) | **Deleted**, not archived. The CEO's role — responsibilities and environment, not a personality — lives in `twins/ceo/role.md`. Persona files survive only in git history. |
| `orchestrator` (executor, checkpointer, both routers, the state graph, `AgentMessage`/outbox) | **Deleted entirely**, including the executor and checkpointer. Pipelines (brief, recap, packets) will be plain Go functions in a later slice, not a state graph. |
| `agent` (`Assemble`, `call`, `RunTurn`, `Consult`, `ceoNode`/`cooNode`/`specialistNode`) | **Deleted.** `internal/runtime` (built in A2) was written fresh against the twin/gate/store shape and never depended on this package, so nothing needed porting. |
| `roles`, `persona` (incl. the skills loader), `identity` (role_id/content_hash + the HMAC keyring), `experience`, `diagnose`, `dashboard`, `surface`, `trace` | **Deleted.** The audit log and session/turn events replace `trace`; there is no per-role anything left to register, sign, or diagnose. |
| `chat`, `editor`, `session` | **Deleted.** Both were reachable only through the deleted `run`/`build-site` commands and did not compile against a council-free tree; `water chat` is now a plain daemon-client REPL (`internal/cli/cmd_chat.go`), and transcript persistence is left for a later slice. |
| `backend` | Kept. Model tiers, `RunStream`, and a warm session process were added in A2. The API backend stays opt-in only. |
| `tools` | Trimmed to what the twin uses: the MCP server (now in twin mode, proxying connector calls to the daemon's gate), `WritePolicyFile`/`LoadPolicyFile`, and `ResolveWithinRoots`/`ExpandRoots` (kept for a future connector, unused today). Deleted: the interactive-workspace read/write/run/apply_actions/open_page tools, the `ApprovalBroker` (the daemon's queue replaces it), and the shell sandbox. |
| `gate`, `approvals`, `audit`, `store`, `connectors`, `vault` (Slice A1) | Kept, extended in A2: rate/usage windows and the audit anchor persist in the store; `water audit verify`/`repair`. |
| `memory`, `config`, `auth`, `voice`, `guards` | Kept. `memory` is not yet wired into the twin's runtime (a later slice); `config` still carries some now-unused per-role/orchestration knobs (harmless, not cleaned up); `guards` lost the council-only tests and kept/ported the rest (strict MCP config, `--tools ""`, metered-leak/`ScrubbedEnv`, the A1 gate guards). |
| `theme`, `layout`, `themes/*.yaml`, `water.ThemesFS()` | **Deleted in A3.** Unwired dead weight since the council was deleted in A2; nothing referenced them outside their own tests. |
| `run`, `orchestrate`, `build-site`, `diagnose`, `dashboard`, `persona`, `experience`, `skills`, `replay`, `debug dump`, `memory` (CLI) | **Deleted.** `/consult`, `/switch`, `/agents` went with the TUI that had them. |

The new layout follows the tree in CONTEXT.md. Two approved dependencies are added, both pure Go so the binary stays static: `modernc.org/sqlite` and `modelcontextprotocol/go-sdk` (MCP client only). The Keychain is reached through the `/usr/bin/security` CLI behind a `Vault` interface, so it needs no cgo.

## Slices and acceptance tests
Every phase must pass vet, test and a `CGO_ENABLED=0` build.

1. **A1 (remote, Opus): foundation.**
   - Build: `gate` with R/D/A/S/B, default deny, untrusted tagging, rate and usage caps; `approvals` (payload-hash envelopes, a persisted queue, code-generated read-backs, a deterministic yes/no matcher); `audit` (hash-chained, 0600, fails closed); `store` (SQLite, migrations, eight record types with source IDs); `connectors` (the contract plus a fake connector); `vault`.
   - Tests: an A-level call without an envelope is refused; an edit voids the approval; untrusted-derived parameters are refused at S; every call is audited and the chain verifies; caps trip; tokens never appear in prompts or logs.
2. **A2 (remote, Sonnet): daemon and conversation plane.**
   - Build: `water daemon` on a 0600 Unix socket with per-client tokens; streaming turns (cli, voice, text-bar); approvals, state and cancel endpoints; `water chat` and `water ask` as clients; a LaunchAgent install command; `RunStream`, the warm session process and deterministic fast paths; retire the council wiring.
   - Tests: streaming, cancel, bad-token rejection, socket permissions.
   - On the laptop: measure time to first token.
3. **A3 (done): Google Workspace (Calendar, Gmail, Drive/Docs/Sheets), read-only.**
   - Remote: code and fixture tests.
   - Laptop: an internal GCP app, OAuth consent, and the live test suite.
4. **A4 (remote): morning brief pipeline.** Ranking signals are computed in code, and the model only writes the text.
5. **B (laptop-heavy): voice and clients.**
   - A Swift menu-bar app with a push-to-talk hotkey and a text-bar hotkey. Replies are spoken sentence by sentence, and approvals are confirmed by voice.
   - Interim: a macOS Shortcut that calls `water ask`.
   - A Slack connector.
6. **C (remote): decisions and planning.** Playbooks (budget, investor, hiring) and packets with a readiness state; the meeting-recap recipe; the action-plan scheduler; the four validated templates; the P0/P1/P2 queue with auto mode; the learning log.
7. **D: remaining connectors.**
   - In order: transcripts (Meet first), GitHub, Linear/Jira, HubSpot, QuickBooks sandbox, LinkedIn, file creation.
   - Remote: code. Laptop: credentials.
8. **E (remote): manifest generalization and twin-to-twin loopback.** Includes a second twin that needs no code changes.

**Deferred:** Twilio and X (both cost money), project health rubrics, dependency linking, the department layer, a phone client, wake word, and barge-in.

**Deferred, tracked separately (owner's long-term concern, 2026-09-23): host the daemon off the laptop.**
Right now `water daemon` only does anything while the Mac is awake (macOS suspends background processes on sleep), so background sync and Gmail push (once built) only work while the laptop is up. That's fine for now but won't scale to "always available." The eventual fix: run `water daemon` on a small always-on machine (e.g. a Mac mini, since it still needs to run the `claude` CLI logged into the subscription), with the laptop, phone, etc. becoming clients that just talk to it over the network instead of running it locally. Not scheduled to any slice yet; revisit once the core agent loop (through Slice C) is solid.

## Log
- 2026-09-23: Step 0 done. Reasoning-layer work committed on `feat/build-site`, branch `feat/ceo-twin` created, and CONTEXT.md, CLAUDE.md and this plan written.
- 2026-09-23: **A1 done.** New packages: `store`, `audit`, `canon`, `twins` (with `twins/ceo/twin.yaml`, embedded), `gate` (+ `gate/permit`, `gate/internal/mint`), `connectors` (+ `connectors/fake`), `approvals`, `vault`. The council code is untouched.
  - **How the gate is made unbypassable.** A connector's `Invoke(ctx, permit.Permit)` gets its arguments and credential only by redeeming the permit (`p.Open()`), and it can do that once. Permits are minted in `internal/gate/internal/mint`, which Go's internal-directory rule lets only `internal/gate/...` import. `gate/permit` re-exports the type, so connectors can accept a permit but cannot make one. The gate also fails any call where the connector returned without redeeming. A guard test checks all of this, and also scans the source to confirm that only the gate imports the mint or calls `Invoke`, and that every connector redeems as its first statement.
  - **Other choices:**
    - Taint's zero value is "unknown" and counts as tainted.
    - A tainted call to an S function escalates to needing an approved envelope. Tainted R and D calls are allowed because nothing leaves.
    - A manifest may grant a function's declared level or tighten it to A or B, never loosen it. `gate.New` enforces this.
    - Approvals bind the sha256 of the canonical payload and must also match the action. A mismatched call voids the approval. Claims are compare-and-set, and a stored row whose payload no longer matches its hash is refused.
    - Every audit write comes before the effect it records, and an audit failure fails the action. An approval that can't be audited is reverted to denied.
    - `audit.Open` refuses to extend a chain that doesn't verify.
    - Rate and usage windows are in memory for now, so the A2 daemon holds them.
    - P2 model calls pause while the persisted subscription status isn't "allowed".
    - Added one audit kind, `propose`, for a new envelope entering the queue.
  - **Known limit:** truncating the tail of the audit log isn't detectable without an external anchor for the last hash. Consider one in A2.
  - **Verify on the Mac:** `WATER_KEYCHAIN_TEST=1 go test ./internal/vault`. It passed during the run, but check it from a normal login session, and check that reads by `/usr/bin/security` never prompt. Secrets must be single-line because they go in on stdin.
- 2026-09-23: **Owner decision, mid-A2:** the council is deleted outright, not kept dormant. Water is a single personal agent from here on; there is no persona archive. This replaced Phase 4 of `docs/slices/A2.md` and is reflected in the keep/move/retire table above and in `docs/CONTEXT.md`'s amendment.
- 2026-09-23: **A2 done.** `water daemon` (HTTP over a 0600 Unix socket, single-instance flock, bearer-token clients) now owns the gate, approvals, audit, store and model sessions; `water chat`, `water ask` and `water approve` are its clients. New: `internal/runtime` (context assembly from `twins/ceo/role.md` + a state summary, a phrase-based zero-model-call fast path for schedule/approvals/brief questions, sentence-by-sentence voice streaming), `internal/gateway` (the daemon itself, plus a Twin extension to `internal/tools` that proxies model-initiated connector calls from an MCP-serve child to the daemon's gate). `internal/backend` gained `RunStream`, `WarmSession` (one persistent `claude` process reused across turns, restarting on crash/system-prompt/model/tool-scope change or after 40 turns) and model tiers from `twin.yaml`. Deciding a pending approval "yes" now executes the action in that same call, exactly once. Gate rate/usage windows and the audit log's tail now persist in the store (`water audit verify`/`repair` round out A1's open items). Then the council was deleted per the owner's decision above: `agents/`, `orchestrator`, `agent`, `roles`, `persona`, `identity`, `experience`, `diagnose`, `dashboard`, `surface`, `trace`, `chat`, `editor`, `session`, and the CLI commands that only served them.
  - **The stable-token deviation.** The spec sketch implied a token per turn for the model's tool-proxy auth. That cannot work with a warm session: the MCP bridge child a warm process spawns reads its `--mcp-config` policy file once, at its own startup, and then serves every turn in that process's life — so the token baked into it cannot rotate per turn without forcing a process restart on every single turn, which defeats the warm session. A2 instead mints one long-lived, session-scoped proxy token, and escalates its taint (never resets it) the first time any turn's assembled context includes external content. This is the conservative direction to err in given the constraint.
  - **Verified once by hand against the real `claude` CLI** (never in tests, which use `backend.Fake` or a fake-CLI shell script): streaming deltas and voice-channel sentences, `--include-partial-messages` actually needed adding to the warm session's own args (a bug caught only by this manual check — the automated warm-session tests use a fake CLI that doesn't need the real partial-message shape), a model calling `fake_calendar__list_events` and `fake_calendar__create_event` over MCP, the `create_event` call landing in the approval queue, and approving it executing exactly once. Cold spawn vs. warm reuse measured on this Mac: about 1.47s vs. 0.77s for a trivial turn.
  - **Left undone / for later slices:** `water memory` has no replacement command and long-term memory is not wired into the runtime yet; transcript persistence for `water chat` (the old per-role `session` package was deleted, not replaced); `theme`/`layout`/three of the four `themes/*.yaml` are dead weight, not deleted; `config` still carries unused per-role/orchestration knobs; the model-tool bridge's "session taken as one unit" taint model should be revisited once P1/P2 scheduling (auto mode) exists and more than one logical conversation can be in flight.
  - **Verify on the Mac:** `water daemon install` and `launchctl` loading it (unit-tested only as plist rendering); that a second `water daemon` truly refuses to start when the first is running under launchd, not just in-process; time-to-first-token specifically (this run measured whole-turn latency, not first delta) for warm vs. cold.
- 2026-09-23: **A3 done.** The twin can now read the CEO's real Calendar, Gmail and Drive, read-only, in place of the `fake_*` connectors. Built as a workflow: one Opus foundation phase, three connectors and a cleanup pass in parallel, then this integration pass.
  - **New packages.** `internal/connectors/google/gapi`: one Keychain credential (`water.google/<account>`, single-line JSON) shared by all three connectors, an in-memory access-token cache keyed by a hash of the credential (refreshed ahead of expiry or once on a 401, with truncated exponential backoff on 429/5xx/rate-limited-403), the PKCE loopback `Authorize`/`Revoke` flow, and scrubbing (exact-string plus a regex over `ya29.*`/`1//*`/`GOCSPX-*`/`4/*`) so no token ever reaches an error, the audit log or a store row. `gcal` (`list_events`), `gmail` (`list_messages`, `get_message`), `gdrive` (`search_files`, `read_file`) — every function `External: true`. Docs/Sheets/Slides read through Drive's `files.export`, so no scopes beyond the three read-only ones are needed.
  - **Wiring.** `twins/ceo/twin.yaml` now lists only the six Google functions (level R, per-function rate caps); `auto_allowlist` holds just `gcal.list_events` and `gmail.list_messages`. The `fake_*` connectors left the production manifest entirely — they now live only in the gate's, guards', and gateway's own inline test manifests (those tests already defined their own manifests and needed no changes; two tests that loaded the *embedded* `twins/ceo/twin.yaml` — `TestEmbeddedCEOManifestLoads` and `TestEmbeddedCEOManifestBuildsAGate` — were updated to check the real Google functions instead of `fake_mail.*`).
  - **`water connect google --client-file <path> [--account] [--status] [--revoke]`** runs the loopback flow (prints the URL and opens it with `/usr/bin/open` on macOS), stores the credential, and never prints a secret. `--status` forces a live refresh; `--revoke` revokes at Google and deletes the Keychain entry. `water doctor` reports connected/not-connected by presence only (no network call, so `doctor` stays fast and offline-safe).
  - **Background sync (`internal/sync`).** The daemon starts one `Refresher` tied to its own shutdown context: once right after start and then every `sync.interval_minutes` (default 10), it calls `gcal.list_events` (today through +7 days) and `gmail.list_messages` (`newer_than:1d`) through the gate at origin **P2** with **Clean** taint. It checks the vault for the shared credential before calling anything, so an unconnected twin logs one line instead of a stream of gate denials every interval. Its function ids and the credential it checks for are both overridable, so tests exercise it entirely with the `fake_*` connectors and a memory vault — no network, no real Google.
  - **The taint fix.** A2 built `escalateTaint` (sticky, never resets) but never called it from the one place that matters most: `handleToolInvoke`, the model's own tool-call path. A3 makes an `Untrusted` gate result there escalate the session's stable tool-proxy token, so a model that just read an email is tainted for every later call in that session, not only for taint computed once at context-assembly time.
  - **The fast-path fix.** `scheduleAnswer` and `runtime.StateSummary` used to `List` the 500 newest records by `created_at` and filter by `start_at` in Go — a background sync backfilling an old event, or just enough other records accumulating, could push a today-starting event out of that window. Added `store.EventsInRange(ctx, s, from, to)`, a direct `start_at` range query with no dependency on insertion order, and pointed both call sites at it.
  - **Cleanup, done alongside this slice by a parallel agent:** `internal/theme`, `internal/layout`, `themes/*.yaml` and `water.ThemesFS()` deleted (dead since the council left in A2); `internal/config` and `internal/voice` collapsed to what a single CEO twin actually uses (no more COO/CTO/design voices; old config files with the retired orchestration/sessions/tools/skills/ui/telemetry/memory keys and per-role voices still load, silently dropping them).
  - **Left undone / for later slices:** incremental sync (Calendar's `syncToken`, Gmail's `historyId`) — A4 territory, since the morning brief needs exactly that kind of "what changed" signal; write functions (`send_email`, `create_event`) are C/D; Docs/Sheets-specific APIs beyond `files.export` (comments, cell ranges) are undone.
  - **Verify on the Mac:** the whole of `docs/google-setup.md` end to end on a personal Gmail account — the consent screen's "In production" + unverified path really does avoid the 7-day Testing-mode token expiry; `water connect google --status` after a real day has passed; `water daemon` running long enough to see two sync intervals fire and check `water audit verify` afterward; `water ask "what's on my schedule today?"` answering from real data with zero model calls; `water ask "summarize my unread email from today"` actually calling the Gmail tools (visible in the audit log) rather than refusing or hallucinating.
