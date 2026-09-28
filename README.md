<p align="center">
  <img src="docs/assets/water-banner.svg" alt="WATER" width="100%">
</p>

# Water ≋

**A personal AI chief of staff. It reads, prepares and drafts. You decide.**

Water runs on your Mac as one long-running daemon. It watches your calendar, mail and documents,
works out what needs your attention, and prepares the decision with the evidence attached. It
never sends, posts, or spends anything without your explicit approval of the exact payload.
Every model call goes through the `claude` CLI you already have, signed in with your own Claude
subscription — no metered API key, no separate model bill.

## What it does

- **Tells you what needs you.** A ranked list of decisions and approvals, ranked by urgency.
- **Prepares decisions.** Each card shows the evidence, with every number tied to its source —
  never a model's paraphrase of what the data says.
- **Drafts, never sends on its own.** Emails, calendar changes, and Linear updates wait for your
  yes.
- **Listens.** Hold a key and speak. When it needs your approval, it reads back exactly what it's
  about to do before it does it.

## Principles

1. **Approval before action.** An approval covers exactly what you saw. Edit anything about it
   and the approval is void — a fresh one is required.
2. **Default deny.** A tool exists for the twin only if its manifest grants it, at a stated
   level (read-only, draft-only, approval-required, autonomous-within-limits, or blocked). Most
   connectors start read-only; outward effects are level-gated explicitly.
3. **Outside content is data, not instructions.** Anything from email, chat, or the web is
   tagged untrusted. It can inform an answer; it can't make Water act.
4. **Everything is logged.** A hash-chained, append-only audit log records every gate decision,
   approval, and execution.
5. **Local first.** State lives in SQLite on your machine. Secrets live in the macOS Keychain,
   never in a file or in a model's context.

## Quick start

**Water needs the `claude` CLI installed and signed in to a paid Claude plan (Pro or Max) —
it has no bundled model and no metered fallback.**

```sh
curl -fsSL https://claude.ai/install.sh | bash   # if you don't already have it
claude auth login
```

Then install Water itself (macOS or Linux; native Windows isn't supported, use WSL2):

```sh
curl -fsSL https://raw.githubusercontent.com/kranthi7899/water/main/install.sh | bash
```

Or build from source (needs Go 1.27.1+; the module path is `water`, so `go install
github.com/.../water@latest` won't work — clone and build from the checkout):

```sh
git clone https://github.com/kranthi7899/water.git && cd water
go build -o ~/.local/bin/water ./cmd/water
```

Then:

```sh
water onboard                 # verifies claude is signed in, writes ~/.water/config.yaml
water connect google          # optional: your real Calendar/Gmail/Drive, read-only to start
water daemon                  # starts the background service — leave it running,
                               # or `water daemon install` for a macOS LaunchAgent
water ask "what's on my calendar today?"
```

`water doctor` is the first thing to run if anything looks wrong — it checks the model backend,
every connector, and the daemon in one pass.

## Connectors

| Connector | Access | Status |
|---|---|---|
| Google Calendar, Gmail, Drive, Sheets | Read-only, plus gated writes (send mail, create/move events) | Working |
| Linear | Read; comments and priority changes behind approval | Working |
| GitHub | Read-only (PRs, issues) | Working |
| HubSpot | Read-only (deals, contacts) | Working |

## How it fits together

```
  voice · web UI · CLI
          │
       daemon ──► gate ──► connectors
          │         │
    SQLite store  audit log
```

Every request goes through one gate. It decides what an action is allowed to do, asks for your
approval when it must, and writes the result to the audit log — no connector can bypass it.

## Status

Early, and built for one user. Interfaces will change. `water --help` and `water doctor` are
always the source of truth for what's actually wired up right now.
