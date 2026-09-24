# Setting up the agent's own identity

Water's write functions (`gmail.send_message`, `gmail.draft_message`,
`gcal.create_event`, `gcal.move_event`) send mail and touch your calendar as
**an agent, never as you.** The design: a second, real Gmail account
becomes a verified "Send mail as" alias on your real Gmail account, so
outbound mail's `From:` header always reads as the agent while sending still
uses your existing Google connection — there is no second OAuth grant, only
a second address. This is a one-time setup, about 10 minutes, after you've
already done `docs/google-setup.md` once.

## 1. Create the agent's own Gmail account

Create a new, ordinary Gmail account the agent will be known as — for
example `water.twin@gmail.com`. Any name works; the owner picks it. This is
a real account with its own password, not a service account.

## 2. Add it as a verified "Send mail as" alias on your real account

On your **real** Gmail account (the one water is already connected to),
through Gmail's own web UI — no water command does this step, it is
entirely Google's own flow:

1. Gmail → **Settings** (gear icon) → **See all settings**.
2. **Accounts** tab → **Send mail as** → **Add another email address**.
3. Enter the agent's name (e.g. "Water") and its address
   (`water.twin@gmail.com`).
4. Google emails a confirmation link to the agent's mailbox. Sign into the
   agent's account, open the email, and click the link to verify.
5. Back in your real account's **Send mail as** list, the new address now
   shows as verified.

From here on, your real Google account (the one `water connect google`
already authorized) is allowed to send mail with `From: water.twin@...`.
Sending still goes through your account's own quota and reputation; the
alias only changes what the `From:` header says.

## 3. Tell water the alias address

```sh
water config set agent.mail_address water.twin@gmail.com
```

`gmail.send_message`/`gmail.draft_message` always set `From:` to this
address — there is no way for a call's arguments to override it (the
schema has no `from` property at all). Leaving this unset means those two
functions refuse to run, with a clear error naming the missing config key,
rather than silently sending as something else.

## 4. Reconnect Google to accept the grown scopes

The write functions need scopes `docs/google-setup.md`'s original consent
didn't request (`gmail.send`, `gmail.compose`, and `calendar.events` in
place of the old read-only calendar scope). Google requires re-consent
whenever the requested scope set grows:

```sh
water connect google --client-file ~/Downloads/client_secret.json
```

Approve the consent screen again; it now lists send/compose mail and
calendar write access alongside the read scopes. Tick every checkbox, same
as the first time.

## 5. (Optional) tell water where to forward mail meant for you

`internal/agentmail`'s inbound-triage watcher (below) forwards a message it
judges is actually meant for you, not the agent, as a normal level-A
approval you decide like any other:

```sh
water config set agent.forward_to you@your-real-address.example
```

Leaving this unset means such mail is still logged in `water doctor`'s
daemon output, just never staged for approval — there's nowhere configured
to send it yet.

## 6. Connect the agent's own mailbox for inbound triage

A second, distinct vault credential — not the same one `docs/google-setup.md`
stored — under the account name `agent`:

```sh
water connect google --account agent --client-file ~/Downloads/client_secret.json
```

This opens a **separate** OAuth consent flow: sign into the **agent's own**
Google account (the one from step 1), not your real one, and approve it.
Water now polls the agent's own mailbox on its own background tick,
classifying each new message as addressed to the agent (a quiet line in
your morning brief) or actually meant for you (staged as a
`gmail.send_message` forward you approve).

## 7. Verify

```sh
water doctor                    # "google: connected" for both the real and agent accounts
water connect google --status --account agent
water ask "draft a short thank-you email to test@example.com"
water approve                   # review, then say yes to actually send a live test
```

The draft/send is queued for your approval like any other level-A action —
nothing goes out until you say yes.
