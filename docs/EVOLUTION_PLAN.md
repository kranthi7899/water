# Evolution plan: council to CEO twin

Status: **A1 done (2026-09-23). Next: Slice A2.** Context: `docs/CONTEXT.md`.

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
| Existing | Fate |
|---|---|
| `orchestrator` executor and checkpointer | Keep as the engine for the pipelines. Retire the hierarchy and fan-out routers. |
| `agent` (`Assemble`, `call`, `RunTurn`) | Refactor into `internal/runtime`. Delete `ceoNode`/`cooNode`/`specialistNode` and `Consult`. |
| `backend` | Keep. Add model tiers, `RunStream` and a warm session process. The API backend stays opt-in only. |
| `tools` (Policy, root confinement, atomic write, approval broker, MCP server, untrusted wrap) | Becomes the basis of `gate` and `approvals`, with access levels R/D/A/S/B. |
| `trace` | Keep for pipeline runs. It is not the audit log: no hash chain, files are 0644, and errors are dropped. |
| `memory` | Keep as long-term memory, and add provenance. |
| `session`, `config`, `auth`, `voice`, `dashboard`, `guards` | Keep. |
| `agents/coo`, `cto`, `design` | Dormant: `dormant: true` in `role.yaml`, and the loader skips them. |
| `agents/ceo` persona files | Move to `docs/archive/personas/ceo/`. `twins/ceo/role.md` replaces them. |
| `orchestrate`, `experience`, `build-site`, `/consult`, `/switch`, `/agents` | Retire. The code stays in git history. |

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
3. **A3: Google Workspace (Calendar, Gmail, Drive/Docs/Sheets), read-only.**
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
