# Demo: showing off the twin without real GitHub/Linear/HubSpot accounts

This is a copy-pasteable walkthrough for showing someone what the CEO twin
does — decision cards, live tool calls, the morning brief — pulling from
more than Gmail/Calendar/Drive, without setting up real OAuth or API keys
for GitHub, Linear or HubSpot.

**This is a demo/showcase feature, not a real integration.** `--demo` (or
`WATER_DEMO=1`) swaps in `twins/ceo-demo/twin.yaml`, which adds three fake,
fully in-memory connectors (`internal/connectors/fake/{github,linear,
hubspot}.go`): canned, clearly-fictional pull requests, issues, tickets,
deals and contacts for a fictional startup ("nimbus-labs/core-api",
"Acme Robotics", "Meridian Ventures", ...). No network call, no OAuth
consent screen, no API key exists for any of the three — ever, including in
tests. A plain `water daemon` (no flag, no env var) is completely unaffected
and keeps loading `twins/ceo/twin.yaml` exactly as before.

Google (Calendar/Gmail/Drive) is still the *real* connector in demo mode —
this showcases the fake connectors running **alongside** real Google data,
not replacing it. Run `water connect google` first if you haven't (see
`docs/google-setup.md`); everything below still works without it, just with
Gmail/Calendar signals reading empty.

## 1. Start the demo daemon

```
water daemon --demo
```

(or `WATER_DEMO=1 water daemon`, if a flag is awkward in your launch
context — e.g. a LaunchAgent env var). Leave it running in this terminal;
run the rest from another one. The startup line prints which twin loaded:

```
water daemon: twin=ceo-demo socket=... cli-token=...
```

Confirm the fake connectors are actually granted, and nothing about the
real path changed:

```
water status --demo
```

```
twin     ceo-demo (CEO twin (demo))
functions gcal.list_events, gmail.list_messages, gmail.get_message,
          gdrive.search_files, gdrive.read_file, github.list_prs,
          github.list_issues, linear.list_issues, hubspot.list_deals,
          hubspot.list_contacts
```

versus, in a normal terminal with no flag and no `WATER_DEMO`:

```
water status
```

```
twin     ceo (CEO twin)
functions gcal.list_events, gmail.list_messages, gmail.get_message,
          gdrive.search_files, gdrive.read_file
```

## 2. Ask it about engineering, live

Every function `twin.yaml` grants at level R becomes a tool the model can
call mid-conversation (`internal/gateway/daemon.go`'s `twinFunctions`) — so
these aren't canned answers, the twin is really calling `github.list_prs`,
`linear.list_issues`, `hubspot.list_deals` while it answers:

```
water ask "What pull requests are open right now, and who's waiting on a review?"
water ask "What's the state of the Checkout Revamp work in Linear? Anything urgent?"
water ask "Any HubSpot deals I should be paying attention to this week?"
```

Expect it to mention things like PR #486 ("Add rate limiting to public
API", changes requested), ENG-142 ("Cart totals off by $0.01...", Urgent,
In Progress), and the Acme Robotics renewal or the Meridian Ventures
deal — all sourced from the fake connectors' seed data, never invented.

## 3. The morning brief, and the investor_request decision card

The morning brief itself (`water ask "what's my day look like"`) still
covers Gmail/Calendar signals and open decision cards exactly as it does
without `--demo` — that computation is unchanged, since it reads only what
is already in the local store. The fake connectors are additive tools for
live asks and for decision-card research (step 4), not a new brief signal
in this slice.

## 4. A real, richly-sourced example decision card

`twins/ceo-demo/decisions/investor_request.yaml` is the fleshed-out
`investor_request` type this slice adds: on top of the shipped stub's Gmail
history and Drive search, it fetches a matching HubSpot deal
(`hubspot.list_deals`) and computes the deal's dollar amount, pipeline
stage and days-to-close in code
(`internal/decisions/investor_request.go`'s
`internal://investor_request.compute_deal_health`) — never a model guess.
It's seeded to match an email whose subject mentions "Meridian" or "Series
B" against the fake HubSpot deal "Meridian Ventures — Series B follow-on
discussion".

To see it: have (or send yourself, from another account) a real Gmail
message with a subject like "Following up on the Meridian Series B" that
also reads like it wants a reply (a question mark, "can you", "following
up", ...) — `internal/runtime/brief.go`'s `needsAttention` heuristic is
what flags an item as a decision candidate in the first place. Once the
daemon's background sync has picked it up (or immediately, if you already
have a real investor thread that matches):

```
water decisions list
water decisions show <id-printed-above>
```

The card's evidence and figures will show `deal_amount_usd`, `deal_stage`
and `days_to_close`, each tagged `[code:investor_request.compute_deal_health]`
— traceable, sourced, never invented — plus the Gmail history and Drive
search results, and `Untrusted: true` (a HubSpot deal and Gmail content are
both someone else's data). If no matching deal or history is found, the
card still renders, honestly marked `missing_info` with each gap stated,
never silently dropped.

No matching email handy? `budget_request` and `inbound_decision` both load
in demo mode too (unchanged from production) and will show up in
`water decisions list` the same way any budget ask or inbound request
already does without `--demo`.

## 5. Back to reality

```
water daemon
```

with neither `--demo` nor `WATER_DEMO` set loads `twins/ceo/twin.yaml`
again: no `github`/`linear`/`hubspot` tools, no demo-only `investor_request`
variant. `internal/cli/twin_test.go`'s
`TestDemoManifestAddsFakeConnectorsRealDoesNot` and
`TestTwinIDDefaultsToRealAndDemoFlagOrEnvSelectsDemo` are the regression
guard that this never flips by accident.
