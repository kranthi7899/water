# Slice UI: interface revamp and demo population

Status: **Plan (2026-09-26). Waiting for the owner's approval. No code has been written.** This plan follows `docs/WORKFLOW.md`'s Plan phase and the style of `docs/slices/V.md` §7 and `docs/slices/W.md`.

Sources:
- The owner's brief, "Water interface build: one ordered plan, demo population last" (2026-09-26), below called **the brief**.
- `docs/demo-sequence.md`, below called **the demo**.
- Four read-only exploration passes covering backend and data, web UI, the macOS client, and demo feasibility. They read the code, the live `water.db` (`sqlite3 -readonly`), the audit log, `~/Downloads/Renaissance_*.xlsx`, and the owner's Drive through the read-only connector. No secret or Keychain value was read.

Every `file:line` below was checked against the working tree on 2026-09-26. Files that another build is editing right now are marked *(in flux)*, and their line numbers may move.

**Baseline warning.** The working tree is not clean. About 96 files are changed or untracked:
- all of V §8–§10 (the globe, the glass tab, voice mode, and the deleted `ActivityHUD.swift` and `ActivityView.swift`);
- all of Slice W, including `internal/connectors/{research,display}/`, `internal/spokenemail/`, `internal/approvals/recipientcheck.go` and `migrations/0015_route_log_class.sql`.

The UI phases edit the same files: `gmail.go`, `approvals/queue.go`, `gateway/daemon.go`, `WaterClientCore/*`, `twins/ceo/twin.yaml` and the migrations directory. **Precondition P0: the coordinator commits the pending V/W work before Phase 0 starts.** Otherwise the first UI commit sweeps in another build's changes. Builders must not revert or tidy any of that work.

**Precedence.** The brief says it "supersedes the separate Slice V and Slice D prompts". The owner made several decisions *after* those source prompts were written:
- V §8–§11: the globe, the glass tab, voice mode, a movable globe.
- Kokoro via FluidAudio.
- Slice W.
- CLAUDE.md's standing rules.

Where the brief conflicts with one of these, this plan **flags** the conflict in §1 and asks in §2. It does not resolve it silently.

---

## 1. Findings: where the brief doesn't match the code or later owner decisions

### 1.1 Conflicts with owner decisions made after the brief's sources

1. **The step-list HUD was removed (V §8, 2026-09-26).** The owner said: "the visual aspect to see what agent is doing is kinda not great, let's not have that."
   - `ActivityHUD.swift` and `ActivityView.swift` are deleted in the working tree.
   - `tool_start`/`tool_end` still stream, and `ActivityModel.steps` still records them (`WaterClientCore/Activity.swift:109,208-226` *(in flux)*), but nothing draws them.
   - What replaced the HUD:
     - the Metal **globe** (`GlobeHUD.swift`, `Globe/`, `GlobeModel.swift`);
     - the blue **glass tab**, which shows only approvals, `email_draft` artifacts, `display.show` artifacts and app notices (`GlassItem.shouldShow`, `WaterClientCore/GlassTab.swift:287`). Everything except an approval hides after 10s.
   - This conflicts with:
     - brief Phase 6 (the blob expanding into the HUD, live job steps, promoted notes, "one view at two sizes", "running now" strip);
     - brief acceptance ("a tool-using command opens the HUD");
     - demo step 6 ("HUD shows the step live … promoted note");
     - scenario H ("Research, HUD");
     - demo decision 1 in `demo-sequence.md`. That text is stale.
2. **Promoted notes and a jobs schema were dropped by owner decision V D1 (2026-09-25, `V.md:180`).** The decision was "No jobs schema. Promoted job_notes … belong to J's task-event trail."
   - Brief Phase 1 item 6 ("on top of the existing job/workbench tables") and Phase 6 ("promoted notes only") re-ask for them.
   - No `jobs`, `job_steps`, `job_notes`, `workbench` or `ideas` table exists in migrations 0001–0015.
3. **Voice keys.** The brief says "push-to-talk hotkey". Today ⌃⌥V *toggles* voice mode, the owner holds Space to talk, and Esc exits (V §9–§10, `HotKeys.swift:13-15,114-165`, `VoiceMode.swift`).
   - ⌃⌥V goes through an NSEvent global monitor, so it needs the Accessibility grant.
   - While voice mode is on, Space is captured system-wide.
   - The workspace page's mic is still true hold-to-talk.
4. **TTS.** The brief says kokoro-mlx. The owner approved Kokoro-82M through FluidAudio's `KokoroAneManager` on 2026-09-25 (V §0.1, §7), and it is built (`KokoroSpeaker.swift`). The brief's wording is superseded; nothing gets built for it.
5. **The blob state machine is done, but it is the globe now.**
   - `ActivityModel` phases are `idle|listening|thinking|responding|needsYou`, with `searching` being added *(in flux)*.
   - It is driven by real mic RMS, a time-driven breath and Kokoro `averagePower`.
   - The brief says `needsYou` "expands into the HUD". Here it renders a calm orb (`GlobeModel.swift:31`) and the approval appears in the glass tab.
6. **A single spoken "yes" no longer sends.** Slice W D5b: for `gmail.send_message`/`twinlink.send_message` on P0 with no warnings, the flow is "yes", then a spelled read-back of the recipient, then "confirm send" within 30s.
   - Warnings, P2 origin, money and public posts stay tap-only.
   - Demo steps 7, 8 and 13 therefore become a two-step flow.
   - The read-back spells the recipient aloud, so on stage it would spell the owner's `+` alias (W.md §2 D5b).
7. **"noise" is already a word in W.** W §8 is titled "voice approvals + noise", and route_log gained `class` (`company|general`, migration 0015). Phase 0a's email "noise" verdict is a different concept. It stays in the `decisions` namespace and is documented as distinct (§2 U21).
8. **CLAUDE.md "zero metered spend" against the phone beat.**
   - The demo offers "Twilio's free trial credit", and the brief says to use `blandcall` "if it works".
   - Bland bills per call. `cmd/blandcall/main.go:5-21` says it is deliberately outside the gated product.
   - Twilio is a new paid telephony service, which the brief itself rules out ("new telephony" is out of scope).
   - Carrier email-to-SMS through the existing gated `gmail.send_message` is the only compliant path. See §2 U6.
9. **CLAUDE.md "commit at the end of each phase" against the uncommitted backlog.** See precondition P0 above.

### 1.2 The brief against the code

10. **Why the Academia.edu mails became decisions** (Phase 0a):
    - The candidate predicate is `decisions.NeedsAttention` (`internal/decisions/attention.go:13-44`). It uses only `From` and `Subject+Body`: a bulk-sender list plus a bare `"?"` marker.
    - `updates@academia-mail.com` and `premium@…` match no bulk marker, and "is this your paper?" contains `?`.
    - The model classifier then gave `needs_decision=1` to `1a0d94cf1146cf24` ("Kranthi, is this your paper?", inbound_decision 0.70) and to the FIELA pair `19bf0ad3c9df3322`/`19472284f71169a8` (0.75).
    - It correctly rejected about 8 near-identical mails (0.88–0.98), so the model is inconsistent.
11. **The brief's Gmail signals are never captured today.**
    - `fetchMetadata` asks only for `From, To, Subject, Date` (`internal/connectors/google/gmail/gmail.go:413`).
    - `gmailMessage` has no `labelIds`, so `CATEGORY_*` is dropped. The history path reads `labelIds` (`:337`), but only to skip SPAM, TRASH and DRAFT.
    - `get_message` keeps only From, To and Subject.
    - `store.Message` (`internal/store/records.go:34-47`) has no header or label columns.
    - All 22 live Academia rows are snippet-only (`body_full=0`) with no headers. So the regression fixtures must pass on **content signals alone**, and on header signals separately. That is what "via general signals" requires.
12. **The candidate filter exists in two copies.** `internal/runtime/brief.go:43` `needsAttention` is a byte-for-byte copy (the note is at `trigger.go:26-39`). A filter that changes only one copy leaves the morning brief citing bulk mail.
13. **The Needs-you window is on `created_at` (ingest time), not `sent_at`** (`internal/decisions/trigger.go:246`, `store/records.go:345-346`). A re-sync on 2026-09-26 pulled the Jan 2025 FIELA mail in as a fresh candidate. This is a real bug.
14. **The persisted verdicts don't need a purge.** `Triage` checks the candidate predicate *before* either cache (`internal/decisions/classify.go:176`). A candidate-level filter therefore overrides the stale `needs_decision=1` rows without touching `decision_classifications`.
15. **`for_workspace` already exists as `in_workspace`** (`store.LinkInWorkspace`, `internal/store/links.go:24`). Nothing writes it. The brief says to write it "the same way `Involve` and `ForProject` are", but `ForProject` has **no** writer (`recordlinks.CopyLinks` only copies it). Only `involves` has writers, and even those resolve nothing live: every roster person has `email: null` (`twins/ceo/seed/people.yaml`), so the live `links` table has zero `involves` edges.
16. **`approvals.origin` is taken.** It is the gate's priority origin (`p0`, `internal/approvals/queue.go:40`, carried into every audit record). The brief's `origin_kind` and `requested_by`, and its `origin: demo_seed` (item 8), must use other names.
17. **Only the model can create approvals** (`Queue.Propose`, `queue.go:122-151`). There is no intake for a *person request*, such as Lee's reallocation or Riley's signature. Such an approval also has no outward action to execute.
18. **The approval status trail.**
    - Status is CHECK-constrained to `pending|approved|denied|expired|executed` (`migrations/0001_init.sql:140`). The brief's "reply" would need a table rebuild.
    - The Gmail thread id of a send is never stored on the approval. `sendMessage` starts a new thread (`gmail.go:545-565`).
    - Replies to the agent alias arrive through agentmail, which never persists messages (`agentmail/classify.go:56-58`).
    - So "advances automatically when a reply arrives" has no pipeline, and the brief's fallback ("build the four states now") applies.
19. **Decision cards are not persisted.**
    - `Trigger.Run` rebuilds them every time from classified messages, with model-phrased prose (`trigger.go:188-198`). Card ids are `sha(typeID+ref)` (`decisions/build.go:103,358`).
    - Seeded decisions (Meridian, Lexicon, B, C), hand-written `team_signal`, and research "Attach" therefore have nowhere to live without a new persisted table.
    - `Card` has `Recommendation`, `Options`, `StagedActions`, `Evidence` and `Gaps` (`decisions/card.go:17-37`). It lacks `team_signal`, `action_suggestions` and a per-evidence icon.
    - `Card` has no JSON tags; `app.js:188-201,359-375` reads Go names.
20. **One envelope per card.** Staging a second action while one is pending returns `already_staged` (`gateway/workspace_decisions.go:130-136`), and `store.CardState` holds one `ApprovalID`. Meridian has two actions (the email and the CRA-3 bump), so staging has to become per-action. The stage request is also keyed by `function`, so two suggestions using the same function cannot be told apart.
21. **Envelopes expire after 15 minutes** (`approvals.DefaultTTL`, `queue.go:65`). That rules out:
    - seeded approvals that must survive the walk from step 1 to step 13;
    - the brief's draft-before-approval model (Phase 3, "Save / Send for approval");
    - the current Drafts design (V D2-A: a draft *is* a pending envelope, `app.js:594-618`).
22. **The brief contradicts the card invariant.** The brief's card drops Gaps, but `card.go:26` says "stated openly, never hidden". The brief's single "Approve (primary)" contradicts the two-click stage → read-back → confirm flow owner-decided in V D3 (`app.js:66-73` `armed()`).
23. **W's warnings are invisible in the web UI.** `Envelope.Warnings` reaches `GET /v1/approvals/{id}`, but `app.js` never renders it. Only the glass tab does.
24. **No external links are possible from the page.**
    - These are all banned (`internal/webui/webui_test.go:19-45`): `https?://` literals, `.href=`, `window.open` and `location` writes.
    - `Workspace.swift:245-258` cancels any navigation that isn't `water://app`.
    - So "View related data → a link per source" and demo step 11 ("Open CRA-3 in Linear") need a new, deliberate native path (§2 U16).
25. **Dashboards and workspaces have no data sources yet.**
    - **Linear is not connected.** The audit (2026-09-26T05:44Z) records "credential for linear is unavailable", and `issues` has 0 rows. The connector has only `list_issues` (R, `internal/connectors/linear/linear.go:150-183`). Its query (`:102-116`) omits team, `state.type`, labels, `dueDate`, relations and comments. There is **no write function**. The demo workspace `water50` appears nowhere in the repo.
    - **`company_finance` has never run.** There are 0 audit calls and 0 `finance_figures` rows, and the Sheets re-consent is still pending (`EVOLUTION_PLAN.md:279`). `spend_breakdown` returns one row per call, and no function reads a monthly cost series, so the July spike (F) has no source.
    - **`company_customers` does not exist** (`gsheets.go:40-41` anticipates it). The only customer data is `~/Downloads/Renaissance_Customers.xlsx`, a local file. The brief both requires it and rules out new connectors.
    - **GitHub *is* connected**, for `kranthi7899/water` only (`~/.water/config.yaml`, executed calls in the audit). It has no `list_commits` and no `merged_at`, and there is no mapping from project to repo. So "Connect GitHub" tiles only apply to the other five projects, and 4-week commit bars need a new read function.
    - **Rate caps.** "Computed at render time" collides with the 60/h caps on `linear.list_issues` and `company_finance.*`, and with the starvation fixed in commits 756aae4 and 4a47519. Dashboards need the same TTL-cache pattern at a low origin.
26. **`research.web` is one synchronous 40s call that stores nothing** (`internal/connectors/research/research.go:40` *(in flux)*). There is no multi-step state for "3 of 5 steps" or for the Queued/Running/Finished columns.
27. **Meetings.**
    - Structured recap signals exist (`meetings/recap.go:54-65`: Decisions, ActionItems with Owner, OpenQuestions, FYI, ProjectGuess) but are not stored.
    - The daemon passes a nil classifier, so the project guess is always "unavailable" (`gateway/workspace_meetings.go:52-54`).
    - Only recorded sessions are listed. Upcoming calendar meetings are not, and the live `events` table has 0 rows.
28. **Registration cost of every new route.** Each page-reachable route is the `daemon.go` `Mux()` entry plus **three hand-kept allowlists**:
    1. `internal/webui/static/api.js`;
    2. `internal/gateway/webui_test.go` `allowed` (`:98`), plus `suffixes` (`:92`) for any new `'/suffix'`;
    3. `clients/macos/Sources/WaterClientCore/WorkspaceAllowlist.swift` `routes` (`:67-87`), plus a case in `WorkspaceAllowlistTests.swift`.

    Constraints that come with this:
    - Swift ids must match `[A-Za-z0-9_-]{1,128}`. Query values must match `[A-Za-z0-9._-]{1,64}` and are allowed only on routes that declare their keys.
    - New page views must also go into `pageViews` (`:156`).
    - The Go test bans the bare substrings `turns`, `notifications` and `thread_id` anywhere in `api.js`, comments included.

### 1.3 The demo against its data

29. **No single "today" makes the story's numbers true.**
    - The sheets compute aging as of **2026-09-25**. Northstar's "25 days overdue / 36 days since contact" hold only on that date.
    - "Meridian due in 13 days" needs **Oct 2** (renewal is Oct 15).
    - "Lexicon ~7 weeks out" is about 9.4 weeks from Sep 25 and 8.4 weeks from Oct 2 (renewal Nov 30).
    - No clock plumbing exists. `runtime.Env.Now` is one hook among 65 non-test `time.Now()` calls.
30. **"65% of YT-Recamendo revenue" is $9k/$14k = 64.3%.** The copy should say "about 64%" or "nearly two-thirds".
31. **There are two finance sheets.**
    - The connector's fixed id `1Jkt1…` (`gsheets.go:42`) is not visible from the umass Drive. The umass copy `1wQpG…` has 9 tabs and none of the connector's "Spend by Application" or "Funding & Cap Table" tabs.
    - The 12-tab `~/Downloads/Renaissance_Finance - 2.xlsx` matches the connector's ranges, but it is internally inconsistent: Voice_text revenue is about $517/mo against $9,000 elsewhere, the Monthly P&L revenue row is shifted 4 months, and "September run-rate" uses Nov/Dec figures.
32. **Facts that trace to a real source:**
    - Meridian: $9k/mo, renewal Oct 15.
    - Meridian NPS 5, "duplicates are embarrassing".
    - Elena Park.
    - Lexicon: $2.8k/mo, +15% quote (ECO-6), Nov 30, owned by Riley.
    - Fenwick: +$2.2k/mo (VOI-1).
    - Northstar: INV-1042, $5k.
    - July: $9k cloud overage.
    - Nimbus: 99.9% SLA, Oct 16 (KEV-4).
    - Atlas: Oct 12 review.
    - Lumen and Brightwater prospects.
    - Nina, Lee, Riley, Jordan, Alex (final interview), Marcus and Quinn are in `people.yaml`.
33. **Items that trace nowhere:**
    - the "Kafka sync" meeting;
    - the Halcyon kickoff as an *upcoming* meeting (the sheet has it on Sep 1);
    - Alex's offer and document statuses;
    - Lee's $3k/mo reallocation;
    - "3 of 5 steps" research;
    - the "on-device short-utterance model" idea;
    - Nina's and Lee's team-signal lines;
    - "Tulasi Garu" (and there is no Google contacts scope).
34. **Labels that are wrong or unclear:**
    - **SUP-305 is not a Linear issue.** It is a help-desk ticket in the customers xlsx.
    - **YTR-6 is ambiguous.** `people.yaml:31` has `linear: P-YTR-6` as the *project* "Meridian renewal".
    - **Kevin is a team/product, not a person.**
    - **Meridian's owner is Priya** (`people.yaml:77`), yet the team signal names Nina and Lee.
    - **Linear's Business trial ends 2026-10-24** (finance Vendors tab).
35. **The demo contradicts itself and its data:**
    - Step 1 expects "Nothing pending", but the brief seeds four approvals before the demo.
    - Seeded calendar events are invisible to the model's live `gcal.list_events`. Only reflex calendar answers and the brief read the store.
    - Personal mail and calendar mix into voice answers, because the demo allowlist covers Today only.
36. **Step 13 needs two approvals.** It says "approve extension → CRA-3 priority changes", but an envelope carries one action. It needs either two envelopes or a new bundling concept, which would be a gate change the brief rules out.

---

## 2. Owner decisions needed before building

Each decision has options, a recommendation and the phase it blocks. **U14 and U21 block Phase 0. U13, U15 and U2 block Phase 1.** The others block only the phase named.

**U1. What replaces the HUD in Phase 6, demo step 6 and scenario H** (blocks Phase 6 and Phase 7 step 6).
- **A (recommended).** No step list.
  - While a tool that announces itself runs (e.g. the Linear comment), the globe uses a distinct look, the same pattern as the in-flux `searching` look for `research.web`.
  - When the relayed reply lands, the daemon emits a **code-built** glass-tab artifact `note`: `{title: "Nina (simulated)", body, source: "Linear CRA-3", simulated: true}`.
  - It is built from the tool result, never from model text, so "(simulated)" can't be dropped. It uses the 10s linger and ✕.
  - Scenario H's steps render only in the Research workspace (Phase 5).
- **B.** The model calls `display.show` with the reply. No new event type, but the "(simulated)" label then depends on the model, and the body is relayed, untrusted text.
- **C.** Voice only ("Nina says … (simulated)"). Linear still carries the exchange for step 11.
- **D.** Restore a step list in the glass tab. This reverses the owner's 2026-09-26 removal, so it is not recommended.
- Also restate the brief's acceptance line as follows: "a tool-using voice turn emits `tool_start`/`tool_end` (NDJSON capture) and the globe reflects it; a Tier-0 lookup emits none; the glass tab appears only when there is something to show."

**U2. Promoted notes and a jobs schema** (blocks Phase 1 item 6 and Phase 5 Research).
- **A (recommended).** Keep V D1 for the HUD: no job_notes. Build only what the Research workspace needs:
  - a `research_runs` table (plus `research_steps`);
  - an `ideas` table.
  - They are deliberately **not** named `jobs`/`tasks`, so Slice J's `task_records`/`task_events` naming stays open (J Q5). J may absorb them later.
- **B.** Wait for J. Research and Ideas render seeded or empty in the meantime, and scenarios H and I are cut.
- **C.** A generic `jobs` table now. This pre-empts J, so it is not recommended.

**U3. Where customer data comes from, given "no new connectors"** (blocks Phase 4 Clients, Phase 5 Clients, Phase 7).
- **A (recommended).** The owner uploads `Renaissance_Customers.xlsx` as a native Google Sheet in the Water account. The owner approves a **read-only** `company_customers` connector as a named exception to "no new connectors". It is built in the existing gsheets package with the same fixed-range pattern, R level only, rate-capped. The brief names this source itself (Phase 1 item 2, Phase 4).
- **B.** A read-only reader for the local xlsx using stdlib `archive/zip` + `encoding/xml` (no new dependency). This is still a new connector, and it reads a file outside the repo.
- **C.** Seed-only rows from the roster `clients` table plus the xlsx, carried as `demo_seed`. This fails the brief's "every number computed from its connector".
- **D.** No customer data. The Clients dashboard and workspace show "Not connected"; steps 5 and 10 (NPS) and scenarios A and G lose their customer half.

**U4. Linear writes for the demo: agent comment, relayed comment, priority bump** (blocks Phase 7 steps 6, 11, 13).
- **A (recommended).** Add two level-A functions to the **existing** Linear connector:
  - `linear.create_comment {issue, body}`;
  - `linear.set_issue_priority {issue, priority}`.

  Rules for both:
  - Each goes through an envelope, per CLAUDE.md's outward rule.
  - In demo mode, `issue` must be on the `demo_routing.yaml` issue allowlist (CRA-3).
  - A relayed comment must start with a code-added "(simulated) Relayed:" prefix, enforced in the connector, not the prompt.

  Step 13 then shows **two approvals in sequence**: the extension email, then the priority bump the card already lists as its second action. There is no bundling.
- **B.** No write surface. The owner posts the two comments and bumps the priority by hand in `water50` before the demo, and Water only reads them. Step 13's "visibly changes in real time" is lost.
- **C.** A fake Linear connector for the demo. Step 11 ("open CRA-3 in Linear") would then show nothing real.
- Sub-question: do R-level **read** extensions to existing connectors count as "new connectors"? This covers Linear's team, state type, relations, due date and comments, GitHub's `list_commits` and `merged_at`, and a whole-grid finance read plus a monthly-cost read. **Recommend no**: allow read-function extensions, with no new connector ids except U3.

**U5. Connection actions only the owner can take** (block the live halves of Phases 4, 5 and 7).
- **Linear:**
  - Run `water connect linear --token …` with a `water50` key that has comment and write scope (if U4-A).
  - Confirm that CRA-3 (due Nov 1, assignee Nina) and the team keys WAT/KEV/YTR/CRA/VOI/ECO/OPE exist.
  - Say whether YTR-6 is an issue or the project.
  - Note that the trial ends Oct 24.
- **Google:**
  - Re-run `water connect google` to grant `spreadsheets.readonly`.
  - **Name the canonical finance sheet** (`1Jkt1…`, `1wQpG…` or the "- 2" xlsx) and fix the Voice_text revenue and the P&L shift in it (finding 31).
- **GitHub:**
  - Confirm the repo map: only Water → `kranthi7899/water`, while the other five projects show "No repo configured" or "Connect GitHub".
  - Or supply their repos.
- **Recommendation:** do all of this before Phase 4. Every dashboard tile shows "Not connected" until then, which is correct behaviour, not a bug.

**U6. The phone beat, step 14** (blocks Phase 7 step 14).
- **A (recommended). Carrier email-to-SMS through the existing gated `gmail.send_message`.**
  - It is free, uses an envelope, and passes W's two-step confirm.
  - The owner supplies the carrier's SMS gateway address. AT&T closed its gateway in 2025, and other carriers vary.
  - That address goes into `demo_routing.yaml`'s allowlist.
- **B. blandcall.** 2 of 5 calls are left (`~/.water/blandcall/state.json`). It is metered and ungated by design (`cmd/blandcall/main.go:5-21`), so it violates CLAUDE.md's zero-metered-spend rule unless the owner records a named exception.
  - Even then, recommend running it **by hand from a terminal**, not wired into the product.
  - Its stale help text says 3 calls (`main.go:72,188`).
- **C. Twilio trial.** This is a new paid telephony service, which the brief's out-of-scope list and CLAUDE.md both exclude. Not recommended.

**U7. Seeded items with no real source** (blocks Phase 7). For each item in finding 33, choose one:
- (a) the owner adds the row to its real source (a Linear issue, a sheet row or a calendar event);
- (b) seed it as `simulated: true`, shown with "(simulated)";
- (c) drop it.

Recommendation per item:
- Kafka sync: (a), a calendar event plus a Linear-consistent recap, or (b).
- Halcyon kickoff: (a) re-dated in the sheet, or re-label it the past meeting it is.
- Alex's onboarding statuses: (b).
- Lee's $3k/mo reallocation: (b), as a person request.
- Research "3 of 5": produced by really running the Phase 5 runner on "clinical dictation" before the demo, not seeded.
- On-device short-utterance idea: (b).
- Team-signal lines: (b) for both Nina and Lee.
- Tulasi Garu: resolved live through `gmail.list_messages` headers (no contacts scope). If not found, the read-back says so.

**U8. The "(simulated)" rule** (blocks Phase 7). The brief marks only Nina. **Recommend: anything a fictional person *says or asks for* is `simulated: true` and shows "(simulated)":** replies, requests, team signals and pulse answers. Records that trace to a real source row (a sheet row or a Linear issue) are `demo_seed` but not simulated.

**U9. The demo clock** (blocks Phase 7).
- **A (recommended).** Compute every relative figure from the clock, not from the sheet's aging columns: days overdue from the invoice due date, days since contact from the last-contact date, and days to renewal. Default `--today` to the actual demo day, and let the story's numbers follow ("due in N days").
- **B.** Fix `--today=2026-10-02` and restate the story: Northstar becomes 32/43 days and Lexicon "about 8 weeks".
- Either way, `--today` affects only seed dates and render-time arithmetic in dashboards and workspaces. The agent's own reasoning keeps the wall clock (the brief's rule: Phase 7 does not change agent logic).

**U10. Drafts: the brief's draft-before-approval model against V D2-A** (blocks Phase 3c).
- **A (recommended).** Add a small `drafts` table (template `reply|delegation|investor_update`, to, subject, body, provenance, simulated), with Save and "Send for approval" going through `Queue.Propose`, the same chokepoint as staging. The 15-minute TTL (finding 21) makes an envelope-as-draft impossible to persist. This reopens V D2 and its deferral to L.
- **B.** Keep D2-A and seed drafts as fresh envelopes at the start of the interface half. This fails the brief's "editable box + Save".

**U11. Approve is one click in the brief, two in the code** (blocks Phase 3a). **Recommend keeping two deliberate clicks** (V D3): "Approve…" styled primary, then the exact read-back, then "Yes, send it" (armed after 600ms). The alternative collapses the read-back the outward-action invariant relies on.

**U12. Gaps on the card** (blocks Phase 3b). Recommend a muted "Missing info: …" line under the recommendation, shown only when gaps exist. This keeps `card.go`'s "never hidden" invariant and the brief's clean card.

**U13. Persisting decisions** (blocks Phase 1 item 5).
- **A (recommended).** A `decision_records` table of persisted cards. Rows come from demo seed, and later from other code-built sources. The Decisions list and Today merge them with the Trigger's computed cards by card id.
  - Add `card_evidence_extra` for research Attach, merged into `Card.Evidence` at build time.
  - This changes where cards come from, not how they are reasoned about. No classifier or builder logic changes.
- **B.** Seed source *messages* and let the classifier build the cards. The prose is then model-written and non-deterministic, and `team_signal` has no source. Not recommended.

**U14. The priority rule** (blocks Phase 0b). Priority is computed server-side as `needsyou.Item.Priority` so native notifications can share it.
- **Urgent:** severity ≥3, or a deadline ≤3 days away.
- **High:** severity 2, or a deadline ≤7 days away.
- **Normal:** everything else.
- **Approvals:** risk high or money → high; a deadline ≤3 days → urgent. Person requests take the priority seeded or requested with them.

Today every decision is severity ≥2 (`notify.min_severity` is 2), so without the deadline rule every card would be amber. The owner may retune the thresholds.

**U15. Person-request approvals** (blocks Phase 1 item 3).
- **A (recommended).** A new internal, non-outward connector `requests` with one level-A function `requests.respond {request_id, answer, note?}`.
  - Approving a person request executes it through the gate like any other envelope. It records the answer and sends nothing.
  - "Request changes" stages a separate `gmail.send_message` envelope to the requester (Phase 3a).
  - The gate stays the only execution path.
- **B.** Approvals with a null action. That needs special cases in `decideAndExecute` and the gate, so it is not recommended.

**U16. External links: "View related data" and "Open CRA-3 in Linear"** (blocks Phase 3b and step 11).
- **A (recommended).** A new closed `WorkspaceMessage` case `{type:"open-external", source, id}`.
  - The **native** side resolves `source:id` to a URL from the store record, the URL the connector returned, never page text.
  - It opens the URL with `NSWorkspace` only if its host is on a fixed allowlist: `linear.app`, `mail.google.com`, `calendar.google.com`, `docs.google.com`, `github.com`.
  - The page never holds a URL. This is a security-surface change, so it needs explicit approval.
- **B.** Internal targets only: open a thread anchored on the source, or show the raw id. For step 11 the owner then switches to Linear by hand.

**U17. What the spoken read-back says for demo aliases** (blocks Phase 7 steps 7, 8, 13).
- **A (recommended).** Keep W's spelled read-back unchanged (safety first). In demo mode the read-back first names the resolution: "Elena Park, routed to your demo inbox". Then it spells the alias.
- **B.** Suppress spelling for allowlisted aliases. This changes W's safety rule, so it is not recommended.

**U18. Demo recipient enforcement** (blocks Phase 7). Recommend **both layers**:
- (a) `approvals.RecipientCheck` refuses any address not on `demo_routing.yaml` when `demo.enabled` is set, before an envelope exists.
- (b) A config-keyed gate rule in `authorize` (`internal/gate/gate.go:310-346`) denies an outward call whose recipients aren't allowlisted, even with an approved envelope.
- Layer (b) is the gate's first argument-based rule. It is small, but it is a gate change.

**U19. Demo step 1 against the seeded approvals.** Recommend restating step 1's expectation as "reports what is actually pending, without inventing urgency". The alternative is seeding approvals only after the voice half, but that is manual setup between steps, which the brief's acceptance forbids.

**U20. Live calendar against seeded events** (blocks Phase 7 steps 2 and 4).
- **A (recommended).** The owner creates a dedicated "Demo" Google calendar by hand. The seed lists the events it expects there (with source ids), and the demo-mode Today allowlist admits only that calendar.
- **B.** The seed writes events into the owner's real calendar. That is an outward action on a real account, so it is not recommended.
- **C.** Seeded events exist only in the store. Reflex answers see them, but the model's live `gcal.list_events` doesn't.

**U21. Naming** (blocks Phase 0/1). Recommendations:
- Reuse `in_workspace`; no `for_workspace` synonym.
- The triage verdict is `decisions.Class` = `noise|informative|decision`, documented as distinct from W's route_log `class`.
- The demo marker is `provenance = 'demo_seed'` (not `origin`), with `simulated` as a boolean.
- Approval metadata columns are `origin_kind`, `requested_by`, `kind` and `source_card_id`. The existing `origin` column is untouched.

**U22. Step 16's "build report".** Recommend that it covers Water's own GitHub PRs from the live connector plus the Linear issues closed since a date, rendered by `internal/reports`, then sent as a `gmail.send_message` approval to the demo alias. The owner should confirm the scope.

**U23. Sizing and session count.** This brief is about 14–18 sessions of work (§6). CLAUDE.md says "one slice per session", and the brief says "phase by phase". Recommend treating **each sub-phase below as one session**, each ending with its own commit and `EVOLUTION_PLAN.md` update by the coordinator.

---

## 3. Phases, in the brief's order

**Rules for every phase:**
- Tests are written first.
- The gates must pass before every commit: `go vet ./...`, `go test -count=1 ./...`, `CGO_ENABLED=0 go build ./cmd/water`, plus `swift test` when `clients/macos` changed.
- **Commit at the end of each phase and sub-phase.**
- Every new page route is registered in the Mux **and the three allowlists** (finding 28). The route tables below list the new `'/suffix'` literals for `webui_test.go`.
- Migration numbers are pre-assigned so parallel streams never collide: **0016** Phase 0, **0017–0019** Phase 1, **0020** Phase 3c, **0021** Phase 3d.
- Tests use `backend.Fake` and fake connectors, never `~/.water`.

### Phase 0 — fixes before anything new (1 session)

**0a. Triage noise pre-filter.**
- **New leaf package `internal/mailnoise`** (pure, stdlib only). It is imported by both `decisions` and `runtime`, which ends the brief.go copy (finding 12).

  ```go
  type Signals struct { Labels []string; ListUnsubscribe, ListID, Precedence, AutoSubmitted string }
  type Verdict struct { Class string /* "noise"|"informative"|"" */; Reasons []string }
  func Classify(m *store.Message, wroteTo func(domain string) bool) Verdict
  ```

  The rules are general. **No domain is named in the code.**
  - **Header or label → noise.**
    - The header is any of: `List-Unsubscribe`, `List-Id`, `Precedence: bulk|list|junk`, or `Auto-Submitted` not `no`.
    - The label is any of `CATEGORY_PROMOTIONS|UPDATES|SOCIAL|FORUMS`.
  - **Bulk sender shape → a signal.** Either the local part is one of an extended marker list (`updates@`, `premium@`, `news@`, `marketing@`, `info@`, `hello@`, plus the existing list), or a domain label contains `mail`, `email`, `news` or `em` as a hyphen- or dot-separated part (e.g. `x-mail.com`, `mail.x.com`).
  - **Claim/confirm pattern → a signal.** Phrases such as "is this your", "claim …", "confirm your …", "did you write", "are you the", "add to profile".
  - **Preheader padding → a signal.** A run of ≥5 invisible or combining characters (U+034F, U+200B–U+200D, U+FEFF, U+00AD).
  - **Classification.** The claim pattern **and** the sender domain never written to, together with at least one of bulk sender shape or preheader padding, → noise. A bulk sender with no claim pattern → `informative`.
- **`wroteTo`** is backed by a new `store.SentToDomain(ctx, domain)`: messages whose `From` is one of the CEO's own addresses. The addresses are the configured Gmail account and `agent.forward_to`, wired in `cli/twin.go` `buildDecisionsTrigger` (`:397-417`).
- **`decisions.Candidate`** becomes `CandidateWith(wroteTo)`, which returns false when `Class != ""`. Because the candidate check comes first (finding 14), stale cached verdicts stop mattering. `runtime/brief.go` calls `mailnoise.Classify` in its own `needsAttention`.
- **Gmail capture:**
  - `gmail.go` `fetchMetadata` adds `List-Unsubscribe`, `List-Id`, `Precedence` and `Auto-Submitted` to `metadataHeaders`.
  - `gmailMessage` gains `LabelIDs`.
  - `get_message` keeps the same headers.
  - `Normalize` fills the new `store.Message` fields.
- **Migration `0016_message_bulk_signals.sql`:** `ALTER TABLE messages ADD COLUMN labels TEXT NOT NULL DEFAULT '[]'`, plus `list_unsubscribe`, `list_id`, `precedence` and `auto_submitted`, all `TEXT NOT NULL DEFAULT ''`. It follows the 0006 `body_full` precedent. Old rows stay empty until re-fetched, which is why the content rules exist.
- **Window bug (finding 13):** `Trigger.build` also drops messages whose `SentAt` is before `now - window`. This is a candidate-selection fix, not a reasoning change.

**0b. Plain-language chips.**
- `internal/needsyou`: new `priority.go` with the U14 rule. `Item` gains `Priority` (serialized as Go names, like the rest of `Item`), `Deadline` stays as it is, and the approvals path gets its rule.
- `app.js`:
  - Remove the three pills at `:199-201` and `:373-375`.
  - Add `dueBadge(date)` ("Due Oct 15", date only; new `fmtDay` in `dom.js`).
  - Show readiness only when it is not ready, as muted text ("Missing info", "Blocked").
  - Show untrusted content as a glyph `span` with `title="Built from an outside email"`.
  - Add the row class `p-urgent|p-high|p-normal`.
  - Soften "Risk x" to "Medium risk", drop "Origin p0", show `statusBadge` words in plain language, and change "Prepare gmail.send_message" to "Prepare email".
  - Do not touch `dom.untrusted` quoting.
- `app.css`: add a 3px left border per priority class; remove `.badge.warn`, `.badge.ready-*` and `.badge.hot`.

**Endpoints:** none.

**Tests:**
- `mailnoise`:
  - Two fixtures, `testdata/academia_is_this_your_paper.json` and `academia_claim_fiela.json`, copied from live rows `1a0d94cf1146cf24` and `19bf0ad3c9df3322` (subject, from, snippet; the owner's first name is kept, nothing else personal) → noise, with reasons that include none of "academia".
  - **The same fixtures with the sender domain rewritten to `papers-mail.example` still → noise**, which proves the verdict comes from general signals.
  - The same fixtures with added headers and labels → noise via the header rule alone.
  - Controls stay not noise: a plain human question from a colleague, and a claim-pattern mail from a domain the CEO has written to.
- `decisions`: a fake classifier counts calls, and `Triage` on the fixtures makes **zero** classifier calls, even with a `needs_decision=1` row already in `decision_classifications`.
- `runtime`: the brief no longer cites the fixtures.
- `trigger`: an old `SentAt` with a new `created_at` is excluded.
- `gmail`: the metadata request names the new headers, and the label and header fields round-trip through `Normalize` (a fake HTTP server).
- `needsyou`: a table test of the priority rule.
- `webui_test.go` `TestNoRawPills`: `app.js` contains none of `'Severity '`, `'External content'`, `ready: 'Ready'` or `'Origin '`. This is the brief's "no raw pills" acceptance item.

**Commit:** "UI-0: bulk-mail noise pre-filter, sent_at window, plain-language chips".

### Phase 1 — data infrastructure (2 sessions: 1a+1b, then 1c+1d)

**1a. Workspaces, links, specs** (migration 0017, first half).
- `workspaces` gains `template`, `primary_source` and `spec_hash` (ALTER).
- New package `internal/workspaces`:
  - It loads the embedded `twins/ceo/workspaces/*.yaml` and validates:
    - `id` matches `^[a-z0-9_-]{1,64}$`;
    - `template` is one of `project|finance|clients|people|ideas|research|marketing`;
    - `source` is one of `linear_team:<KEY>|company_finance|company_customers|roster|research|github:<owner/repo>`.
  - It upserts rows at daemon start.
- There are 12 files: `water`, `kevin`, `yt-recamendo`, `crawler`, `voice-text`, `econ-rag` (template project, source `linear_team:WAT|KEV|YTR|CRA|VOI|ECO`), and `finance`, `clients`, `people`, `ideas`, `research`, `marketing`.
- Each file declares **deterministic membership rules**, not guesses: `projects: [...]`, `teams: [...]`, `decision_types: [...]`, `clients: all|[...]`, `vendors: [...]`.
- `recordlinks.InWorkspace(ctx, st, rec)` writes `in_workspace` edges from those rules (reusing `LinkInWorkspace`, U21), through the existing `store.AddLink` (idempotent). A record may match several workspaces.
- The first `for_project` writer: a Linear issue → project via its identifier prefix → `teams.linear_key` → the team's projects. A roster client → a project through `clients.product`.
- New package `internal/dashboards` (spec only, no compute): it loads `twins/ceo/dashboards/{finance,delivery,clients}.yaml`. A spec has `source`, `metrics: [3 ids]`, `breakdown: id` and `callout: id`, and **every id must be in a closed Go registry of compute functions**. YAML carries no numbers and no expressions.

**1b. Approvals metadata and trail** (migration 0017, second half).
- `approvals` gains these columns:
  - `origin_kind TEXT NOT NULL DEFAULT 'agent_draft'` (`agent_draft|person_request`);
  - `requested_by`, `kind` (`email|money|signature|flag|message`), `source_card_id`, `priority` and `deadline`;
  - `thread_ref`, `sent_at`, `replied_at`, `reply_ref` and `provenance`.
- `approvals.Envelope` gains the matching JSON fields, **outside `PayloadHash`** (`queue.go:104-109`).
- `kind` is derived at `Propose` from a code table keyed by action (`gmail.send_message` → email, `requests.respond` → from the request, `linear.set_issue_priority` → flag).
- New `Queue.ProposeRequest` for person requests, per U15. It adds the new internal connector `internal/connectors/requests` (level A `respond`, sends nothing) and its twin.yaml grant.
- **The trail is derived, not stored:** `staged`=pending → `approved` → `sent`=executed with `sent_at` → `reply` when `replied_at` is set.
  - `decideAndExecute` stores the Gmail `threadId` from the send result into `thread_ref`.
  - Gmail sync and the agentmail watcher both call the new `store.MarkApprovalReplied(threadRef, msgRef)` when an inbound message's thread matches.
- **TTL:** `provenance='demo_seed'` and `origin_kind='person_request'` envelopes use a new `approvals.RequestTTL` (7 days). Agent drafts keep 15 minutes (finding 21).

**1c. Decision records and card fields** (migration 0018).
- New tables:
  - `decision_records(card_id PK, card_json, provenance, simulated, created_at)` (U13);
  - `card_evidence_extra(card_id, source, text, untrusted, added_at)`;
  - `card_action_states(card_id, action_id, status, approval_id, staged_at)`, which replaces the one-envelope `card_states` for staging. The old table is kept for dismiss.
- `decisions.Card` gains:
  - `TeamSignal []Signal{Person, Status string; Simulated bool}`;
  - `ActionSuggestions []Suggestion{ID, Icon, Sentence, Function string; Payload map[string]any; Actionable bool}`;
  - `Evidence.Kind` (icon: `money|customer|issue|mail|calendar|research`).
- `Suggestion.Sentence` is **code-built** from a per-function template ("Email the client proposing a 3-month extension" comes from the seeded record; computed cards use "Send an email", "Change an issue's priority"). It never contains an address.
- `ActionSuggestions` for computed cards derive 1:1 from `StagedActions`, with `ID = sha(card_id+index)`.
- `Options` stay in the model.
- New `decisions.Merge(computed, records, extra)` is called by `GET /v1/decisions` and `needsyou`.

**1d. Ideas, research runs, provenance flags** (migration 0019, per U2-A).
- `ideas(id, title, gist, stage raw|explored, provenance, simulated, created_at)`.
- `idea_evidence(idea_id, source, label)`.
- `research_runs(id, idea_id, topic, status queued|running|finished|failed, attached_card_id, report_text, untrusted=1, provenance, created_at, finished_at)`.
- `research_steps(run_id, n, label, status, source_count)`.
- Normalized tables (messages, events, issues, …) get **no** Meta change. A side table `record_flags(table_name, source, source_id, provenance, simulated)` flags seeded rows (finding 16).
- New tables carry `provenance`/`simulated` natively.

**Endpoints:** none. Phase 1 is data only.

**Tests:**
- The workspace loader rejects a bad id, template or source, and all 12 files load.
- `InWorkspace` writes the expected edges, writes nothing for a record no rule matches, and adds no duplicate when re-run.
- The dashboard loader rejects an unknown metric id or a numeric literal field.
- Approvals:
  - the metadata round-trips;
  - **`PayloadHash` is unchanged** by the metadata;
  - `kind` is derived correctly;
  - `ProposeRequest` executes `requests.respond` through `Gate.Invoke` and nothing else;
  - the trail goes staged → approved → sent → reply when a fake inbound message on `thread_ref` arrives, through both the Gmail and agentmail paths;
  - `RequestTTL` applies only to requests and seeds.
- `decisions.Merge`: the record wins over a computed card with the same id, and extra evidence is appended and tainted.
- `card_action_states`: two actions on one card stage independently.
- `record_flags` queries.

**Commits:** "UI-1a: workspaces, in_workspace/for_project writers, dashboard specs"; "UI-1b: approval origin_kind/kind/trail and person requests"; "UI-1c: decision records, team signal, action suggestions, per-action state"; "UI-1d: ideas, research runs, provenance flags".

### Phase 2 — navigation (under 1 session)

- **Prep task (do first, it unblocks parallel work):** split `app.js` (1142 lines) into flat per-view files: `view_today.js`, `view_decisions.js`, `view_approvals.js`, `view_drafts.js`, `view_meetings.js`, `view_threads.js`, `view_dashboards.js`, `view_workspaces.js`, plus a slimmer `app.js` router.
  - Flat names pass `webui.contentTypes` and Swift `isAssetName` (one dot, `[A-Za-z0-9_-]` stem). CSP `script-src 'self'` covers them.
  - `index.html` loads them with `defer`.
  - Confirm that the forbidden-API scan walks every file from `webui.Files()`. If it lists files explicitly, extend it.
- `index.html`: the nav order becomes Today, Decisions, Approvals, Drafts, Meetings, Threads; then a divider, Dashboards, and Workspaces (a disclosure listing workspaces from the API). Recent threads and the bottom bar are unchanged.
- The hash router gains an optional second segment: `workspaces/<id>/<sub>`.
- Swift `pageViews` gains `dashboards` and `workspaces`. Two-segment opens are validated segment by segment with `isID`.

**New endpoints:**

| Route | Handler | Registration |
|---|---|---|
| `GET /v1/workspaces` | new `gateway/workspaces.go` (id, name, template, source) | Mux; `api.js` `workspaces()`; `webui_test.go` `allowed`; Swift `routes` + test |
| `GET /v1/dashboards` | new `gateway/dashboards.go` (id, name, source label) | the same four |

**Tests:**
- The Go allowlist pin is updated.
- Handler tests: the list order and fields match the YAML.
- Swift `WorkspaceAllowlistTests`: allow cases for the two routes and the new views; a two-segment open with a bad segment is denied.
- The forbidden-API scan covers the new files.

**Commit:** "UI-2: nav, per-view scripts, workspace and dashboard lists".

### Phase 3 — the six views (4 sessions: 3a–3d)

**3a. Today and Approvals.**
- Today rows show:
  - the priority edge;
  - a kind icon (Unicode glyph; no images, no `url()`, no SVG unless `dom.svg` is approved separately);
  - the title;
  - one origin line built server-side as `Item.Origin` ("From Meridian renewal", "Requested by Lee", "Built from an outside email").
- A row opens its card (`decisions/{id}` or `approvals/{id}`; the native deep links stay stable).
- The Approvals queue:
  - dense rows sorted client-side by priority, then deadline;
  - the agent-draft card: "From <decision>" via `source_card_id`, subject plus a one-sentence gist (the first sentence of the body, code-cut), and risk in words from a code table ("Medium risk · external recipient, new commitment");
  - Approve… → read-back → Yes (U11), Edit, Reject;
  - the trail;
  - **W's `warnings` as an amber box** (finding 23).
- The person-request card: "Requested by <name>" with an initials avatar, and "Request changes" with the caption "Sends your notes back to <name>". It opens an inline note field.

**3b. The decision card.**
- The header is the title plus a due badge, with the question underneath.
- The left column has up to 3 evidence lines, each with its icon and no inline source. The right column is a "Team signal" box with initials and a "(simulated)" suffix where flagged.
- "Recommendation:" is one line. The muted "Missing info" line follows (U12).
- Suggestion rows show icon + sentence + Review.
  - Review → the existing `stageForm` for **that action id** → read-back → confirm.
  - The recipient and payload appear only after Review.
- "View related data (N)" is full width, with N = `len(SourceItemIDs) + evidence sources`. It opens a panel listing each source. Its links follow U16.

**3c. Drafts** (U10-A, migration 0020).
- A `drafts` table: `id, template, to, subject, body, source_card_id, provenance, simulated, updated_at`.
- The rows are tagged Reply, Delegation or Investor-update section.
- The editor has Save and "Send for approval" (→ `Queue.Propose(gmail.send_message)`, recipient checks included). Draft bodies are plain text in a `textarea`.

**3d. Meetings and Threads** (migration 0021).
- `meeting_sessions` gains `recap_signals TEXT` (JSON of `RecapSignals`) and `project_guess` (id, confidence), written at recap-on-stop.
- The daemon wires a classifier into `workspace_meetings.go:52`. It is the existing `ModelClassifier` over the subscription CLI and gated. It is labelled "likely: X (medium)", never auto-filed.
- Upcoming meetings come from the `events` table (`?upcoming=1`).
- The recap shows Decisions made, Action items (owner avatar via the roster, text-matched on the owner name only when exact), Open questions and FYI.
- Threads: `threadView` gains `anchor_label`, and anchor types gain `project`, `workspace` and `idea`, each with a code-built snapshot and correct taint. "free" becomes "Unanchored".

**New endpoints:**

| Route | Sub-phase | New suffix |
|---|---|---|
| `POST /v1/approvals/{id}/request-changes` `{note}` → denies the original with reason "changes requested", then proposes a `gmail.send_message` to the requester's roster address. 409 if there is none. | 3a | `'/request-changes'` |
| `POST /v1/decisions/{id}/actions/{action_id}/stage` (replaces the function-keyed stage; the old route is kept for one release) | 3b | `'/stage'` (exists); the path gains `actions` |
| `GET /v1/decisions/{id}/related` | 3b | `'/related'` |
| `GET /v1/drafts`, `GET /v1/drafts/{id}`, `POST /v1/drafts/{id}` (save), `POST /v1/drafts/{id}/submit` | 3c | `'/submit'` |
| `GET /v1/meetings?upcoming=1` (a Swift `queryKeys` change) | 3d | — |

Each one is registered in the Mux, `api.js`, `webui_test.go` and Swift `routes` + a test.

**Tests:**
- Today: an `Item` carries `Origin` and `Priority`.
- Approval sort order (a pure JS-free Go helper, or a documented client rule).
- Warnings rendered: the `webui_test` scan requires a `warnings` reference in `view_approvals.js`.
- `request-changes`: denies plus proposes, never executes, and returns 409 when there is no address.
- Per-action staging: two actions stage independently, and a second stage of the same action returns `already_staged`.
- `related` lists only the card's own sources.
- Drafts: save round-trips; submit creates exactly one pending envelope through `Propose`; a recipient-check refusal is a 422; **submit never executes**.
- Meetings: recap signals persist; the guess is labelled and never files a link.
- Threads: new anchor types with taint copied; the "Unanchored" label.
- Swift allowlist cases for every route.

**Commits:** "UI-3a …", "UI-3b …", "UI-3c …", "UI-3d …".

### Phase 4 — dashboards (1–2 sessions)

- `internal/dashboards/compute.go` has the closed registry of metric, breakdown and callout functions. Each calls connectors **only through `Gate.Invoke` at a low origin**, following commit 4a47519's reserve-P0-headroom pattern.
  - A `Cache` with a 10-minute TTL fronts every source call.
  - Every value carries a tile state: `ok | not_connected | unavailable | illustrative`.
  - `not_connected` comes from the gate's "credential … unavailable" denial.
- **Finance** (`company_finance`):
  - cash, burn and runway from `cash_position`;
  - spend by application from a new read variant `spend_breakdown` with no application argument, returning all rows (U4 sub-question);
  - callout 1: the largest `spend − revenue` gap;
  - callout 2: a month whose cost is more than 1.5× the median, from a new R function `monthly_costs` (Monthly P&L range).
- **Delivery** (Linear): the `list_issues` query gains `team{key}`, `state{type}`, `dueDate`, `relations`/`inverseRelations` and `labels`. The counts are open (by `state.type`), blocked (has an inverse `blocks` relation that is open) and urgent (`priority=1`). The breakdown is by team key. The callout is the issue that blocks the most open issues.
- **Clients** (`company_customers`, U3): accounts at risk, open tickets and NPS; accounts by health. The callout is the account with the longest time since contact **and** money outstanding, joining finance `outstanding_invoices` by account name. Days are computed from the clock (U9).

**New endpoints:** `GET /v1/dashboards/{id}`, registered in the Mux, `api.js`, `webui_test.go` and Swift + a test.

**Tests:**
- Fake connectors with fixture results produce **exact** computed numbers, which is the brief's "computed from connectors" item.
- A scan test: dashboard YAML and `compute.go` contain no hardcoded metric values (no numeric literals outside thresholds named in code).
- A missing credential gives the `not_connected` state and no number.
- Cache: N renders make at most one gated call per source per TTL, and no call happens at P0.
- The callout logic works on edge fixtures (ties, empty).

**Commit:** "UI-4: finance, delivery and clients dashboards".

### Phase 5 — workspaces (3–4 sessions: 5a–5d)

A workspace view is the workspace's filtered existing sections, found via `in_workspace` edges, plus its control-room tiles. **Controls may start a research run, open a draft or open a thread. Nothing creates a decision.**

- **5a. Project, Finance, Clients.**
  - Project:
    - a progress bar (the project's `TargetAt` plus the share of the Linear team's issues done), drawn with `<progress>`/`<meter>` after adding `max`, `min`, `low`, `high` and `optimum` to `dom.js` `SAFE_ATTRS` (**never `style`**);
    - open PRs, merged this week and blocked issues;
    - a "Building now" list (in-progress issues);
    - 4-week commit bars from a new R function `github.list_commits`;
    - the PR and commit tiles are **"Connect GitHub"** when the workspace has no `github:` source or the call returns `not_connected`;
    - below: Needs-you rows, recent meetings and threads.
  - Finance: the Finance dashboard, invoices with aging, and vendor renewals from roster `vendors`.
  - Clients: accounts with health, days since contact, tickets, revenue (Finance) and owner (roster `owns_client`).
- **5b. People.**
  - A resource bar: budget left, hours this week, and people with free capacity from roster allocations.
  - Team tiles: border by strain, initials coloured by load, a load bar and an overdue count. Load comes from Linear assignee counts plus allocation.
  - A cross-team callout when the same people are on deadlines within 7 days of each other.
  - Buttons: "Message a team" and "Send pulse check" **create drafts** (Phase 3c); "Policies" opens a read-only document.
  - **Morale comes only from load, overdue work and pulse answers people chose to give.** A test pins that the people-strain compute function takes no message text.
- **5c. Ideas and Research.**
  - Ideas: a capture bar (a POST body, never a query); Raw and Explored groups; Start research (queues a run); Discuss (anchors a thread, type `idea`); Propose (creates a draft).
  - **The research runner:**
    - It runs one run at a time in a daemon goroutine.
    - Each run has **5 fixed, code-built steps**: overview, market, competitors, pricing, risks. Each step is one `research.web` call through `Gate.Invoke` at origin P2 with a code-templated query (`"<idea title> <facet>"`). W's exfiltration guard still applies, and the calls stay within its 20/h cap.
    - Step status goes to `research_steps`, so "3 of 5" is real.
    - The report is marked untrusted.
    - This is subscription only, and it refuses to run when `Available()` reports metered (W invariant 5).
  - Research columns: Queued, Running, Finished. A card shows its steps. A finished run shows "In <decision>" or Attach, which inserts only into `card_evidence_extra`.
- **5d. Marketing.**
  - Trend tiles name the research run their data came from. With no run they are labelled "illustrative".
  - Prospects (customers sheet) and public reviews get "Draft outreach" and "Draft reply" (drafts).
  - A capacity note appears when the design lead's computed allocation is ≥1.0.

**New endpoints:**

| Route | Sub | New suffix |
|---|---|---|
| `GET /v1/workspaces/{id}` | 5a | — |
| `POST /v1/workspaces/{id}/drafts` `{kind: team_message\|pulse_check, team}` | 5b | `'/drafts'` |
| `GET /v1/ideas`, `POST /v1/ideas`, `POST /v1/ideas/{id}/research`, `POST /v1/ideas/{id}/propose` | 5c | `'/research'`, `'/propose'` |
| `GET /v1/research/runs`, `GET /v1/research/runs/{id}`, `POST /v1/research/runs/{id}/attach` `{card_id}` | 5c | `'/attach'` |

`POST /v1/threads/anchor` already exists and gains the `idea`/`workspace`/`project` types from 3d. Each route above is registered in the Mux, `api.js`, `webui_test.go` and Swift + a test.

**Tests:**
- **No workspace control can create a decision** (the brief's acceptance item). A table test enumerates every route under `/v1/workspaces`, `/v1/ideas` and `/v1/research`, invokes each with valid bodies against a spy store, and asserts:
  - zero writes to `card_states`, `card_action_states`, `decision_classifications` and `decision_records`;
  - no `Trigger.Run`.

  A static check also asserts that those handlers' files don't reference `decisions.Trigger` or `store.SetCardState`.
- **Connect GitHub:** a project workspace with no `github:` source, and one whose call is denied `not_connected`, both return the tile state `not_connected` and no number.
- Every workspace number comes from fixture connectors, with exact values.
- Runner:
  - 5 steps run through the gate at P2;
  - it stops on a denial;
  - it refuses when the backend is metered (the fake reports `Metered`);
  - the report is untrusted.
- Attach touches only `card_evidence_extra`, and the other card fields are byte-identical.
- The morale function signature takes no message text.
- The new `SAFE_ATTRS` still refuse `style`.

**Commits:** one per sub-phase.

### Phase 6 — voice and the Activity HUD, restated (1 session, mostly verification)

Per finding 1 and U1, "the HUD" means the globe and the glass tab.

- **Keep, not rebuild:**
  - Parakeet STT;
  - Kokoro via FluidAudio (restating that kokoro-mlx is superseded);
  - the download consent and progress window;
  - the shared turn pipeline;
  - notifications (`Notifier.swift`);
  - `docs/CONTEXT.md:128-130`, which already records "no context-accumulation layer".
- **Restate** "push-to-talk from any app" as "⌃⌥V voice mode from any app (Accessibility granted), then hold Space" (V §9). No second hotkey.
- **Finish** (coordinated with the build that owns it, finding 1): globe drag, dock and menu wiring in `GlobeHUD.swift` (`panel.ignoresMouseEvents`, `place()` reading `GlobePrefs`).
- **U1-A:**
  - `runtime.EventArtifact` gains type `note` `{title, body, source, simulated}`, emitted from `gateway/artifact.go` only for tool results flagged `relay: true`. That flag is the Linear `create_comment` result when the body carries the code prefix.
  - Swift adds `GlassItem.Kind.note` with a "(simulated)" badge, and the globe look for announcing tools (the `searchTools` pattern).
- **Notifications:** Phase 0a lands first, because the two live delivered notifications (`card-4103a2fb…`, `card-7ce4715d…`) are likely the Academia false positives.

**Endpoints:** none.

**Tests:**
- An NDJSON capture test: a tool-using turn emits a `tool_start`/`tool_end` pair, and a Tier-0 lookup emits none.
- The `note` artifact is emitted only for relay results, and `simulated` survives into Swift decoding.
- `GlassTabTests` covers the note kind.
- The existing `DecideBound` tests cover spoken yes and click resolving the same envelope. Add one for the W two-step on `gmail.send_message`.

**On-device verification owed** (goes to `UI-verification.md`):
- banner, tap and second-tap reuse;
- glass-tab Approve against spoken "yes → confirm send";
- Kokoro first-audio latency;
- the first hold after launch.

**Coordinator action:** set `router.voice_approve.internal_domains` if calendar voice approvals are in the demo. Builders don't touch `~/.water`.

**Commit:** "UI-6: note artifact, globe tool look, restated voice acceptance".

### Phase 7 — demo population (2–3 sessions; starts only after 0–6 are verified)

- **`water demo seed [--today=YYYY-MM-DD]` and `water demo reset`** live in new `internal/cli/cmd_demo.go` and `internal/demo`.
  - Seed data is in `twins/ceo/seed/demo/*.yaml`. **Every record carries a `source:` citation** (sheet!cell, a Linear identifier, a `people.yaml` line, a customers-sheet row, or `simulated`).
  - The loader refuses a record that has no citation.
  - The seed writes `provenance='demo_seed'` into the new tables and `record_flags` for normalized rows.
  - `reset` deletes exactly the flagged rows, and runs in one transaction.
  - Dates are relative to `--today` (U9).
- **`twins/ceo/seed/demo_routing.yaml`** holds:
  - a name → `+` alias map (Elena Park → `<owner>+elena@gmail.com`, and so on);
  - the SMS gateway address (U6-A);
  - the Linear issue allowlist (U4);
  - the demo calendar id (U20);
  - the Today allowlist for real items.
- **`demo.enabled` config:**
  - Today shows `demo_seed` items plus allowlisted real items only.
  - Recipient enforcement uses both layers (U18).
  - The read-back names the resolution (U17).
- **Seeded content:** the main story, scenarios A–I, the three drafts, three meetings with recaps, and three threads. They are resolved per U7 and U8.
- **Scenario D:**
  - An obviously fake sender, `invest0r-relations@totally-legit.example`.
  - The message is untrusted, and the seed contains **no staged action, draft or approval payload** for it.
  - No investor name from Funding & Cap Table appears anywhere in the seed.
- **The runbook** `docs/slices/UI-demo-runbook.md`: steps 1–16 with exact utterances, expected outputs, and the U-decisions they depend on.

**Endpoints:** none new. Demo mode reuses the existing routes.

**Tests:**
- **seed/reset isolation**, against a temp store holding non-demo rows in every affected table: seed adds only flagged rows, reset restores a byte-identical dump of the non-demo rows, and seed → reset → seed is idempotent.
- **Demo sends go only to aliases:** with demo on, `Propose` of `gmail.send_message` to `x@example.com`, to an unlisted address, or with an allowlisted `to` plus an unlisted `cc` is refused. A pre-approved envelope to an unlisted address is denied at `Gate.Invoke`.
- **Scenario D cannot send:** no seeded envelope, draft or suggestion references the D message; staging from D's card is refused (no `ActionSuggestions`); and a model-proposed forward of it to any address fails the allowlist.
- **Every simulated item says "(simulated)":** each rendered view model with `simulated=true` has the suffix (team signals, notes, replies, requests), and the relayed Linear comment body starts with the code prefix.
- **Demo Today shows no personal mail:** a fixture inbox with non-allowlisted real messages is filtered.
- Every seed record carries a citation (loader test).
- The `--today` arithmetic reproduces the story's numbers for the chosen date.

**Verification:** a live run of steps 1–16 against the owner's accounts, **only after** the owner explicitly approves live sends to the aliases (WORKFLOW.md Phase 4), with evidence in `docs/slices/UI-verification.md`.

**Commits:** "UI-7a: demo seed/reset, routing, gate rule"; "UI-7b: story and scenario data, runbook".

---

## 4. Invariants kept

Each is covered by the existing guard tests plus the tests named above:

1. **The gate is the only execution path.** Every new write goes through `Queue.Propose` → envelope → `decideAndExecute` → `Gate.Invoke`: request changes, draft submit, Linear comment and priority, person-request respond, and the research steps. Dashboards and workspaces read only through `Gate.Invoke`.
2. **Nothing sends without an approved envelope.** Drafts, pulse checks, team messages, Propose, outreach and SMS all create envelopes; none executes. Two clicks stay (U11), and W's two-step spoken confirm stays. The demo gate rule only *adds* refusals.
3. **Untrusted rendering.**
   - All DOM goes through `dom.h` with `textContent`.
   - `SAFE_ATTRS` grows only by the meter/progress numeric attributes, never `style`, `href` or `src`.
   - Glyph icons, no images.
   - `dom.untrusted` quoting and every taint field stay.
   - Relayed and research text is tainted, and actions derived from it never run autonomously.
4. **CSP unchanged** (`webui.go:37`, `TestCSPIsStrict`, meta/header match). The page never calls `/v1/turns`, `/v1/tools`, `/v1/quick`, `/v1/twinlink`, `/v1/notifications`, `/v1/intents`, `/v1/state` or `/email`. External URLs never exist in the page (U16).
5. **Zero metered spend.**
   - Research and the meeting classifier use the subscription CLI only, gated and rate-capped.
   - No Twilio, and no blandcall inside the product (U6).
   - Tests use `backend.Fake`.
6. **No new dependencies.** Go stdlib only (no cgo), no Swift package changes, and no chart or icon libraries.
7. Reflex tiers stay subprocess-free, the audit log verifies, and no secret appears in logs, errors or model context.
8. Card invariants hold: no model writes a number, source or readiness label, and gaps are never hidden (U12).
9. Never-guess links: `in_workspace` and `for_project` come from declared rules only.

## 5. Risks and open questions (each with a default)

- **Scale.** This is about 14–18 sessions (§6). Default: one sub-phase per session, with the owner reviewing after Phase 3a so the visual design is confirmed early.
- **Concurrent edits.** Another build owns `clients/macos` (globe, glass), `internal/connectors/research` and `twins/ceo/role.md`. Default: P0 commit first. Swift route registrations are batched into one stream per phase (§6), which re-reads the files right before editing and uses `Edit` only.
- **The live data may never match the story** (two finance sheets, Linear not connected, the Oct 24 trial end). Default: tiles show `not_connected`/`unavailable` honestly, and Phase 7 is blocked, not faked, until U5 is done.
- **Rate caps under demo load.** Dashboards and workspaces multiply Linear and finance calls. Default: a 10-minute cache at a low origin. Add the new functions' caps in twin.yaml at the same 60/h.
- **The meeting classifier adds model calls on recap.** Default: one call per recap, gated. It is off if `decisions.card_ttl_seconds`-style caching shows pressure.
- **The 7-day `RequestTTL` changes the expiry posture** for person requests and seeds. Agent-drafted outward sends keep 15 minutes. Default: accept this.
- **The approval trail's reply detection** relies on Gmail thread ids from the send result. Default: if the result lacks one, the trail stops at "sent" and a known gap is logged.
- **The noise filter could hide a real request** that arrives through a mailing list. Default: header or label signals give `noise` only when the sender's domain was never written to. Otherwise they give `informative`, which still shows in the brief. The reasons are logged in the verdict for review.
- **The demo gate rule is the gate's first argument-based rule** (U18). Default: it is active only when `demo.enabled` is set, and is covered by tests in both states.
- **`app.js` split churn** (Phase 2) may collide with the hotfix history. Default: do it as a pure move with no behaviour change, in its own commit.

## 6. Sizing and parallelization

| Phase | Sessions | Go (approx. lines) | Web (approx. lines) | Swift |
|---|---|---|---|---|
| 0 | 1 | 400 | 150 | none |
| 1 | 2 | 1500 | none | none |
| 2 | ≤1 | 150 | 300 (+ split) | ~20 |
| 3 | 4 | 1200 | 1200 | ~60 |
| 4 | 1–2 | 900 | 300 | ~10 |
| 5 | 3–4 | 2500 | 1500 | ~60 |
| 6 | 1 | 150 | none | ~150 |
| 7 | 2–3 | 1200 + seed YAML | 50 | none |

**Streams per phase, with disjoint file ownership for a multi-agent build.** The **integrator** (I) stream owns, in every phase:
- `gateway/daemon.go` `Mux()`;
- `api.js`;
- `internal/gateway/webui_test.go`;
- `WorkspaceAllowlist.swift` + `WorkspaceAllowlistTests.swift`;
- `twins/ceo/twin.yaml`;
- the phase's migration file.

Feature streams publish route and schema contracts, and I lands them in one pass at the end. Nobody but I edits those files.

- **Phase 0:**
  - **S-a** (triage): `internal/mailnoise/`, `internal/decisions/attention.go`, `trigger.go`, `internal/runtime/brief.go`, `gmail.go` + tests, `store/records.go`, `cli/twin.go` (`buildDecisionsTrigger` only).
  - **S-b** (chips): `internal/needsyou/`, `app.js`, `app.css`, `dom.js`, `webui_test.go` (the pill scan, handed to I if I is active).
  - I: `0016`.
- **Phase 1:**
  - **S-ws**: `internal/workspaces/`, `internal/dashboards/` (spec), `twins/ceo/{workspaces,dashboards}/`, `internal/recordlinks/`, `store/workspace_records.go`.
  - **S-appr**: `internal/approvals/` (not `recipientcheck.go`), `store/approvals.go`, `internal/connectors/requests/`, `gateway/actions.go` (trail capture), `internal/agentmail/watcher.go` (the reply hook).
  - **S-dec**: `internal/decisions/{card,merge}.go`, `store/decision_records.go`, `store/card_states.go`.
  - **S-idea**: `store/ideas.go`, `store/research_runs.go`, `store/record_flags.go`.
  - I: `0017`–`0019`, twin.yaml.
- **Phase 2:** a single stream (the split must land before anything else); I for the registrations.
- **Phase 3:**
  - **3a** `view_today.js`, `view_approvals.js`, `needsyou`, `gateway/workspace_approvals.go`;
  - **3b** `view_decisions.js`, `gateway/workspace_decisions.go`;
  - **3c** `view_drafts.js`, new `gateway/drafts.go`, `store/drafts.go`;
  - **3d** `view_meetings.js`, `view_threads.js`, `gateway/workspace_{meetings,threads}.go`, `meetings/`.
  - `app.css` is split into per-view sections, each owned by its stream, and I merges them.
  - They can run in parallel once Phase 1 has landed.
- **Phase 4:** **S-fin** (`gsheets` read variants, finance compute), **S-del** (the `linear.go` query, delivery compute), **S-cli** (the customers connector per U3, clients compute), and **S-ui** (`view_dashboards.js`, `gateway/dashboards.go`).
- **Phase 5:** 5a–5d as four streams: `view_workspaces_{project,finance_clients,people,ideas_research,marketing}.js`, their gateway files, and `internal/research/runner.go` (5c). `github.go` `list_commits` goes to 5a only.
- **Phase 6:** Swift (`clients/macos`, in coordination with its current owner) and Go (`runtime`, `gateway/artifact.go`).
- **Phase 7:** **S-cmd** (`internal/demo`, `cmd_demo.go`, the gate rule in `gate.go`, `recipientcheck.go` demo mode) and **S-data** (the `twins/ceo/seed/demo/` YAML plus the runbook).

**Dependencies:** P0 → 0 → 1 → 2 → {3a, 3b, 3c, 3d} → 4 → {5a–5d} → 6 → 7. 4 depends on U3/U5 for live numbers only; its fixture tests don't. 6 can run in parallel with 3–5 once U1 is decided.

## 7. What happens next

**Stop for owner approval.** Nothing in §3 starts until the owner does three things:
1. Approves or edits this plan.
2. Answers U1–U23. At minimum, U14 and U21 for Phase 0, and U2, U13 and U15 for Phase 1.
3. The coordinator commits the pending V/W backlog (P0).

Phase 0 can start as soon as U14 and U21 are answered. Phase 7 also waits for U3–U9, U16–U20 and the owner's connection actions (U5). Each sub-phase ends with its own commit and an `EVOLUTION_PLAN.md` update by the coordinator.
