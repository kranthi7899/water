# Demo sequence: Meridian / Lexicon / bug-to-client arc

Owner-provided (2026-09-26), saved verbatim apart from obvious paste typos.
The build plan that implements it is `docs/slices/UI.md` (Phase 7).

One connected story, told through voice then the interface. Everything traces back to one root
cause: the Crawler dedup bug (CRA-3), which is both an open customer ticket (SUP-305) and the
blocker on the Meridian renewal (YTR-6).

## Two open decisions before building

1. **Nina's reply.** No live chat connector exists yet, so it's simulated: the agent posts its
   own message on CRA-3, then a second comment marked as relayed ("Nina, via Slack: ...") standing
   in for a future real connector. The exchange also appears live in the voice Activity HUD as a
   job step and a promoted note, then persists in Linear for the interface walkthrough.
2. **The phone call.** Not built yet, and outside the no-paid-telephony rule. Options: use
   Twilio's free trial credit for this one demo call, or replace it with a free carrier
   email-to-SMS text to the same effect.

## Sequence

| # | Phase | What you say / do | What it shows |
|---|---|---|---|
| 1 | Voice | "What's up?" | Nothing pending — doesn't invent urgency |
| 2 | Voice | "What's on my calendar?" | Ranked by priority; Meridian risk surfaces |
| 3 | Voice | "What's our sales report?" | Pulls Revenue; flags Meridian and Lexicon |
| 4 | Voice | "Give me today's brief" | Linear + calendar combined |
| 5 | Voice | "How's our client side going?" | Surfaces SUP-305, ties it to Meridian unprompted |
| 6 | Voice | Agent: "Let me check with Nina" | HUD shows the step live; simulated reply lands as a promoted note |
| 7 | Voice → approval | Agent drafts the client update to Elena Park | Approve; sends to your own inbox, watch it land |
| 8 | Voice → approval | "Should we renew Lexicon?" | Small, fast decision; approve; a note goes out |
| 9 | Interface | Switch over, tour Today / Decisions | First look at the full UI |
| 10 | Interface | Click into the Meridian decision | Zoom: revenue, NPS, the CRA-3 timeline gap, linked people |
| 11 | Interface | Open CRA-3 in Linear | The Nina exchange from step 6 is already there |
| 12 | Interface | "How are we doing financially?" | The finance dashboard |
| 13 | Interface | Approve the Meridian extension | CRA-3's priority visibly changes in real time |
| 14 | Interface or voice | Phone or text beat | Client-facing update delivered to your own number |
| 15 | Live | Join the Zoom call, agent listens | Framed correctly: listening in the background, not a bot joining |
| 16 | After the call | "Give me a recap, and a report on the build" | Meeting recap + a build report, sent out |
