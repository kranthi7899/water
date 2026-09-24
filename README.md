# water

A CEO digital twin on the Claude subscription you already pay for.

Water is a single Go binary built around one long-running daemon: `water daemon` owns the
permission gate, the approval queue, a hash-chained audit log, a local SQLite state store, and
the model session, all behind one local socket. Every client — `water chat`, `water ask`, `water
approve`, and later a macOS menu-bar app — talks to that one daemon. The twin never sends,
posts, deletes, or spends anything without a passing gate check and, for anything with an
external effect, an approval you explicitly said yes to for the exact payload. Every model call
goes through the `claude` (Claude Code) CLI you already have, signed in with your Claude
subscription. Water doesn't use a metered API key and refuses to use one unless you explicitly
opt in.

Water used to host four role-agents (CEO, COO, CTO, Design) orchestrated through a state graph.
That system is gone; this is a rebuild into a single personal agent. The old code survives only
in git history.

---

## Prerequisites (read this first)

**Water can't do anything without the `claude` CLI installed and signed in.** Water doesn't
install it and doesn't bundle a model. If it isn't there, every command except `version` and
`doctor` fails.

| You pay for | Install | Sign in | Check |
|---|---|---|---|
| A paid **Claude** plan (Pro or Max) | `curl -fsSL https://claude.ai/install.sh \| bash`<br>or `npm install -g @anthropic-ai/claude-code` | `claude auth login` | `claude auth status` |

- The `npm` route needs Node.js.
- **Sign in to Claude yourself before running `water onboard`** (see [First run](#first-run) for
  why).
- A `codex` (ChatGPT/OpenAI) backend is still selectable in config for now, but the twin's tool
  bridge and streaming warm session are only exercised against `claude`.

---

## Install

### macOS and Linux

Release binaries exist for `darwin` and `linux` on `amd64` and `arm64`. The macOS builds have been
installed and run end to end. **The Linux builds are produced by CI but haven't been run on a
real Linux machine yet.**

> **The GitHub repository is currently private.** Until it's public, the anonymous one-liner
> below returns 404. If you have access, export a token first (a personal access token with
> read access to `kranthi7899/water`, or `$(gh auth token)` if you use the GitHub CLI) and the
> installer will use the GitHub API instead:
>
> ```sh
> export GITHUB_TOKEN=ghp_yourtokenhere
> ```

**This is macOS/Linux only — it does not work on native Windows** (see [Windows](#windows) below).

```sh
curl -fsSL https://raw.githubusercontent.com/kranthi7899/water/main/install.sh | bash
```

### Windows

**Native Windows isn't supported.** Water ships no Windows binary, and the installer is a bash
script that exits on anything other than macOS or Linux. The supported route is **WSL2**:

1. Install WSL2 with its default Ubuntu distribution. Follow Microsoft's guide:
   <https://learn.microsoft.com/windows/wsl/install>. Restart when it asks.
2. Open the Ubuntu terminal. Do **everything** from here on inside WSL:
   - install and sign in to `claude` inside WSL (a Windows-side install won't be seen),
   - then follow the [macOS and Linux](#macos-and-linux) steps above (bash `PATH` fix).

The daemon's Unix socket and the macOS-specific pieces (Keychain, the LaunchAgent, the future
Swift client) are untested under WSL.

### Build from source

You need Go **1.27.1 or newer**. The module path is `water`, not
`github.com/kranthi7899/water`, so `go install github.com/kranthi7899/water/cmd/water@latest`
**won't work**. Clone the repo and install from the checkout instead:

```sh
git clone https://github.com/kranthi7899/water.git
cd water
go install ./cmd/water            # installs to $(go env GOPATH)/bin — make sure that is on PATH
# or: go build -o ~/.local/bin/water ./cmd/water
water version
```

---

## First run

```sh
water onboard
```

`water onboard` does three things, in order:

1. **Detect.** It checks whether `claude` is installed and signed in, and whether a metered API
   key is set.
2. **Verify.** It sends one real, minimal message through the chosen backend and requires a
   non-empty reply that wasn't metered. **Onboard doesn't report success without that round
   trip.** If it fails, it exits non-zero with `setup is NOT complete`.
3. **Save.** It writes `~/.water/config.yaml` (set `WATER_HOME` to use another directory).

If neither CLI is installed and signed in, onboard stops and tells you how to install one.

Flags:

| Flag | Effect |
|---|---|
| `--no-login` | Only detect; never launch a login flow |
| `--headless` | Force the no-browser login path |
| `--yes` | Don't prompt; useful for scripts |

Then start the daemon:

```sh
water daemon
```

It prints the twin it's serving, the socket path, and a bearer token for the `cli` client
(persisted to `~/.water/run/clients.json`, so this only happens once). Leave it running in a
terminal, or install it to start automatically:

```sh
water daemon install     # writes a macOS LaunchAgent, KeepAlive, logs under ~/.water/logs/
water daemon uninstall
```

---

## Usage

Every client below talks to the running daemon over its Unix socket. **None of them falls back
to running in-process** — if the daemon isn't up, they say so and name the command to start one.

### `water connect google`

Connects the CEO's real Calendar, Gmail and Drive (read-only) so the twin answers from real
data instead of the in-memory fakes. One-time setup, about 10 minutes — see
[`docs/google-setup.md`](docs/google-setup.md) for the exact GCP-console clicks.

```sh
water connect google --client-file ~/Downloads/client_secret.json
water connect google --status     # forces a live token refresh
water connect google --revoke     # revokes at Google and forgets the credential
```

Once connected, `water daemon` also runs a small background sync (today's calendar and the
last day of mail, every 10 minutes by default — `sync.interval_minutes`) so the fast paths below
usually have fresh data without waiting on a conversation to trigger it.

### `water ask "<text>"`

Streams one turn to stdout. This is the scriptable, one-shot path — a macOS Shortcut or a cron
job calls this.

```sh
water ask "what's on my calendar today"
water ask --voice "give me a two-sentence status update"   # speaks each sentence as it streams
```

A question the twin can answer straight from the local state store (today's or tomorrow's
schedule, pending approvals, whether the morning brief is ready) never reaches the model at all.

### `water chat`

A back-and-forth conversation in the terminal. `/clear` resets the twin's context (restarts the
warm model session); `/quit` or Ctrl-D ends it.

```
water chat — the CEO twin. /clear resets context, /quit (or Ctrl-D) ends the session.
> what's on my calendar tomorrow
Nothing on the calendar for tomorrow.
> /quit
```

### `water approve`

Lists pending approvals as a numbered menu — the exact action, recipient, and a summary built by
code from the structured payload, never a model's paraphrase — and lets you decide one. Saying
yes both approves and executes the action, in that one step, exactly once.

```
Approvals waiting (1):
  1. Create event 'Board Sync' starting 2026-10-01T10:00:00Z. [risk medium, expires 20:29]
Enter a number to review it, then answer yes or no.
> 1
Create event 'Board Sync' starting 2026-10-01T10:00:00Z. Say yes to create it or no to cancel.
yes or no> yes
env_20835103e16e7d5567e3c15c: executed
executed: {"id":"ev1","title":"Board Sync","start":"2026-10-01T10:00:00Z"}
```

### `water status`

The twin's manifest (which connector functions it has, at what level), the resolved backend and
why, and your subscription budget. Add `--no-probe` to skip checking the backend.

```
twin      ceo (CEO twin)
functions gcal.list_events, gmail.list_messages, gmail.get_message, gdrive.search_files, ...
backend   claude-subscription first available non-metered backend
budget    claude-subscription: 5h window 26% used (resets 12:00) · 7d window 56% used
config    /Users/you/.water/config.yaml
```

### `water doctor`

A full checkup: backends and sign-in, which one is selected, any metered keys in your
environment, the twin's manifest, whether the daemon is running, voice, and whether onboard's
round trip passed. Start here when something's wrong.

```
  ✓ config                       /Users/you/.water/config.yaml
  ✓ backend claude-subscription  subscription login (max, you@example.com)
  ✓ selection                    claude-subscription (first available non-metered backend)
  ✓ credential leak              no metered API keys exported in this environment
  ✓ twin                         ceo: 5 function(s) across 3 connector(s)
  ✓ google                       connected (water.google/ceo)
  ! daemon                       not running; start it with `water daemon`
  ✓ voice                        os (speak only; listen is a documented no-op)
  ✓ onboard                      verified round trip at 2026-09-15T22:06:36Z
```

### `water audit`

Inspects the hash-chained audit log every gate decision, approval and execution is written to.

```sh
water audit verify     # walks the chain; fails loudly on any break
water audit repair      # drops a single torn final line (a crash mid-write), if that's the only break
```

### `water config`

With no arguments, prints every setting, its value, and where the value came from (`file` or
`default`). The file is `~/.water/config.yaml`.

```sh
water config
water config get backend.preferred
water config set backend.preferred claude-subscription
```

Settings you're most likely to change:

- **`backend.preferred`**: `claude-subscription`, `codex-subscription` or `auto`.
  Precedence is: `--backend` flag → config → auto.
- **`backend.allow_metered`**: stays `false` unless you explicitly opt into a metered API key.

### Other commands

| Command | What it does |
|---|---|
| `water daemon token new <name>` | Mints a bearer token for another client (a Shortcut, the future Swift app) |
| `water voice "text"` | Test speech output: `say` on macOS (verified), `spd-say` or `espeak` on Linux (not verified). There's no speech-to-text yet |
| `water version` | Print version, commit and build date |

Exit codes:

| Code | Meaning |
|---|---|
| 0 | OK |
| 1 | Error |
| 2 | Bad usage or a missing prerequisite |
| 3 | No usable backend, or a metered backend was refused |
| 4 | Not configured (run `water onboard`) |
| 5 | Subscription rate limit reached; resume later |
