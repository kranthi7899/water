# Slice M: live-meeting assistance

**BLOCKED on Slice B and Slice C-base.** The owner's sequencing: A4 (done)
→ B (text pop-up first, voice second) → C-base (`docs/slices/C.md`) → M
(this spec). M needs B because it is fundamentally a Swift-client feature
(audio capture, on-device STT, the pop-up panel), and it needs C-base
because the after-meeting recap's project/decision matching reuses the
same "is this item a decision, and if so what kind" machinery C-base
builds (`internal/decisions`). This document is the spec to build against
once both ship; it authorizes no code yet.

## Goal

A twin that is attentive during a meeting and helps the CEO in real time,
privately, without anyone in the meeting knowing it's running beyond the
CEO themself. The after-meeting recap is a by-product of the same live
transcript captured for the on-demand help feature — there is no separate
dependency on a Zoom/Meet transcript connector (that stays a Slice D
concern, deferred).

## Architecture split

**The daemon never receives audio. Audio is never written to disk.** This
is the load-bearing privacy property of the whole slice, and it constrains
every component below:

- The **Swift menu-bar client** (built in Slice B, extended here) captures
  system audio and the microphone locally and runs on-device speech-to-text
  (macOS `SFSpeechRecognizer`, on-device mode — the same free, no-network
  engine `docs/CONTEXT.md`'s constraints already commit Water to for voice
  generally). It tags each recognized segment by channel: microphone is the
  CEO, system audio is everyone else.
- The client sends **timestamped text segments only** to the daemon over
  the local API (new endpoint, following the existing `POST /v1/...`
  convention in `internal/gateway/daemon.go`'s `Mux()`:
  `POST /v1/meetings/{id}/segments`, body `{at, channel, text}`).
- The daemon holds a **rolling transcript buffer** per active session (an
  in-memory ring plus a `store.Meeting`-linked persistence path — the
  `Meeting` record type already has `TranscriptRef`/`Summary` fields from
  A1's schema, unused until now) and marks **all of it as untrusted
  external content**: every segment, regardless of channel, sets the same
  `External: true` / taint-escalation path A3 already wires for connector
  output (`gateway.handleToolInvoke` → `escalateTaint`). Words spoken in a
  meeting — including the CEO's own, since a hot mic can pick up anyone
  near it — can never instruct the twin to act; they can only be quoted,
  searched, and summarized.

## Build

### 1. Meeting session

Start and stop by hotkey (Slice B's global-hotkey mechanism), and
optionally offered from the calendar (a new event starting soon triggers a
client-side prompt, never an automatic start — consistent with "manual
start and stop" in the consent section below). The client shows a clear
"listening" indicator whenever a session is active, matching the existing
voice-indicator convention (`♪`/`○`) `docs/archive/voice-council-era.md`
described for the old TUI, reused for the new client.

A new daemon-side type, `internal/meetings` (a new package — this is
session lifecycle state, not a connector, not the gate, and not a single
turn's context, so it doesn't fit cleanly into any existing package):

```go
type Session struct {
    ID        string
    StartedAt time.Time
    EndedAt   *time.Time
    EventID   string // store.Event.SourceID, if started from a calendar event
}
```

`POST /v1/meetings/start` (optional `event_id`), `POST /v1/meetings/{id}/stop`.

### 2. Prefetch

When a session starts (or the client offers to start a few minutes before
a linked calendar event), the daemon pulls the event's attendees and any
linked documents (Drive links in the event description/attachments) plus
related recent threads (`gmail.list_messages` scoped to the attendees) and
Drive files into the local store, ahead of the meeting. **All reads go
through the gate at origin R** (a synchronous CEO-adjacent prefetch, not
background P2 drafting — it's triggered by an imminent or active meeting,
which is closer to P1 than P2) — no new gate behavior, this is the same
`gcal`/`gmail`/`gdrive` read functions already at level R in
`twins/ceo/twin.yaml`, called with different arguments (attendee-scoped
queries) than the existing sync loop uses.

### 3. Local text index

Add full-text search over stored message and document text: **SQLite
FTS5**. Confirmed: `modernc.org/sqlite` (the pure-Go driver already in
`go.mod`, v1.59.0) compiles FTS5 into its translated SQLite C source by
default — no loadable extension, no cgo, nothing to add to `go.mod`. A new
migration under `internal/store/migrations/` adds an FTS5 virtual table
(`messages_fts`, `documents_fts`, content-linked to the existing
`messages`/`documents` tables via `content_rowid`) kept in sync by triggers
or by `Store.Upsert` also writing to the shadow table — the existing
migration numbering (`0003_sync_cursors.sql` was A4's) continues
sequentially. Lookups hit local FTS first; a live connector call
(`gmail.list_messages` with a query, `gdrive.search_files`) is a fallback
only when the local index has nothing, exactly the "local first, live as
fallback" order the amendment specifies.

### 4. On-demand help

From the pop-up bar or hotkey (Slice B), the CEO asks things like "pull up
the Kafka budget" or "what did Priya say about compute last time." This is
a **P0 request** (the CEO's immediate ask, preempting background work per
`docs/CONTEXT.md`'s priority model) answered on the **fast model**
(`twin.yaml`'s `models.fast`, currently `haiku`), under the same usage
caps `gate.ModelCall` already enforces. The answer is built from the last
few minutes of the rolling transcript buffer plus the FTS index (section
3), with a live connector call only if both come up empty.

### 5. Private output only

Results render in the pop-up panel; optionally spoken into headphones via
the existing `voice.Provider` interface. **Nothing is ever sent into the
meeting** — there is no mechanism in this slice, anywhere, that writes to
a calendar event, posts to a meeting chat, or otherwise has an outward
effect visible to other attendees. Every action available here is level R
(read) or a private render; nothing in M needs a new gate level.

### 6. Quiet proactive cues (flag, off by default)

When a known project, person, or document is mentioned in a live segment,
show two or three related items (from the local FTS index) in a side
panel. Config-gated (`meetings.proactive_cues: false` default, following
`internal/config`'s existing plain-bool-flag convention), rate-limited
(e.g. at most one cue per N seconds, code-enforced the same way gate rate
caps are). No voice, no interruptions — this is a glanceable side panel
only.

### 7. After the meeting

The recap runs over the closed transcript: decisions, action items with
owners, open questions, FYI — using **the same code path as the fixture
transcripts** (i.e., this is a plain Go pipeline function taking a
transcript and returning structured recap sections, testable against a
written-out fixture transcript with no live session involved, the same
pattern `internal/runtime/brief.go` established for the morning brief:
signals/structure computed or extracted in code and by the model from
text, one `backend.Request` to phrase, never to invent facts outside the
transcript). Any project match uses `internal/decisions.Classify` (or a
lighter sibling — "is this mentioned project a known one" is a narrower
question than "does this need a decision") and is **labeled a guess with a
confidence value**, never silently filed. **Nothing becomes a task until
the CEO confirms** — the recap's action items are drafts (level D:
"prepare, nothing leaves") until an explicit confirmation turns a
confirmed item into a real stored task, mirroring the same "the twin never
decides, only prepares and stages" posture `docs/slices/C.md` states for
decision cards.

### 8. Replay mode for testing

Feed a scripted, timestamped transcript through the **same segments
endpoint** (`POST /v1/meetings/{id}/segments`) so a meeting can be tested
without a real call — a test harness or `water` subcommand posts each
scripted segment at (or compressed relative to) its recorded timestamp.
This is the natural fixture mechanism for:

**Scenario S13** (a scripted meeting): the CEO asks two questions midway
through a scripted transcript (exercising section 4's on-demand help path
against the replayed segments) and confirms two action items afterward
(exercising section 7's recap-then-confirm path). See
`docs/slice-c-planning.md` for the scenario-harness proposal this slots
into, and its note on scenario numbering (no pre-existing numbered
scenario list was found in this repo — S8 and S13 are the amendment's own
numbers, used as given).

## Consent

This is a prototype with no real third parties in the meetings it will be
tested against, so **no consent flow is built now** — no participant
notification, no jurisdiction-aware recording-consent logic, no opt-out
mechanism for a non-CEO attendee. The cheap, structural safeguards stay:
transient audio (never written to disk, never reaches the daemon), only
transcript text crosses the local API, a visible listening indicator, and
manual start/stop only. **`docs/known-gaps.md` records that consent
handling is deferred and must be revisited before any real use** — do not
point this at a meeting with real outside participants until that gap is
closed.

## Deferred

Zoom RTMS, meeting-bot services, speaker identification beyond
microphone-vs-system-audio (i.e., no per-person voice ID within "everyone
else").
