# Slice V: voice everywhere, foundational workspace UI, notifications

Status: **V-schema done (2026-09-25) and V-voice done and verified (2026-09-25).** V-ui is built too (§5a–§5c). V-notify remains. **The owner's revised brief (2026-09-25) has a delta plan in §7, waiting for approval.** Built from two parallel read-only Opus planning passes (voice/Swift; UI+schema+notifications), synthesized here. Follows `docs/WORKFLOW.md`'s Explore → Plan → Approve → Implement loop. See `docs/EVOLUTION_PLAN.md`'s dated log entries for the full build detail of each piece.

## 0. Corrections to the original brief — read this first

Both planning passes found the brief assumed things that turned out not to be true. Nothing below is a criticism of the brief; it's exactly what Phase 1 (Explore) exists to catch before any code is written.

1. **"kokoro-mlx" does not exist as a Swift package.** It's a Python pip package (`gabrimatic/kokoro-mlx`). Swift MLX ports of Kokoro exist (e.g. `Blaizzy/mlx-audio-swift`), but MLX Swift needs Xcode to compile its Metal shaders — this machine has Command Line Tools only, no Xcode, and this project deliberately builds without Xcode. **Recommended substitute: Kokoro-82M via FluidAudio's own `KokoroAneManager`** (Core ML on the Neural Engine, same model weights, Apache-2.0) — one dependency covers both STT and TTS instead of two. This needs your explicit sign-off (see §1).
2. **Threads are not "store-backed as already designed."** A repo-wide search found no threads table, no transcript persistence, nothing. This is the single largest new concept in the slice, not a wire-up of existing design. Flagged prominently rather than quietly built on a false premise.
3. **Hold-to-talk from any app already exists and is correct** — built earlier this session. Voice is already a channel through the one `nervous.Handle` pipeline. Section 1 is narrower than it might read: swap the STT/TTS *engines* only, not rebuild the hotkey or session state machine.
4. **The `links` table (built this session for the people roster) is confirmed reusable with zero schema change** for decision/meeting/thread/workspace/person edges — just new `Kind` constants and `from_type`/`to_type` string values.
5. **A decision card has no approve/edit/reject path today.** Cards are rebuilt fresh on every request, never persisted; only "email this card" exists as an action. The web UI's Decisions section needs new endpoints, not just a new frontend on existing ones.
6. **Meeting recap code exists but has never been called in production.** This slice would be what finally wires it — a real, deliberate scope addition, not free.
7. **`docs/CONTEXT.md` has no "context accumulation" item to remove.** Grepped; nothing there. Section 0 of the original brief has nothing to do — noted, not actioned further.

## 1. The one blocking gate before anything else: a new third-party dependency

The Swift client currently has **zero** third-party package dependencies (`Package.swift`'s own comment: "No third-party packages — Apple frameworks only"). FluidAudio would be the first. Per `CLAUDE.md`: *"the approved new ones are `modernc.org/sqlite` and `github.com/modelcontextprotocol/go-sdk`. Ask before adding anything else."* That rule is Go-focused by its wording but the spirit — no new dependency without asking — applies here too, and this is a bigger addition than either approved Go dependency: it includes a prebuilt binary xcframework fetched from GitHub releases at build time, plus a resource bundle.

**What you're approving, concretely, if you say yes:**
- `FluidAudio` (github.com/FluidInference/FluidAudio), Apache-2.0, pinned to an exact version (`0.17.4`).
- Model weights downloaded on first use, with explicit consent and visible progress, to `~/Library/Application Support/Water/Models/` (Parakeet CC-BY-4.0, Kokoro Apache-2.0) — except Kokoro's G2P assets, which are hard-coded by the library to `~/.cache/fluidaudio/` regardless of directory override (a real, disclosed limitation, not a bug we'd be introducing).
- No paid API, no key, no telemetry documented, no network access at inference time (only at the one consented download).
- Real trade-off: the library ships frequent releases (5 releases in 3 days during this research pass, one past public-API rename) — mitigated by pinning to an exact version and keeping our own code behind a thin adapter layer.

**If you'd rather not add this dependency at all**, the fallback is staying on the on-device macOS Speech framework (already built) for STT/TTS indefinitely, and this whole voice section of the slice doesn't happen. Worth deciding explicitly either way.

## 2. Scope reality check — this is not one slice by this project's own usual size

Every prior slice this session (roster, the real connectors, company_finance, Tier 1's retirement) was one bounded piece of work. This combined plan is ~30 small tasks across two genuinely different subsystems (a new Swift ML dependency plus voice pipeline rework; a new Go schema + ~10 new HTTP endpoints + an embedded web UI + native notifications). Recommend treating this as **several sequential slices**, not one:
- **V-voice**: the FluidAudio/Kokoro swap (§4 below), independently shippable, doesn't block or get blocked by the UI work except at one shared file (see §3.3).
- **V-schema**: the links extension, workspace/thread/notification store types, the `needsyou` threshold service — a foundation with no UI yet.
- **V-ui**: the embedded web UI itself, once V-schema exists.
- **V-notify**: native notifications, once V-schema and V-ui exist (tapping a notification opens the UI).

This is a recommendation, not a decision made on your behalf — say if you'd rather keep it as one continuous push.

## 3. Coordination points between the two workstreams

1. **Only one database migration is actually needed** (`0013_workspace_ui.sql`, from the UI/schema plan — workspaces, threads, thread_messages, card_states, notifications tables). The voice plan needs no DB migration; its one optional Go-side change is a new `kokoro_voice` field in the existing `style.yaml` schema (`internal/nervous/render/style.go`), unrelated to the migration. No actual collision, despite both reports flagging migration numbering as a risk to watch.
2. **Shared Swift territory**: the UI plan's mic button (in the embedded web view) drives the same `VoiceController`/`SpeechCapture` abstraction the voice plan is simultaneously refactoring. Recommend building voice's `SpeechOutput`/engine-selection foundation first, then wiring the UI's mic button against the finished abstraction — not both editing `AppDelegate.swift`'s voice plumbing at once.
3. **`GET /v1/voice/profile`** gains an optional `kokoro_voice` field (voice plan) — the UI plan doesn't touch this endpoint, no conflict.

## 4. Voice pipeline — design summary (full detail: the planning agent's report, this session's transcript)

- New `SpeechOutput` protocol + `SentenceSpeechQueue` (ordered, barge-in-aware, overlapped synthesis) in `WaterClientCore`; existing AVSpeech code becomes one conformer (`AppleSpeechOutput`), Kokoro becomes a second (`KokoroSpeaker`).
- New `ParakeetCapture: SpeechCapture` conformer alongside the existing `OnDeviceSpeechCapture` — `VoiceSession`'s state machine is untouched.
- Pure engine-selection function: Apple Silicon + models ready → FluidAudio; Intel/Rosetta or models missing → Apple Speech, with a one-time notice (matching the existing "explain once" UI pattern already in `AppDelegate.swift`).
- Model downloads: explicit consent dialog showing size before any byte fetches, visible progress (download → compile → ready), `ModelHub.offlineMode = true` at all other times.
- Latency instrumentation: a trace logged per voice turn (key-up → STT final → request sent → ack → first sentence → first audio → done), plus a `--voice-bench` mode for repeatable N≥30 benchmark runs without a human.
- 15 open risks recorded with proposed defaults in the full report (memory footprint ~1.5GB+ for Kokoro, transcript-format differences potentially affecting Tier 0 hit rate — proposed to bench ~30 eval phrases through both engines and treat a regression as blocking, Intel path unverifiable on this Apple Silicon machine, etc.).

## 5. Workspace UI, schema, notifications — design summary (full detail: the planning agent's report, this session's transcript)

- **Transport**: a Swift `WKURLSchemeHandler` for a custom `water://` scheme, proxying into the existing Unix socket — never a TCP port, so the token never has to leave native code and a strict path allowlist blocks the UI from ever reaching e.g. `/v1/tools/invoke` or twinlink. A TCP-port alternative was explicitly considered and rejected as a real attack-surface increase.
- **Security**: decision evidence and email bodies are attacker-controlled content rendered inside this UI — the plan requires `textContent`-only rendering (never `innerHTML`), a strict CSP header, and a Go test that fails the build if any shipped static file contains `innerHTML`/`eval`/etc.
- **New store types**: `Workspace` (distinct from the roster's `Project` — reasoned as a UI-level container that can span projects, not roster seed data), `Thread`/`ThreadMessage`, `Notification`, `CardState` (for dismiss/reject, since cards aren't persisted today).
- **New "needs you" threshold**, shared by Today and Notifications (doesn't exist today as a single number): decisions cross at severity ≥2, approvals cross when pending past a grace period, each record notifies at most once ever (enforced by a DB unique constraint, survives restarts).
- **~10 new/extended HTTP endpoints**: `/v1/today`, approvals list filters + edit, decision stage/dismiss, meetings list (+ finally wiring recap-on-stop), threads CRUD + anchor (get-or-create), notifications list + mark-delivered.
- **Card "approve"** becomes stage-then-confirm through the *existing* approval-envelope machinery — nothing new executes without an approved envelope; this preserves the project's core outward-action invariant rather than adding a side door.

## 5a. V-ui backend endpoints (built 2026-09-25, not yet committed)

Every route below is behind the daemon's client-token `auth`, like every other client route. The Swift `water://` scheme handler's path allowlist should contain exactly these plus the existing ones the UI reads (`GET /v1/today`, `GET /v1/decisions`, `GET /v1/approvals/{id}`, `POST /v1/approvals/{id}/decision`, `POST /v1/tasks/{id}/cancel`, `GET /v1/voice/profile`). It must never include `/v1/tools/invoke`, `/v1/quick/invoke` or any `/v1/twinlink/*` route.

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/v1/approvals?status=&kind=&limit=` | status `pending` (default) \| `decided` \| `all` \| `approved` \| `denied` \| `expired` \| `executed`; kind `connector` or `connector.function`; limit 1–500 (default 50). With no params it returns the same pending list as before. | `[ApprovalView]`. Pending is oldest first; every other status is newest first. |
| POST | `/v1/approvals/{id}/edit` | `{payload_hash, payload}` | `{voided: ApprovalView, envelope: ApprovalView}`. This calls `Queue.Edit`: the old envelope is voided and a new pending one is proposed. 409 on a stale hash or an envelope that can't be edited; 400 on a bad payload. |
| POST | `/v1/decisions/{id}/stage` | `{function?, payload}` | `{status: "queued"\|"already_staged", card_id, approval_id, envelope: ApprovalView}`. Nothing executes. The CEO confirms through the existing `/v1/approvals/{id}/decision`. 404 if the card isn't open, 409 if it was dismissed, 422 if it has no stageable action, 400 on a bad payload. |
| POST | `/v1/decisions/{id}/dismiss` | `{reason?}` | `{card_id, status: "dismissed", reason, decided_at}`. Dismissed cards drop out of `GET /v1/decisions` and the "needs you" list. |
| GET | `/v1/threads` | – | `[thread]`, without `anchor_context`, newest activity first |
| POST | `/v1/threads` | `{title?}` | 201 `thread` (free-standing) |
| POST | `/v1/threads/anchor` | `{anchor_type: decision\|approval\|message\|meeting, anchor_id, title?}` | `{thread, created}` (get-or-create; 404 if the anchored record doesn't exist) |
| GET | `/v1/threads/{id}` | – | `{thread, messages: [message]}` |
| POST | `/v1/threads/{id}/messages` | `{text, channel?, turn_id?}` | the same NDJSON event stream as `POST /v1/turns`, with the `X-Water-Task-Id` and `X-Water-Thread-Id` headers |
| GET | `/v1/meetings?limit=` | limit 1–200 (default 20) | `[meeting]`, newest first |
| GET | `/v1/meetings/{id}` | – | `meeting` |
| POST | `/v1/meetings/{id}/stop` (existing) | – | Now also carries `recap`: `running` \| `skipped` \| `none` |

The shapes are:
- **`thread`**: `{id, title, anchor_type, anchor_id, anchor_context?, anchor_untrusted, created_at, updated_at}`
- **`message`**: `{id, thread_id, role: ceo|twin, channel, task_id, text, created_at}`
- **`meeting`**: `{session_id, started_at, ended_at, live, event_id, event_title, recap: none|running|ready|skipped|failed, recap_text?, recap_error?, untrusted: true}`

`anchor_context`, `recap_text`, card evidence and envelope payloads may all hold attacker-controlled text. Render them with `textContent` only.

## 5b. V-ui web UI (built 2026-09-25, not yet committed)

- **Package** `internal/webui`: `static/{index.html, app.css, dom.js, api.js, app.js}`. Plain HTML/CSS/vanilla JS, no framework, no build step, no external URL. Embedded with `//go:embed static`.
- **Route** `GET /ui/` (`gateway.UIPrefix`), behind `d.auth` like every route. `/ui/` serves `index.html`. Only `.html`, `.css` and `.js` are served, and there is no directory listing. The security headers are set *before* auth, so every response under the prefix carries them, 401s and 404s included.
- **Headers**: `Content-Security-Policy: webui.CSP`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, COOP/CORP `same-origin`, and `Cache-Control: no-cache`. `index.html` repeats the CSP in a `<meta>` tag, minus `frame-ancestors`.
- **CSP**: `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'self'; media-src 'none'; object-src 'none'; frame-src 'none'; worker-src 'none'; manifest-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; require-trusted-types-for 'script'; trusted-types 'none'`
- **Rendering**: `dom.js`'s `h()` is the only DOM builder.
  - Strings become text nodes.
  - Attributes go through an allowlist of inert names.
  - Events use `addEventListener`.
  - `webui_test.go` fails the build on `innerHTML`, `outerHTML`, `insertAdjacentHTML`, `document.write`, `eval`, `Function(`, string `setTimeout`/`setInterval`, `javascript:`/`data:` URLs, external URLs, URL-bearing property writes, unsafe `setAttribute`, CSS `url()`/`@import`, and inline script, style or handlers in HTML.
- **API calls** are all root-relative, and they all live in `api.js`, which a gateway test pins to the §5a allowlist. The UI never calls `/v1/turns`, `/v1/tools`, `/v1/quick`, `/v1/twinlink`, `/v1/intents`, approval edit or decision email.
- **For the Swift stage**:
  - The `water://` handler's allowlist needs `GET /ui/*` plus the §5a routes.
  - It must forward the response headers, CSP included.
  - It must support streamed bodies for `POST /v1/threads/{id}/messages`.
  - The mic button appears only when `window.webkit.messageHandlers.water` exists, and it posts `{type: "mic"}`.
  - The page exposes `window.water.open(view, id)` and `window.water.refresh()` for native calls (for example, a tapped notification in V-notify).
  - Check on device that WebKit matches `'self'` for the custom `water://app` origin.

## 5c. V-ui Swift workspace window (built 2026-09-25, not yet committed)

- **Allowlist** `WaterClientCore/WorkspaceAllowlist.swift` (pure, tested in `WorkspaceAllowlistTests`): only `water://app` (no user, password or port); GET or POST only; any `%` in the path is refused (daemon ids are all `[A-Za-z0-9_-]`); dot, empty and non-ASCII segments are refused; `GET /ui/` and flat `/ui/<name>.{html,css,js}` plus exactly the fifteen §5b routes. Queries only on `/v1/approvals` (`status`, `kind`, `limit`) and `/v1/meetings` (`limit`), each key once, values from `[A-Za-z0-9._-]`. The forwarded path is rebuilt from the matched pieces. `GET /v1/voice/profile` and `POST /v1/approvals/{id}/edit` are *not* allowed until the UI calls them.
- **Scheme handler** `Sources/Water/Workspace.swift` (`WorkspaceSchemeHandler`): denied requests get a local 403 without touching the socket. Allowed ones go over a dedicated `UnixSocketClient` with the bearer token added natively; response status and a fixed header allowlist (content type, CSP and the other security headers, `X-Water-Task-Id`/`X-Water-Thread-Id`) are passed back, and body bytes go to `didReceive` as each chunk is read. `stop` cancels the socket (the daemon sees the disconnect). Request bodies are capped at 1 MiB.
- **Window** `WorkspaceWindowController`: NSWindow + WKWebView with a non-persistent data store, loading `water://app/ui/`. Navigation outside `water://app` is cancelled (never opened), there are no JS-opened windows and no file panel. Opened from the menu's "Open Workspace" or **⌃⌥W**.
- **Mic**: the `water` script message handler accepts exactly `{type: "mic"}` from the main frame on `water://app` and toggles push-to-talk (the menu's "Talk to Water" path); anything else is ignored.
- **Verified on device** with a throwaway harness (the real handler and the real static files, served by a mock daemon on a Unix socket): WebKit matches `'self'` for `water://app` (scripts, styles and fetch all load under the CSP); NDJSON deltas reach the page incrementally; aborting a fetch disconnects the socket; denied routes return 403 and never reach the daemon; `Set-Cookie`/unknown headers are dropped; external and `file:` navigations are cancelled; `window.open` returns null.

## 6. What happens next

Nothing, until you've reviewed this. Once you validate (as a whole, or per-workstream, or with changes), tell me which pieces to execute and in what order/grouping, and whether to keep the recommended multi-slice split from §2 or push through as one continuous effort. The FluidAudio dependency approval (§1) is the one decision that blocks voice work from starting at all; the rest of the plan can be revised without re-running the research.

## 7. Revised brief (2026-09-25): delta plan

The owner pasted a revised Slice V brief. It adds an Activity HUD with a blob, Drafts, a bottom input bar, a links requirement and a context-accumulation correction. This section is the delta against what §4–§5c already built. It follows `docs/WORKFLOW.md`'s Plan phase and ends by stopping for approval. Every claim below was checked against the code on 2026-09-25.

Decided already, not re-opened: **kokoro-mlx is replaced by Kokoro-82M through FluidAudio's `KokoroAneManager`** (§0.1, §1). FluidAudio is the Swift client's one dependency. kokoro-mlx is a Python package, and MLX Swift needs Xcode, which this build avoids on purpose. V-voice already built it.

### 7.1 Corrections to the revised brief

1. **`job_steps`, `job_notes` and "promoted" notes do not exist.** There is no table, type or endpoint. "job" appears only in comments (`internal/store/links.go`'s node-type list, `0013_workspace_ui.sql`'s header). J's plan (`docs/slices/J.md` §"What exists today", open question 5) found the same thing and deliberately does not reuse `"job"`.
2. **There is no daemon-wide event stream: no SSE and no `/v1/events`.** The only push is each `POST /v1/turns` NDJSON response (`gateway/handlers.go` `streamTurn`, also used by `POST /v1/threads/{id}/messages`). The event kinds are `ack, delta, sentence, approval_required, queued, done, error` (`runtime/runtime.go:50-60`).
3. **Nothing emits a live event when a tool is called.** `backend/stream.go:125` keeps only `text_delta` blocks, so `tool_use` blocks never reach a client. The model's tool calls arrive over HTTP at `POST /v1/tools/invoke` (`daemon.go` `handleToolInvoke`) and `POST /v1/quick/invoke` (`quick.go`). Both only call `Nervous.RecordToolUse`, which is written to `route_log` after the turn. `approval_required` does reach the active turn's stream live, through `d.sinks[d.activeTask]` (`daemon.go` `notifyApprovalRequired`).
4. **`approval_required` from the Tier-0 path is bare.** `nervous.go:609` (a write intent's envelope) and `actions.go:271` (a spoken yes on a tap-required envelope) send only `approval_id`, with no `read_back`, `risk`, `payload_hash` or `action`. Only the model-queued path (`daemon.go:250`) fills them in. The Swift `TurnEvent` (`WaterClientCore/Events.swift`) doesn't decode `read_back` at all, and it decodes `queued` as `.unknown`.
5. **`style.yaml` exists, but the main path never sees it.** `runtime.Env.StyleBlock` (`runtime.go:137`) is never assigned. `gateway.baseEnv` (`daemon.go:771`) and the prewarm request (`cli/cmd_daemon.go:228`) both leave it empty, so `prompt_block`'s tone rules never reach the model. `max_chars.voice: 280` caps only Tier-0 answers (`answerQuick` → `speak.Speakable`). The main path only *lints* voice length (`mainpath.go:145`) and never truncates. `TurnPrompt` (`runtime.go:313`) doesn't tell the model which channel it's on, so it can't know to answer briefly for voice. This is a real existing bug, not a new feature.
6. **"Drafting a reply" is not an outward action here and creates no envelope.** `gmail.draft_message` is level D (`twins/ceo/twin.yaml:31`), and the `mail.draft_reply` intent delivers the draft directly (`nervous/actions.go` `tryWriteIntent`, `!it.RequiresApproval`). The real outward action, `gmail.send_message`, is level A, risk high (`gmail.go:121`), and missing from `voiceEligibleActions` (`nervous/voiceapprove.go`). So a spoken yes never approves a send. It is tap-only by design. Only `gcal.create_event` and `gcal.move_event` (risk medium) can be approved by voice today, and only with no external attendees, `router.voice_approve.enabled` (off by default, `config.go:331`) and `internal_domains` set. The brief's "a spoken yes and clicking Approve resolve it identically" is true in code where a spoken yes is allowed at all: both go through `decideAndExecute` (`gateway/actions.go:96`, `DecideBound` at `:169`). It can't be shown with a drafted reply.
7. **"The same answer" can't be byte-identical across channels, by design.** Rendering depends on the channel (`max_chars` voice 280 against text-bar 600, `max_list_items` 3 against 8, and voice gets `sentence` events through `Speakable`). Main-path answers come from the model and aren't deterministic. What can be proven is **the same pipeline**: the same `route_log` tiers, `answered_by`, intent and `tools_used`, with only `channel` differing.
8. **The brief contradicts itself on reflex-tier approvals.** "Reflex-tier answers (no job, no tool calls) never trigger this window" collides with "the instant an approval envelope … exist[s], expands". A Tier-0 write intent (`calendar.create_event`) stages an envelope with no tool call and no model. This plan lets the approval win: an open envelope always shows the HUD.
9. **Approval statuses are `pending|approved|denied|expired|executed` only.** `staged`, `sent`, `awaiting_reply` and `completed` don't exist. `awaiting_reply` is reply-ingestion territory (J/N). The brief allows the fallback: "otherwise just pending/decided".
10. **The links requirement is already met at the schema level.** `links` is generic typed edges. V-schema added `about`, `in_workspace`, `involves` and `for_project`, so no future zoom or template needs a migration. At the data level, though, only anchored threads write a link today (`workspace_threads.go:343`, `LinkAbout`). No `workspaces` rows exist, and nothing creates one.
11. **§0 of the brief has nothing left to finish.** R is done (tag `slice-R-done`), and `docs/CONTEXT.md` has no context-accumulation item to remove (§0.7). Only the one line needs adding.
12. **Two real gaps in the "done" voice work (§1).** (a) Models don't go to `~/Library/Application Support/Water/Models/`. No code passes a `directory:` to `KokoroAneManager()` or `SlidingWindowAsrManager()` (`EngineSelector.swift:31`, `KokoroSpeaker.swift:81`, `ParakeetCapture.swift:71`), so FluidAudio's defaults apply: `~/Library/Application Support/FluidAudio/Models/`, plus the G2P assets in `~/.cache/fluidaudio/`. (b) The consent and progress window covers only Kokoro (`EngineSelector` → `kokoroManager.initialize()`). Parakeet's models download lazily on the first hold, with no progress shown (`ParakeetCapture.InitGate` → `loadModels()`). (c) On Intel, Water falls back to Apple Speech *silently*: `EngineSelector.resolve`'s `guard isAppleSilicon` returns with no notice, and the brief asks for one notice, shown once.
13. **The web UI differs from the brief's layout.** It has a sidebar and a two-column Today (`app.css:153` `.grid-2`), not one column. There is no Drafts view. There is no bottom bar; the composer exists only inside an open thread. The mic toggles rather than holds, and its reply goes to the text bar, not the thread (known-gaps "Slice V-ui: open gaps").

Nothing else in the brief failed to parse beyond the coordinator's listed reconstructions. One phrase reads oddly: "an edit to either voids it" is read here as "an edit made through either path voids the envelope", which is already what `Queue.Edit` does (hash-bound).

### 7.2 Mapping: brief → current state

| Brief item | State | Where / what's missing |
|---|---|---|
| §0 correction + CONTEXT.md line | Not done (docs only) | Add one line; see V-0 |
| §1 FluidAudio STT (Parakeet, ANE) | Done | `ParakeetCapture.swift` (streaming via `SlidingWindowAsrManager`) |
| §1 TTS Kokoro | Done (as decided) | `KokoroSpeaker.swift`, `SentenceSpeechQueue` |
| §1 Swift package deps | Done | `Package.swift`, FluidAudio 0.17.4 pinned |
| §1 hold-to-talk, any app | Done | `HotKeys.swift` ⌃⌥V, `HoldToTalk.swift` `VoiceSession` |
| §1 reply uses style.yaml tone/length | **Partly** | Tier 0 yes. Main path no: the StyleBlock bug and no channel hint (§7.1.5) |
| §1 same `nervous.Handle` pipeline | Done | `handlers.go` `streamTurn` → `Nervous.Handle` for every channel |
| §1 models in `…/Water/Models/`, visible progress | **Partly** | Wrong dir, and Parakeet has no progress (§7.1.12) |
| §1 Intel fallback, said once | **Partly** | Fallback works; the notice is missing |
| §1a live steps | Not done | No tool events (§7.1.3) |
| §1a promoted job notes | Not done, premise missing | §7.1.1, decision D1 |
| §1a inline approval (read-back, Approve/Edit/Reject) | Not done (native) | Events are bare on the T0 path (§7.1.4). `AskPanel.appendApproval` shows only the action and risk |
| §1a lifecycle / reflex never triggers | Not done | V-hud |
| §1a one reusable view, blob states | Not done | V-hud |
| §2 Today | Done | `today.go`, `app.js` `viewToday` (two-column; see V-ui2) |
| §2 Decisions (evidence, options, approve/edit/reject) | **Partly** | Stage and dismiss exist. There is no "staged" indicator and no post-stage edit (D3) |
| §2 Approvals with status | Done (fallback) | `?status=` filter; §7.1.9 |
| §2 Meetings | Done | `workspace_meetings.go`, recap-on-stop |
| §2 Drafts | Not done | D2 |
| §2 Threads sidebar | **Partly** | Threads are a section in the main pane, not a sidebar list |
| §2 bottom bar + mic push-to-talk | Not done | V-ui2 |
| §2 links on every record | **Partly** | Schema yes; data only for thread→anchor (D6) |
| §2 one column, system font, no theming | **Partly** | System font, no theming; Today is two-column |
| §3 fires on the needs-you threshold | **Partly** | `needsyou.Service.Tick` writes `notifications` rows (at most once ever). No endpoint serves them and no client fires them |
| §3 native notification | Not done | V-notify |
| §3 tap → anchored thread (get-or-create) | **Partly** | `POST /v1/threads/anchor` and `window.water.open` exist. No native tap handling yet |
| §4 out of scope | Respected | — |

### 7.3 Owner decisions needed

**D1. The HUD's live steps: new tool events on the turn stream, or a jobs schema now.**
- *Option A (recommended): turn-stream events.* Add `tool_start`/`tool_end` to `runtime.Event`, emitted from `handleToolInvoke`/`handleQuickInvoke` through `d.sinks[d.activeTask]`, the same routing `notifyApprovalRequired` already uses. There is no migration, no new state and no new endpoint, and old clients ignore unknown kinds. Trade-off: steps exist only while the turn's stream is open. Close the text bar mid-turn and they're gone, so there is no history of "what ran". Neither is there today.
- *Option B: `jobs`/`job_steps`/`job_notes` tables now.* The HUD could reattach after a reconnect and show history. But a durable job with steps *is* J's task record (`task_records`/`task_events`, J §6). Building it here means either a second, competing durable-work table that J must later migrate or delete, or pre-empting J's open naming question (J Q5). It also needs a subscription endpoint, and that is the SSE stream §7.1.2 says doesn't exist. That's several times the work.
- *Promoted job_notes:* there is nothing to map them to today. The model's prose already streams as `delta`. Recommend **dropping them from V** and noting that J's `task_events` trail is where "notes worth showing" belong. The HUD view takes a plain `[Step]` array, so it can later render a J task record's events with no view change.

**D2. Drafts.** No draft state exists (§7.1.6).
- *Option A (recommended):* the Drafts view lists **pending envelopes whose action is an outward message** (`gmail.send_message`, `twinlink.send_message`), with the same read-back and approve/edit/reject as Approvals. No new state, and it uses the existing `?kind=` filter (two calls, merged client-side). Gmail drafts made at level D stay in Gmail and are not listed. The view says so in one line.
- *Option B:* raise `gmail.draft_message` to level A, so every draft goes through an envelope and shows in Drafts. That makes "drafting a reply" a real approval (and a voice-approvable one, since it's in `voiceEligibleActions`, for internal recipients). But it reverses R-20's deliberate choice (docs/slices/R.md risk 24) and makes every dictated draft wait for a yes.
- *Option C:* a `drafts` table. That's the drafter's job (L), on J's record. Defer.

**D3. Approve/edit/reject on decision cards.** Recommended mapping, which keeps "no side door past envelopes":
- **Approve** = stage (`POST /v1/decisions/{id}/stage`), then show that envelope's code-built read-back inline, then confirm (`POST /v1/approvals/{id}/decision`). There are always two deliberate clicks, never a one-request stage-and-execute.
- **Edit** = the parameter form before staging (exists), plus approval edit after staging (`POST /v1/approvals/{id}/edit`, which exists but isn't allowlisted). V-ui2 also fixes the known gap where `card_states.approval_id` keeps pointing at the voided envelope.
- **Reject** = dismiss with an optional reason (exists).
- *Alternative:* a single-click "approve" that stages and decides in one request. Not recommended: it collapses the read-back step the invariant relies on.

**D4. `router.voice_approve.enabled`.** Acceptance §5 needs a spoken yes to work.
- *Option A (recommended):* keep the **code default `false`**. The owner turns it on in their own `config.yaml` and sets `router.voice_approve.internal_domains` to the company domain. Verification runs in an isolated `WATER_HOME` with it on.
- *Option B:* flip the default to true. With `internal_domains` empty, every addressed envelope is still tap-only, so the practical gain is small and the posture change is real.
- Either way, the §5 demo action must be **`gcal.create_event` with internal or no attendees** (§7.1.6), unless D2 goes with B.

**D5. Fix the StyleBlock bug in this slice?** Recommend **yes, in V-events**. It's small: set `Env.StyleBlock = style.PromptBlock()` in `baseEnv` **and** in the prewarm request. Both are needed, because `WarmSession` restarts when `System` differs (`warmsession.go:440`); fixing one site alone would restart the warm process on every first turn. Also add a one-line per-turn channel hint to `TurnPrompt`, e.g. `## Channel: voice (reply in at most ~280 characters, no lists or markdown)`, taken from `style.MaxChars`. It lives in the user message, so the cached system prompt stays byte-identical. *Sub-choice:* also hard-truncate main-path voice replies at `max_chars.voice`? Recommend **no**: cutting a model answer mid-thought is worse than a long one, and `Lint` already flags overlength in `route_log.warnings`.

**D6. The links data model.** Schema: nothing to do (§7.1.10). Data, recommended minimum:
- write only links that are **deterministic from code, never guessed by a model**:
  - meeting → `involves` → person, from the calendar event's attendees through `roster.PersonByIdentity`, at meeting start;
  - decision → `involves` → person, from the card's source message sender, when a card crosses needs-you or is staged, dismissed or anchored (cards aren't persisted; `card_id` is deterministic, `decisions/build.go:103`);
  - thread → copies its anchor's `involves`/`for_project` links at anchor time.
- write no `in_workspace` links until something creates workspaces (none exist).
- **"job" = J's task record, decided in J** (J Q5). V writes no job links.
- *Alternative:* write no links now, and backfill later (links are derivable from source records, so it is still no migration). Cheaper, but it doesn't meet the brief's "must already carry" wording.

**D7. When the blob is visible.** The brief lists an Idle state but doesn't say whether the blob is always on screen. Recommend **showing it only during an interaction**: from hotkey-down or text submit until the reply ends plus the auto-dismiss delay, and while an approval is pinned. Idle is the resting look inside that window. The alternative is an always-on floating blob, which sits over every app all day.

Design defaults, also open to override (not blocking): the HUD expands on the **first `tool_start` of any tool, `quick.*` included, or any `approval_required`** (§7.1.8), and auto-dismisses 4s after `done` with nothing pending. The HUD's Edit opens the workspace window at `approvals/{id}`, so there is one edit UI, not two. Step labels are code-built from the function spec (a new optional `Activity` string on `connectors.FunctionSpec`, falling back to `Description`) and never include model-composed arguments. Notifications poll every 30s with at most 3 banners per poll, and a single "and N more" banner for any remainder.

### 7.4 Proposed sub-slices, in build order

Order: **V-0 → V-events → V-notify → V-hud → V-ui2 → V-links → V-verify.** V-notify depends only on V-ui (done) and could run before V-events. The order above keeps one slice per session and puts the Go event contract first, since V-hud and V-ui2 both consume it. V-hud and V-ui2 both edit `AppDelegate.swift`'s voice plumbing, so they run one after the other, never in parallel (the same reasoning as §3.2).

#### V-0: docs correction (first, per the brief's §0)
- Add a dated amendment to `docs/CONTEXT.md`: "Context is fetched on demand through tool calls, not accumulated ahead of time." It goes in a new "Amendment (2026-09-25, Slice V revised brief)" section, not as an edit to principle 4. The background plane that precomputes the brief and decisions is a different thing and stays.
- Acceptance: the line is present; no code changes.

#### V-events: tool events, complete approval events, the StyleBlock fix (Go + Swift core)
Scope:
- `runtime/runtime.go`: add `EventToolStart = "tool_start"` and `EventToolEnd = "tool_end"`. Add `Event` fields `StepID` (`step_id`), `Tool` (`tool`), `Label` (`label`) and `Status` (`status`: `ok|queued|denied|error`).
- `gateway/daemon.go`: add `notifyStep(e runtime.Event)`, routed exactly like `notifyApprovalRequired`: `d.sinks[d.activeTask]`, nothing emitted if there's no active turn. `handleToolInvoke` emits `tool_start` before the envelope check or `Gate.Invoke`, and `tool_end` on every return path (status `queued` when an envelope was proposed, `denied`, `ok`, or `error` for `executed_with_error`). Emit on every path with a `defer`, so a failed call can't leave a step "active". `handleQuickInvoke` does the same. `StepID` is `newID("stp")`.
- `connectors/connector.go`: an optional `Activity string` on the function spec (e.g. gsheets `budget_status` → "Reading the budget sheet"). The label is computed in `gateway` from the spec (`Activity`, then `Description`, then the function id). Quick tools get labels from their own table. There are **no arguments in labels**.
- Complete bare `approval_required` events: `nervous.go:609` and `actions.go:271` look up the envelope (`env.Approvals.Get`) and fill `Action`, `Risk`, `PayloadHash` and `ReadBack` (`approvals.ReadBack`). A lookup failure still sends the bare event, as today.
- StyleBlock (D5): add `StyleBlock string` to `gateway.Config`, filled from `deps.style.PromptBlock()` in `cmd_daemon.go`. Set it in `baseEnv`, and in `daemonPrewarmer` by passing the same string. `TurnPrompt` gains the channel hint. Its signature takes a channel, and the call sites get updated.
- Swift `WaterClientCore/Events.swift`: add `.queued`, `.toolStart` and `.toolEnd` kinds, and fields `readBack`, `stepID`, `tool`, `label` and `status`.
- Web `app.js`: ignore the new kinds for now. Its `switch` already falls through.
- `apicompat_test.go`: extend it, so old clients still parse.
Tests (written first):
- the tool-invoke handler emits start and end on the active sink for each status, and nothing when no turn is active or the stream is closed;
- a `quick.*` call emits a pair;
- the T0 write-intent `approval_required` carries a `read_back` equal to `approvals.ReadBack(env)`;
- `RoleSystem(baseEnv) == prewarm System` (a regression test for the warm restart);
- the prompt contains the style block;
- `TurnPrompt` has the voice hint only on voice;
- Swift decoding tests for each new field and kind.
Acceptance:
- a main-path turn that calls one tool produces exactly one `tool_start`/`tool_end` pair, in order, before `done`;
- a Tier-0 turn produces none;
- every `approval_required` on every path carries `read_back` and `payload_hash`;
- the system prompt sent to `claude` contains `prompt_block`.
Invariants: no new route, and the tool-invoke semantics are unchanged (events are side-effect-free writes to an already-open stream).

#### V-notify: native notifications (§3)
Scope:
- Go (new file `gateway/notifications.go`): `GET /v1/notifications?undelivered=1&limit=` (→ `ListUndeliveredNotifications`) and `POST /v1/notifications/{id}/delivered` (→ `MarkNotificationDelivered`), behind `d.auth`. These routes are for **native only**: *not* added to `api.js` or `WorkspaceAllowlist.routes`, since the page never needs them. The Go allowlist-pin test gets a negative assertion that the page can't reach them.
- Swift core (new `WaterClientCore/Notifications.swift`): a pure `NotificationPlanner` (dedupe by id, the 3-per-poll cap plus "and N more", and mapping `record_type`/`record_id` to an anchor request, accepting only `decision|approval` types and ids matching `[A-Za-z0-9_-]+`).
- Swift app (new `Sources/Water/Notifier.swift`): a 30s poll on its own `UnixSocketClient`, and `UNUserNotificationCenter` (authorization asked on the first notification, not at launch). `userInfo` carries only the record type and id. After a successful `add`, mark the notification delivered. On tap (`didReceive`): `POST /v1/threads/anchor` → `workspace.show()` → `window.water.open('threads', id)` through `callAsyncJavaScript` with **arguments**, never string-built JS. An anchor 404 (for example, the card was dismissed since) opens the Decisions or Approvals view instead.
Tests:
- Go: the list only returns undelivered notifications, delivered is idempotent, 404 on an unknown id, auth required;
- Swift core: planner cap and dedupe, anchor mapping, invalid ids rejected.
Acceptance:
- a fixture decision at severity ≥ `notify.min_severity` produces exactly one banner, ever, across a daemon restart;
- tapping it opens a thread whose `anchor_type=decision` and `anchor_id=<card id>`, with the card snapshot shown;
- a second tap reuses the same thread.
Risk:
- `UNUserNotificationCenter` with `build.sh`'s self-signed identity and `LSUIElement` is unverified here. Default: verify first. If it's refused, fall back to `NSUserNotification` (deprecated, still works) and log a known gap.

#### V-hud: the Activity HUD and blob (Swift)
Scope:
- `WaterClientCore/Activity.swift`: a pure `ActivityModel`, the **one state machine**. It has states `idle | listening | thinking | responding | needsYou` and holds `steps: [Step{id,label,status}]`, `approvals: [ApprovalCard{id,action,risk,readBack,payloadHash}]`, `level: Float` and an auto-dismiss deadline. Its inputs are `holdStarted`, `holdEnded`, `turnSent`, `event(TurnEvent)`, `speechStarted`, `speechIdle`, `micLevel`, `ttsLevel`, `approvalResolved(id)` and `tick(now)`. Rules:
  - expand when `steps` or `approvals` is non-empty;
  - an open approval pins it;
  - after `done` with no approvals, collapse at `done + 4s`;
  - a turn with no `tool_*` and no `approval_required` never expands.
- The real signals:
  - **mic amplitude**: `MicTap` gains `onLevel`, the RMS of each tap buffer, throttled to about 30Hz on main. It is shared by `OnDeviceSpeechCapture` and `ParakeetCapture`.
  - **TTS**: `SentenceSpeechQueue` gains `onIdle` (drained) next to `onWillPlay`. `KokoroSpeaker` turns on `AVAudioPlayer.isMeteringEnabled`, and a display-rate timer reads `averagePower`. `AppleSpeechOutput` has no metering, so it uses a gentle fixed wave while speaking, driven by `willSpeakRangeOfSpeechString` ticks (a known limitation, recorded).
  - **Thinking**: from `turnSent` until the first `sentence`/`delta`, a time-driven pulse, not amplitude.
- `Sources/Water/ActivityView.swift`: **one** AppKit `NSView` with `compact` and `expanded` layouts. The blob is a `CAShapeLayer` path whose radius and wobble follow the model's state and level. Expanded, it shows step rows (`NSTextField` labels, plain strings) and the approval card with the exact `readBack`, plus Approve, Edit and Reject. The view knows nothing about windows, so the workspace can later embed it as a "what's running now" strip.
- `Sources/Water/ActivityHUD.swift`: a non-activating floating `NSPanel` (`.canJoinAllSpaces`, `.fullScreenAuxiliary`), placed next to the `AskPanel` frame. Approve and Reject → `POST /v1/approvals/{id}/decision` with the event's `payload_hash`. That click *is* the tap for tap-required envelopes. Edit → the workspace at `approvals/{id}`. After every turn's `done`, and after each click, it re-reads `GET /v1/approvals/{id}` for each open card, so an approval resolved by a spoken yes (a separate voice turn) disappears the same way a clicked one does.
- `AppDelegate.swift`: feed the model from the hotkey callbacks, `voice.onListening`, `TurnRunner` events and the speech queue. `AskPanel.appendApproval` keeps working for the panel's own transcript.
- §1 leftovers, all Swift:
  - the Intel one-time notice (`EngineSelector`, same `UserDefaults` "explain once" pattern);
  - Parakeet included in the consent and progress flow (`AsrModels`/`loadModels()` run inside `showDownloadProgress`);
  - both managers get a `directory:` under `~/Library/Application Support/Water/Models/`. The existing downloads in FluidAudio's default dir get re-downloaded once, with consent, or moved. Default: re-download behind the existing dialog. Kokoro's G2P stays in `~/.cache/fluidaudio/`, already disclosed in §1.
  - `--voice-bench --tts kokoro` to measure Kokoro first-audio.
Tests:
- `ActivityModelTests`: reflex turn never expands; tool turn expands on the first `tool_start`; approval pins; auto-dismiss on a fake clock; a resolved approval unpins and then collapses; listening → thinking → responding → idle driven by inputs; a stale turn's events are ignored;
- RMS helper tests;
- the existing `SentenceSpeechQueueTests` extended for `onIdle`.
Acceptance:
- on device: a tool-calling turn pops the HUD with ≥1 live step before `done`;
- a Tier-0 answer doesn't pop it;
- the blob visibly reacts to voice while held, pulses while thinking and moves while Kokoro speaks;
- an approval stays pinned until it's resolved.

#### V-ui2: web UI deltas
Scope:
- **Drafts** view (D2-A) in `app.js`, reusing the approval detail.
- **Approval edit UI.** Add `POST /v1/approvals/{id}/edit` to `api.js`, the Go allowlist-pin test and `WorkspaceAllowlist.routes` (all three by hand, per known-gaps). The edit handler also updates `card_states.approval_id` from the voided envelope to the new one (new store method `RepointCardState`).
- **Decision card flow** (D3): `GET /v1/decisions` includes each card's `card_state` (`staged` plus `approval_id`), so the UI shows "staged, awaiting your yes" instead of staging again.
- **Bottom bar**, fixed under `main`: typed text posts to the open thread, or creates a free-standing thread first (`POST /v1/threads`, then messages). No separate chat mode.
- **Mic in the bar is hold-to-talk**: pointerdown posts `{type:"mic-down", thread}` and pointerup posts `{type:"mic-up"}`. The native handler accepts only those two shapes, and `thread` must match `^thr_[0-9a-f]+$`.
  - The voice turn goes through `POST /v1/turns` with a new optional `thread_id`. The daemon checks the thread exists and appends both messages, as `handlePostThreadMessage` does, sharing one helper.
  - The native side speaks and drives the HUD, then calls `window.water.refresh()`.
  - `POST /v1/turns` isn't page-reachable, so the page never gains turn access.
  - Known-gaps' "the mic toggles rather than holds" and "its reply goes to the text bar" both close.
- **Layout**: one column (drop `.grid-2` on Today), and a recent-threads list under the sidebar's section nav.
- Approvals show `executed` as "sent" for outward-message actions.
Tests:
- the Go allowlist pin updated;
- the `webui_test.go` sink scan still passes;
- edit re-points the card state;
- `thread_id` on `/v1/turns` rejects an unknown thread;
- Swift `WorkspaceAllowlistTests` for the edit route;
- a message-handler shape test (pure helper in core).
Acceptance:
- a staged card shows its state on reload;
- editing a staged envelope leaves the card pointing at the new one;
- a held-mic question lands in the open thread as a `voice` message and is spoken;
- Drafts lists a pending `gmail.send_message` envelope and approving it there resolves it.

#### V-links: deterministic links (D6)
Scope:
- `internal/meetings` start: attendees → `involves`;
- `needsyou.Service.Tick`, stage and dismiss: decision → `involves` (source message sender via roster);
- `resolveAnchor`: the thread copies its anchor's links.
All writes go through `AddLink`, which is idempotent, and write nothing when the roster can't resolve an identity. **Never a guess.**
Tests: each writer adds the expected edge, writes nothing for an unresolved identity, and re-running adds no duplicate.
Acceptance: after one fixture meeting and one fixture decision, `LinksOf` returns the expected person edges. There is no new migration.

#### V-verify: Phase 4 evidence (`docs/slices/V-verification.md`)
See §7.5.

Invariant checks every sub-slice keeps:
- the gate is still the only execution path, and nothing new executes without an approved envelope (the HUD, Drafts and the bottom bar only call existing decision and edit endpoints);
- untrusted text is rendered as text only: `textContent` in the web UI, plain `NSTextField` strings natively, never attributed HTML;
- the page never gains `/v1/turns`, `/v1/tools`, `/v1/quick`, `/v1/twinlink` or `/v1/notifications`;
- the CSP is unchanged;
- no new dependency;
- zero metered spend;
- `go vet`, `go test -count=1 ./...`, `CGO_ENABLED=0 go build ./cmd/water` and `swift test` all pass before each commit;
- `water audit verify` stays clean.

### 7.5 Acceptance plan (brief §5 → how it's proved)

| §5 criterion | Proof |
|---|---|
| Hotkey from any app → spoken answer, latency measured | ~10 real hold-to-talk turns on device with Parakeet and Kokoro, with another app focused. The per-turn `voice_trace` log lines give min/median/p90 for key-up→STT final→ack→first sentence→first audio. Plus `--voice-bench --n 30` (Apple Speech and Kokoro) for repeatable numbers. The numbers are reported, not asserted against a budget, because the brief sets none. |
| The same question typed gives the same answer through the same pipeline (trace) | Ask one Tier-0 question and one tool-calling question, each spoken and typed. Show the four `route_log` rows side by side: identical `tiers_attempted`, `answered_by`, `intent` and `tools_used`, with only `channel` differing. Reply-text differences are explained by the channel's style caps (§7.1.7). |
| A test decision crossing needs-you → native notification → tap opens a thread scoped to it | A fixture decision in an isolated `WATER_HOME`. Capture the banner (screenshot) and the `notifications.delivered_at` row. After the tap, `GET /v1/threads/{id}` shows `anchor_type=decision` with that card id. |
| A tool-calling voice command pops the HUD with ≥1 live step; a calendar lookup doesn't | "What's blocking the Halcyon rollout" (main path, calls Linear/GitHub or `quick.*` read tools; live read-only or demo connectors) against "what's on my calendar today" (a Tier-0 `schedule.on_date` hit, `ceo_eval.yaml:17`). Screen recording plus the NDJSON captures showing `tool_start`/`tool_end` in the first case and none in the second. |
| An outward-action voice command shows the approval card with the exact read-back; spoken yes and click resolve identically | Per D4, use `gcal.create_event` with internal attendees, voice-approve enabled in the isolated home (or a drafted reply if D2-B is chosen). Stage two identical envelopes: approve one with a spoken "yes" and one by clicking in the HUD. Compare envelope status, audit entries and the executed call (fake connector): identical apart from ids and times. Also show that a spoken yes on `gmail.send_message` is refused as tap-required, and that the HUD click then resolves it. |
| No new dependency needs a paid key or a non-Apple, non-OSS endpoint | `Package.swift`/`go.mod` diffs are empty for the whole revised brief. FluidAudio's only network use is the consented Hugging Face model download (Apache-2.0 and CC-BY-4.0 weights), as §1 already recorded. |
| CONTEXT.md reflects §0 | `grep` for the line (V-0). |

Real accounts stay read-only throughout. Every write demo uses fake connectors or an isolated `WATER_HOME`, never `~/.water`.

### 7.6 Risks and open questions (with proposed defaults)
- **The active-turn attribution of tool events** is only as good as the one-model-slot rule. A tool call landing after its turn's stream closed is silently dropped, just as `approval_required` is today. Default: accept it, and document it.
- **Notification permission** under self-signed codesigning is unverified. Default: verify first; fall back as V-notify says.
- **Moving models to `Water/Models/`** means a one-time re-download of about 1.5GB. Default: do it behind the existing consent dialog. The alternative is to keep FluidAudio's default dir and amend the brief's path.
- **Linear/GitHub read data for "Halcyon"** may not exist live. Default: use the demo connectors' fixtures for the HUD demo, and state which was used.
- **Parakeet and Tier-0 hit rate:** §4's plan was to bench about 30 eval phrases through both STT engines. If that hasn't been run, fold it into V-verify.

### 7.7 What happens next

Stop. Nothing in §7.4 starts until the owner approves this plan, or edits it, and answers D1–D7. V-0 is docs only and can go first on approval. After that, one sub-slice per session, in the order above, each ending with its own `docs/EVOLUTION_PLAN.md` update by the coordinator.
