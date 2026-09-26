# Slice V revised brief: V-verify (automatable evidence)

Date: 2026-09-25 (captures are stamped in UTC, 2026-09-26T05:2x Z). Tree: `feat/ceo-twin` at `b172fc5` plus the uncommitted V-events, V-notify, V-hud, V-ui2 and V-links work, as reviewed (the review changed no code). Spec: `docs/slices/V.md` §7.5.

This covers the items a machine can prove. The device-only items are listed as a checklist in the final section.

## How it was run, and one deviation from "the real daemon binary"

Everything ran in an isolated `WATER_HOME=/tmp/wvv.YhXn/home` with its own socket (`/tmp/wvv.YhXn/home/run/water.sock`). Its `config.yaml` set only these values:

```yaml
backend:  {preferred: claude-subscription}
router:   {voice_approve: {enabled: true, internal_domains: example.com}}
```

`~/.water` and the owner's running daemon were not touched (`~/.water/config.yaml` mtime is still Sep 24), and `build.sh --install` was not run.

**Why this is a harness and not `water daemon`.** On darwin, `runDaemon` always wires `vault.Default()`, which is the login Keychain. Setting `WATER_HOME` does not change that, so an "isolated" `water daemon` still holds the owner's real Google credential. `buildCEORegistry` also always registers the real `gcal` and `gmail` connectors, and `--demo` fakes only GitHub, Linear and HubSpot. Approving a `gcal.create_event` envelope there would have sent real invites. Item 5 had to execute writes, so the daemon was rebuilt as a small harness program at `/tmp/wvv.YhXn/harness/main.go`, deleted afterwards. It sat outside the repo and was a module `water/zzvverify` with `replace water => <repo>`, so it compiled against the current tree's packages.

The harness mirrors `runDaemon`'s wiring:
- the same `gateway.New`, `nervous.New` (Tier 0, the main path, voice-approve), `approvals.Queue`, `gate.New`, hash-chained audit log, `needsyou.Service` tick, `WarmSession` and `ClaudeSubscription` backend;
- the real `twins/ceo` manifest, intents, style, role and roster;
- the prewarmer was not wired.

It differs from `runDaemon` only in these ways:
- **The gate's vault is `vault.NewMemory()`, and it is empty**, so no connector can authenticate to any real account.
- **`gcal`, `gmail` and `gdrive` are recording fakes.** They keep the real connectors' names and `Functions()` (level, risk, schema, `Activity`), but `Invoke` appends `{function, args}` to `$WATER_HOME/executed.jsonl` and returns canned JSON. `github`, `linear` and `hubspot` are the demo fakes. `company_finance` (gsheets) stays real but has no credential.
- **The decision classifier is a fixture** (`backend.Fake`): only the seeded "Speaking invite" message is a decision. The conversation backend is the real `claude` subscription CLI, and the MCP tool server is the real `water mcp-serve` from a binary built from the tree.
- **Fixtures were seeded into the store:**
  - a message from `Dana Reyes <dana@partner-co.example>`, "Speaking invite";
  - a message from `Priya Nair <priya@example.com>`, "Halcyon sync";
  - one event today at 23:00, "Halcyon standup".

Zero metered spend: the environment has no metered key set. The only model calls went to the subscription CLI, and the classifier was a fake.

## (1) `--voice-bench --n 30`

The Swift client was built from the tree with `swift build -c release` (already up to date), and `.build/release/Water --voice-bench` was run with `WATER_HOME` set to the isolated home.

- **Kokoro did not run.** `--tts kokoro` refused: `the speech models aren't in ~/Library/Application Support/Water/Models. Launch Water.app and accept the download first; the bench never downloads.` That folder doesn't exist on this Mac. Only the G2P assets and an older `kokoro-82m-coreml` sit in `~/.cache/fluidaudio/Models/`, which V-hud's `SpeechModels` no longer reads.
- **Apple Speech ran:** 30/30 turns completed, exit 0.

```
request_sent->ack:           n=30 min=0ms median=1ms p90=1ms    max=49ms
ack->first_sentence:         n=30 min=0ms median=0ms p90=1ms    max=5ms
first_sentence->first_audio: n=30 min=0ms median=0ms p90=0ms    max=0ms
first_audio->done:           n=30 min=0ms median=1269ms p90=4299ms max=7310ms
(key_up->stt_final and stt_final->request_sent are 0ms by construction; the bench has no mic)
```

Daemon side, for the same 30 turns (`route_log`):

| canned prompt | tiers | answered_by | n | total ms avg (min–max) | daemon first sentence avg |
|---|---|---|---|---|---|
| what's on my schedule today? | t0 | t0 (`schedule.on_date`) | 5 | 1 (0–5) | n/a |
| any approvals waiting on me? | t0, main | main | 5 | 1122 (975–1373) | 1060 ms |
| what's the status of the current project? | t0, main | main | 5 | 1431 (1153–1960) | 1239 ms |
| who's working on what this week? | t0, main | main | 5 | 1846 (1123–4227) | 1527 ms |
| read me today's top decision | t0, main | main | 5 | 2227 (1166–4945) | 1905 ms |
| summarize my unread email | main | main | 5 | 3522 (1401–7310) | 3125 ms |

**Finding (a measurement gap, not a regression):** on a voice escalation, the daemon's spoken ack is emitted as a `sentence` event (`"One moment."`). The bench's `first_sentence` and `first_audio` marks fire on that filler, so `ack->first_sentence ≈ 0ms` measures the filler, not the answer. The time to the real answer's first sentence is `route_log.first_sentence_ms` (the last column above), about 1.0–3.1 s median on a warm session. The on-device ~10-turn run should read the same column.

## (2) The same pipeline across channels (route_log)

Two questions were each sent through `POST /v1/turns` on channel `voice` and on channel `text-bar`:

| id | channel | utterance | tiers_attempted | owner | answered_by | intent | escalation | outcome | tools_used |
|---|---|---|---|---|---|---|---|---|---|
| 1 | voice | what's on my calendar today | ["t0"] | quick | t0 | schedule.on_date | | answered | [] |
| 2 | text-bar | what's on my calendar today | ["t0"] | quick | t0 | schedule.on_date | | answered | [] |
| 3 | voice | What's blocking the Halcyon rollout? | ["t0","main"] | main | main | | no_match | answered | ["linear.list_issues","linear.list_issues","gmail.list_messages"] |
| 4 | text-bar | What's blocking the Halcyon rollout? | ["t0","main"] | main | main | | no_match | answered | ["hubspot.list_deals","github.list_issues"] |
| 5 | text-bar (repeat of 4) | What's blocking the Halcyon rollout? | ["t0","main"] | main | main | | no_match | answered | ["gmail.list_messages"] |

- **Tier-0 pair:** `tiers_attempted`, `owner`, `answered_by`, `intent` and `tools_used` are identical, and only `channel` differs. The reply text differs only by the channel's rendering: voice gives "Today, Friday, September 25: 11 PM Halcyon standup." plus a `sentence` event, and text-bar gives "Today, Fri 25 Sep: 23:00 Halcyon standup".
- **Main-path pair:** `tiers_attempted`, `owner`, `answered_by`, `intent` (empty) and `escalation_reason` are identical. **`tools_used` is not.**
  - Row 5 repeats row 4 on the *same* channel and got a third different tool set.
  - So the model's tool choice varies from run to run, even with the channel held fixed. The channel isn't what changes it.
  - §7.5's "identical `tools_used`" can only hold for Tier 0. For the main path it is non-deterministic by design, in the same sense as §7.1.7's reply text. Suggested correction to §7.5: "same tiers, owner, answered_by, intent and escalation_reason; tools chosen by the model."

## (3) Tool events on the turn stream (NDJSON)

A mechanical check of each capture, confirming that every `tool_start` is matched by a `tool_end` with the same `step_id`, in order, all before `done`:

| capture | tool_start | tool_end | matched pairs | unclosed | all before done | a label contains argument text |
|---|---|---|---|---|---|---|
| t0 voice ("what's on my calendar today") | 0 | 0 | 0 | 0 | yes | no |
| t0 text-bar | 0 | 0 | 0 | 0 | yes | no |
| tool voice ("What's blocking the Halcyon rollout?") | 4 | 4 | 4 | 0 | yes | no |
| tool text-bar | 3 | 3 | 3 | 0 | yes | no |
| tool text-bar, repeat | 2 | 2 | 2 | 0 | yes | no |

The whole Tier-0 voice stream (no tool events):
```
{"kind":"ack"}
{"kind":"delta","text":"Today, Friday, September 25:\n11 PM Halcyon standup."}
{"kind":"sentence","text":"Today, Friday, September 25:\n11 PM Halcyon standup."}
{"kind":"done","text":"Today, Friday, September 25:\n11 PM Halcyon standup."}
```

The tool-calling voice stream, with deltas omitted:
```
{"kind":"ack"}
{"kind":"sentence","text":"One moment."}
{"kind":"tool_start","step_id":"stp_66896c1f96bb03dcd9acfc51","tool":"linear.list_issues","label":"Checking tickets in Linear"}
{"kind":"tool_end","step_id":"stp_66896c1f96bb03dcd9acfc51","tool":"linear.list_issues","label":"Checking tickets in Linear","status":"ok"}
{"kind":"tool_start","step_id":"stp_3d37b4376f92029bda585202","tool":"quick.mail_from","label":"Looking up an email"}
{"kind":"tool_end","step_id":"stp_3d37b4376f92029bda585202","tool":"quick.mail_from","label":"Looking up an email","status":"denied"}
{"kind":"tool_start","step_id":"stp_847d8b54d8d89ae888b6c34f","tool":"linear.list_issues","label":"Checking tickets in Linear"}
{"kind":"tool_end","step_id":"stp_847d8b54d8d89ae888b6c34f","tool":"linear.list_issues","label":"Checking tickets in Linear","status":"ok"}
{"kind":"tool_start","step_id":"stp_3aba9ca046e50f974572a988","tool":"gmail.list_messages","label":"Searching your email"}
{"kind":"tool_end","step_id":"stp_3aba9ca046e50f974572a988","tool":"gmail.list_messages","label":"Searching your email","status":"ok"}
{"kind":"sentence","text":"I'm not finding open blockers in Linear or recent mail about Halcyon."}
…
{"kind":"done","text":"I'm not finding open blockers in Linear or recent mail about Halcyon. …"}
```

On text-bar the steps were `hubspot.list_deals` "Checking deals in HubSpot" (ok), `company_finance.budget_status` "Reading the budget sheet" (denied, since there is no credential in the isolated vault) and `github.list_issues` "Checking issues on GitHub" (ok). The repeat had `quick.cached_brief` "Reading this morning's brief" (denied, since no brief is cached) and `gmail.list_messages` (ok). Every label is fixed text from the function spec or the quick table, and none contains an argument; the model's query `"Halcyon rollout blocker blocking"` appears only in `executed.jsonl`.

Observations, no action required:
- A quick tool that returns an error (`quick.mail_from`, `quick.cached_brief`) ends as `denied`, and a denied call is not written to `tools_used`. That matches `quick.go`.
- The text-bar escalation stream carries two bare `ack` events: the turn's ack, then the escalation ack after `ack_ms`. Voice turns the second one into the spoken "One moment." This predates Slice V, and clients tolerate it.

## (4) A fixture decision crossing needs-you, then the notification and the anchor

At startup, the first `needsyou.Service.Tick` produced exactly one row:

```
id                            record_type  record_id              title                                                                          delivered_at
ntf_afc1a20b30855c3259ca7f5a  decision     card-e7cd35fb48d8335f  Inbound decision: Speaking invite (from Dana Reyes <dana@partner-co.example>)  (null)
```

| request | result |
|---|---|
| `GET /v1/notifications?undelivered=1` | 200, one item (above), `delivered_at: null` |
| `POST /v1/notifications/ntf_afc…/delivered` | 200, `delivered_at: 2026-09-26T05:21:23.631191Z` |
| the same POST, repeated | 200, **the same** `delivered_at` (idempotent) |
| `POST /v1/notifications/ntf_nope/delivered` | 404 `no notification ntf_nope` |
| `GET /v1/notifications?undelivered=1` after the mark | 200 `[]` |
| `GET /v1/notifications?undelivered=1` with no token | 401 |
| `POST /v1/threads/anchor {"anchor_type":"decision","anchor_id":"card-e7cd35fb48d8335f"}` | 200 `thread.id=thr_b8ca142e42b4bdd454b2e810`, `anchor_type=decision`, `anchor_id=card-e7cd35fb48d8335f`, `anchor_untrusted=true`, `created=true`, with the card snapshot in `anchor_context` |
| the same anchor POST, repeated | 200, **the same** `thread.id`, `created=false` |
| `GET /v1/threads/thr_b8ca142e42b4bdd454b2e810` | `anchor_type=decision`, `anchor_id=card-e7cd35fb48d8335f` |

`SELECT … FROM threads` returns exactly one thread.

**Across a restart:** the harness was stopped (SIGTERM) and started again on the same home. The post-restart tick ran at 05:23:17Z (its decision-need fetches are in `executed.jsonl`) and rebuilt the same `card-e7cd35fb48d8335f`, and `notifications` still held exactly one row, already delivered. After the restart, `GET ?undelivered=1` returned `[]`. So the notification fires once, ever.

## (5) Spoken yes against a click, on two identical `gcal.create_event` envelopes

Both envelopes came from the same Tier-0 voice utterance, "schedule a meeting with priya tomorrow at 3:15pm". Priya resolves from the seeded message. The attendee `priya@example.com` is internal under the test `internal_domains`.

- **E1 `env_e604fb3118dcea1035d81261`:** proposed on voice, then resolved by a second voice turn `"yes"`. The reply was `"Got it."`, with `route_log` `approvals.respond`, outcome `decided`.
- **E2 `env_47de4c8009c856a9b08f2338`:** proposed on voice, then resolved by `POST /v1/approvals/{id}/decision {"payload_hash":"61710aa1…","reply":"yes"}`. That is the call the HUD's Approve makes. The response was `executed:true`, `output {"id":"fake-ev-2","status":"confirmed"}`.

Both `approval_required` events were complete. Here is E1's:
```
{"kind":"approval_required","approval_id":"env_e604fb3118dcea1035d81261","action":"gcal.create_event","risk":"medium",
 "payload_hash":"61710aa11e918a24566dba1b6d120f185df7af5ca5357de4e68b64ab783cb418",
 "read_back":"Create event 'Meeting with Priya Nair' starting 2026-09-26T22:15:00Z ending 2026-09-26T22:45:00Z with priya@example.com. Say yes to create it or no to cancel."}
```

| | E1 (spoken yes) | E2 (POST decision) |
|---|---|---|
| action / risk / origin | gcal.create_event / medium / p0 | gcal.create_event / medium / p0 |
| payload_hash | 61710aa1…cb418 | 61710aa1…cb418 |
| payload | `{attendees:[priya@example.com], start:2026-09-26T22:15:00Z, end:…22:45:00Z, title:"Meeting with Priya Nair"}` | identical |
| final status / reason | executed / executed | executed / executed |
| audit entries (kind, allowed, reason) | 33 propose ✓ "awaiting approval" → 34 approval ✓ "answered yes" → 35 call ✗ "received taint=tainted" → 36 decision ✓ "level A with approved envelope" → 37 execute ✓ "ok" | 38 propose → 39 approval "answered yes" → 40 call "received taint=tainted" → 41 decision "level A with approved envelope" → 42 execute "ok": the same five kinds, flags and reasons, with the same `args_hash` |
| executed call (`executed.jsonl`) | `gcal.create_event {attendees:[priya@example.com], end:…22:45:00Z, start:…22:15:00Z, title:"Meeting with Priya Nair"}` | identical args |

They differ only in ids, timestamps, the audit hash chain and the fake's returned event id. Both paths run `decideAndExecute`. (The `call … taint=tainted` line is the gate's taint record. Earlier main-path turns had read external mail, so the session was tainted; the level-A envelope still executes, as designed.)

**A spoken yes on `gmail.send_message` is refused as tap-required.** The voice utterance "send priya a reply saying sounds good see you then" proposed E3 `env_5f02a41b3e41d29510c3699c` (`gmail.send_message`, risk **high**, with a complete `approval_required` carrying the read-back and hash). Then a voice `"yes"` produced:

```
{"kind":"ack"}
{"kind":"approval_required","approval_id":"env_5f02a41b3e41d29510c3699c","action":"gmail.send_message","risk":"high","payload_hash":"18d2b71b…0afa","read_back":"Send email to priya@example.com, subject 'Re: Halcyon sync'. Body: 'sounds good see you then'. Say yes to send or no to cancel."}
{"kind":"delta","text":"That one needs a tap to confirm."}
{"kind":"sentence","text":"That one needs a tap to confirm."}
{"kind":"done","text":"That one needs a tap to confirm."}
```

`route_log` recorded outcome `tap_required`. The envelope was still `pending`, and `executed.jsonl` had no `send_message`. The HUD click (the same `POST …/decision` with its hash) then resolved it: status `executed`, a call to the fake gmail, and audit 43 propose → 50 approval → 51 call → 52 decision → 53 execute.

`water audit verify` on the isolated home returned `audit log verifies: 53 entries`.

## (6) No new dependency

`git diff --exit-code HEAD -- go.mod go.sum clients/macos/Package.swift clients/macos/Package.resolved` exits 0 with no output. `go.mod` requires only the pre-existing modules (sqlite, cobra, yaml, charm); FluidAudio 0.17.4 is still the Swift client's only package.

## (7) CONTEXT.md

`docs/CONTEXT.md:126` has `## Amendment (2026-09-25, Slice V revised brief)`, and `docs/CONTEXT.md:128` has `Context is fetched on demand through tool calls, not accumulated ahead of time.`

## Gaps found by this pass (for known-gaps)

1. **An isolated `WATER_HOME` does not isolate `water daemon` from real accounts.** `vault.Default()` is the Keychain on darwin, and `gcal`/`gmail` are always the real connectors. A future write-verification pass either needs this harness pattern or a supported switch: a vault or connector override for verification, never the default.
2. **The voice bench times the filler, not the answer.** Its `first_sentence`/`first_audio` fire on the daemon's spoken ack ("One moment.") on escalated turns. The bench should skip the ack filler, or report `route_log.first_sentence_ms` next to its own numbers.
3. **§7.5's `tools_used` equality can't hold on the main path** (model-chosen tools; see row 5). Amend the criterion.
4. **Kokoro bench numbers are still owed.** The models aren't in `~/Library/Application Support/Water/Models/`, so the owner needs to accept the one-time download in Water.app first.
5. **The HUD's first-click risk** (review residual 2) and the page mic-while-busy state (review residual 4) can only be judged on the device. See the checklist.

## Owner-only checks on the device (checklist)

Two ways to run these:
- **Against the real daemon, for read-only questions.** Launch the signed app from Terminal so the `voice_trace` lines print: `clients/macos/build/Water.app/Contents/MacOS/Water`.
- **For the approval steps, don't approve anything on a real account that you don't want to happen.** Reject, or use a harmless event with only yourself invited. Turning voice approval on in `~/.water/config.yaml` (D4) is your call: `water config set router.voice_approve.enabled true` and `water config set router.voice_approve.internal_domains <company domain>`, then restart the daemon.

- [ ] **0. Models.** Launch Water.app. Accept the one-time download dialog: Parakeet and Kokoro in one progress window, into `~/Library/Application Support/Water/Models/`. Then run `clients/macos/.build/release/Water --voice-bench --n 30 --tts kokoro` and paste its summary into this doc.
- [ ] **1. Hotkey from another app, with a real mic and speakers (about 10 turns).**
  - Focus another app, such as Safari. Hold ⌃⌥V, say "what's on my calendar today", and release.
  - Expect the spoken answer through the speakers, with no focus change. The Terminal prints `voice_trace … key_up->stt_final … first_audio …`.
  - Repeat about 10 times, mixing in "what's blocking the Halcyon rollout". Record min/median/p90 of key-up→STT final→ack→first sentence→first audio from the `voice_trace` lines.
  - For escalated turns, read the first sentence after "One moment." (see gap 2).
- [ ] **2. The blob reacts visually.**
  - While holding ⌃⌥V and speaking, the compact blob sits next to the Ask panel and its size or wobble follows your voice. It goes still when you're silent.
  - After release it pulses at a steady, time-driven rate until the first sentence arrives.
  - While Kokoro speaks it moves with the audio. With Apple Speech it gets a gentle fixed wave instead, which is the known limitation.
  - When nothing is happening, no blob is on screen (D7).
- [ ] **3. The HUD pops for tool calls only.**
  - Hold ⌃⌥V and ask "what's blocking the Halcyon rollout" (on the real daemon it needs Linear or GitHub connected, or use `--demo`). Expect the HUD to expand with at least one live step row, such as "Checking tickets in Linear", **before** the answer ends. It collapses about 4 s after the reply finishes.
  - Ask "what's on my calendar today" and expect the HUD **not** to expand.
- [ ] **4. The approval card is pinned, and the first click is safe.**
  - Say "schedule a meeting with <yourself> tomorrow at 3:15pm". Expect a card with the exact read-back, plus Approve, Edit and Reject, that stays until it's resolved.
  - While the card is up, click in another app near the HUD and confirm that click doesn't land on Approve (review residual 2).
  - Click **Reject**. The card disappears, and `water approvals` or the workspace shows it `denied`.
  - Optionally, with voice approval on and an internal attendee, stage it again and say "yes". The card disappears after the re-read, as a clicked one does.
- [ ] **5. A native banner, and the tap.**
  - With a decision at or above `notify.min_severity`, wait up to 30 s. Expect one macOS banner, and Allow the permission prompt the first time.
  - `sqlite3 ~/.water/water.db "select id,record_id,delivered_at from notifications"` shows `delivered_at` set.
  - Tap the banner. The workspace opens on a thread with `anchor_type=decision` and that card id.
  - Tap it again from Notification Center, or re-trigger it. The same thread opens, and no second banner appears for the same card, even after `launchctl kickstart -k` of the daemon.
- [ ] **6. The page mic (V-ui2).**
  - Open the workspace (⌃⌥W) with a thread open. Hold the bar's mic, speak, and release. The question and the answer appear in that thread as `voice` messages, and the answer is spoken.
  - Also try holding the page mic while a hotkey voice turn is still speaking (review residual 4). Note whether the page gets stuck on "Listening…".

## Cleanup

The harness was stopped with SIGTERM and exited; `ps` shows no `vv-harness` process. `/tmp/wvv.YhXn` was removed: the harness source, both binaries, the isolated home, its database, audit log and captures. Nothing was written under `~/.water`.
