# Decisions, findings, and open calls — Phase 3 campaign (2026-09-14)

This file records what the master build prompt (v2) asked to be surfaced rather than decided
silently: the two investigations, the Part 10 questions, and the places the prompt was wrong,
missing, or did not know to ask.

## Investigation 1 — the tool-layer fork

**Question.** Do headless `claude -p` / `codex exec` expose a structured, interceptable tool-call
protocol, or only text?

**Finding (verified on this machine, claude 2.1.270).**

- `--output-format stream-json` emits typed events (`system/init`, `assistant` with content
  blocks, `rate_limit_event`, `result`). Tool use appears in those events, but only *after* the
  fact: the tool already ran inside the subprocess. Observable, not interceptable.
- The interceptable path is **MCP**. `claude` will spawn a stdio MCP server and route every call
  to its tools through JSON-RPC. Combined with `--tools ""` (removes every built-in tool),
  `--strict-mcp-config` (removes every user connector, the Phase 1 leak) and
  `--mcp-config <file>` (points at Water itself), the subprocess's *only* tools are the ones Water
  serves, and every call crosses a channel Water owns: parse, authorise, execute, trace, return.
- `--allowedTools mcp__water__<tool>` expresses a per-invocation whitelist from outside, so no
  permission prompt is ever shown inside the subprocess.

**Branch taken: Option C, realised through MCP.** Water owns a small, defined set (`read_file`,
`list_dir`, `write_file`, `run`), exposed per role by policy; everything else is excluded by
`--strict-mcp-config` and `--tools ""`. This is not fragile text parsing; it is the CLI's own
supported tool protocol. Verified end to end: `water run cto` read a file under a declared root
through `water mcp-serve`, the trace recorded the invocation with its permission decision and
basis, and a role with no `tools:` block reported having no tools at all.

Not verified: `codex`. It is not installed here. Codex supports MCP servers via config, but the
wiring is not shipped because it could not be checked; `CodexSubscription.SupportsTools()` returns
false and tool-bearing roles simply get no tools on that backend. This is stated in code and docs
rather than implied.

## Investigation 2 — attachments

**Finding (verified).** A bare path in the prompt gives headless `claude` nothing (`CANNOT_SEE`,
because tools are off). `--input-format stream-json` accepts a user message with structured
content blocks; a base64 `image` block was seen correctly (an 8×8 test PNG described accurately).
So attachments are **cheap** on claude: Water reads the file, base64-encodes it, and sends one
stream-json message. Documents use the `document` block; text is inlined inside explicit
UNTRUSTED markers. Codex has `--image` in recent releases; detected at runtime, not assumed.

Syntax shipped: `/attach <path>` (session-scoped, listed in `/status`), `@path` tokens (per turn),
`water run --attach`. Promotion of an attachment to memory is manual via `/remember`, like
everything else. Every turn that carried an attachment is marked untrusted and rendered as such.

## Part 10 — the open questions, resolved or flagged

1. **CEO persona scope (people decisions).** *Considered revision, not drift.* The sibling project's
   `soul_layer/REPORT.md` records the change explicitly: the draft line "You do not manage people …
   not human relationships" was replaced because it put the People & Team pillar outside the CEO's
   job and contradicted the time-use research the persona cites, whose high-performing pattern is
   people-intensive. Left as is. The instance-management framing was kept alongside it.
2. **Citation leakage.** No named academic citations or `EXP-00N` labels appear in the visible
   `experience.md` files; their sentence→source map is in `.index.json`. The CEO `soul.md` cites a
   study by its numbers ("1,114 CEOs in six countries") without naming authors; the Design
   `soul.md` cites correlation figures the same way. Both are intentional uncited prose. One real
   leak was fixed: `soul.md` files referenced `frameworks.md`, a file that does not exist in Water;
   the wording now points at skills.
3. **Uneven entries.** Every experience sentence traces to a named twin record (`.index.json`,
   confidence 1.0 each; CEO 12, COO 5, CTO 9, Design 7 lessons). The vaguer CEO entry is the
   Danone/Mylan one ("a long-term stakeholder commitment, or a favorable narrow metric pushed to
   its limit…"): it fuses two cases with different mechanisms. It traces, so it stays; splitting it
   is a content-pass call.
4. **Tool access per role — decided.** CEO: none (its work product is judgment). COO: none (it
   coordinates; giving the hub file access is the widest blast radius for the least benefit). CTO
   and Design: filesystem read-only over roots the user names in `tools.roots`, no shell, no
   network. Unrestricted shell is out of scope for v1 for every role. `tools.enabled` defaults to
   false, so a fresh install invokes nothing.
5. **Depth cap and step budget.** `orchestration.max_rounds = 2` COO assignment rounds,
   `orchestration.max_steps = 24` supersteps, whole-run timeout 5m. A step that changes nothing is
   a stall and ends the run with a resumable checkpoint.
6. **Escalation gating.** The CTO→CEO and Design→CEO edges are always *available*; what gates them
   is the persona text ("only when highly confident a checkable problem…") plus the message type.
   Water does not score confidence; a wrong-headed escalation costs one extra CEO read, a buried
   one costs the run. Availability wins.
7. **Tie-breaking.** Not decided by Water. The CEO prompt requires an explicit decision with the
   tradeoff and a reversal condition when CTO feasibility and Design exposure conflict; the
   diagnostics flag a final output that resolves nothing. The weighting is the CEO's judgment.
8. **"Irreversible exposure."** Taken verbatim from Design's persona: a failure of a documented,
   publicly written accessibility standard that also creates legal exposure, or a defect in a
   safety-critical interface that cannot be undone once shipped. Encoded in the manifest and the
   `wcag-conformance` skill's escalation note.
9. **Transcript retention.** `sessions.keep = 30` per role, age backstop 90 days, pinned exempt.
10. **Skill auto-promotion.** Manual only. `/remember` promotes; transcripts log `signal` entries and
    nothing reads them yet.
11. **Bubble Tea v2 vs v1.3.10.** v2 (`charm.land/bubbletea/v2@v2.0.9`, `bubbles/v2@v2.2.1`) is
    stable and shipped. The textarea's wrap rule is mirrored in Go so the app-side row count and the
    widget agree; the two load-bearing gates pass.
12. **Docker as opt-in hardened mode.** Not in the story. The install promise is one binary.

## Things the prompt got wrong, missed, or did not ask

- **The COO could not see the brief.** The first real hierarchy run had the COO write, correctly,
  "I don't have the underlying brief or any attachments — only your summary." The permission graph
  addresses the brief to the CEO only, and the CEO's direction was a summary. Fixed: Water appends
  the user's brief verbatim under the CEO's direction (a mechanical forward, never a paraphrase).
- **Theme-invariant chat geometry needs the hero box, not the art, to be fixed.** The first layout
  test failed because the hero region was sized from the art; two themes with different art moved
  CHAT. The engine now allocates a fixed hero box for a terminal size and scales art into it, so
  geometry is theme-invariant by construction.
- **`--tools ""` and MCP coexist.** Undocumented in the help text; verified empirically. Worth
  re-verifying on every claude upgrade (the guard test checks the arguments, not the CLI).
- **Signatures cannot live in the repo.** An HMAC keyed per machine cannot be committed; the repo
  carries `role_id` + `content_hash` (portable), signatures are added by the local keyring on first
  `persona edit`, and "edited but unsigned" fails only where a keyring exists.
- **Verbatim forwarding is done by Water, not asked of the COO.** The prompt says the COO "must"
  forward dissent word for word; a model cannot be trusted with a must. The COO node forwards
  every `Verbatim`/dissent/escalation message mechanically before the COO's own report runs.
- **Untrusted content needs a message flag.** Marking tool output in the prompt is not enough:
  the outbox had to learn `Untrusted` so a node that consumed external content cannot emit an
  unmarked message (guard test).
- **Voice `Listen` is a documented no-op.** No local STT binding was judged reliable; shipping
  `Speak` alone was the honest option the prompt allowed for.
- **Linux sandboxing is not implemented.** `sandbox-exec` on macOS is wired for the (currently
  unused) shell tool; Landlock is the planned Linux fit. No role has shell, so nothing is exposed,
  but the doc must not imply confinement exists where it does not.

---

# Follow-up campaign (2026-09-14, afternoon)

## Part 1 — COO evidence provenance (built)

Every tool invocation now carries a `call_id`, returned to the model at the top of the tool
result. A specialist cites it as `(evidence: call-cto-2d9d79)` on the statements that rest on
that read; statements resting on reasoning carry nothing. `AgentMessage.Evidence []TraceRef`
holds the references on deliverables (all cited) and on the COO's status (verified only).

The COO's grant is `tools: trace: current-run` — a new capability kind, served in-process,
never over MCP. `trace.Recorder.Resolve` refuses a reference whose run id is not its own and a
reference to a denied call. The COO gets no filesystem or shell tool from it (`TestCOOTraceScope`).

Verification is mechanical: Water splits each fresh deliverable into claims, resolves references,
and appends a block to the COO's prompt and, verbatim, to the status the CEO receives:

- reference resolves → **VERIFIED** with tool, args, role, basis
- reference does not resolve → **FAILED VERIFICATION** (dangling ids are raised, never dropped;
  `TestDanglingEvidenceFails`; `diagnose` reports `failed-verification`)
- no reference → **UNCONFIRMED** (pure judgment, working as designed; `TestUnconfirmedPropagates`)

Real run (`/tmp/whp1`, brief: double a connection ceiling, config under the CTO's root): the CTO
ran `list_dir` and `read_file`, cited both ids on nine statements; Design read the same file
independently; the COO's status marked the config values VERIFIED with the call ids and marked
"this file is what the deployed process loads" UNCONFIRMED, and the mechanical block reported the
counts. Six calls, zero metered.

Two things the build surfaced:
- Both CTO and Design have read-only roots, so both read the file. That is by design (Design's
  grant is its own), but the COO correctly noted the second read as an independent reproduction.
- A model can cite a call id on a statement that goes beyond what the read showed; provenance
  verifies that *a* read happened and was authorised, not that the sentence is entailed by it.
  That is the stated limit of "verify provenance, not content".

## Part 3 — per-role backend mixing (exercised for real)

`codex` 0.154.0 was installed via npm and was already signed in with ChatGPT on this machine.
`water run cto --backend codex-subscription` returned `CODEX-OK` (0 metered). With an
`--agents-dir` overlay setting `backend: codex-subscription` in `cto/role.yaml`, `water status`
resolved the CTO to codex with the reason `role.yaml backend:` and the others to claude. The
first orchestration with that overlay never reached the CTO because the CEO answered a build-vs-buy
brief alone (correct behaviour). The second (a 2 TB Postgres migration brief that demands a
feasibility range) ran the full graph:

| node | backend | metered |
|---|---|---|
| ceo (frame) | claude-subscription | no |
| coo (assign) | claude-subscription | no |
| cto | **codex-subscription** | no |
| design | claude-subscription | no |
| coo (verify) | claude-subscription | no |
| ceo (final) | claude-subscription | no |

Six calls, zero metered, 4m12s wall. `TestPerRoleBackend` is now backed by a real two-provider
run: resolution precedence (`role.yaml backend:` won over the config default), subprocess
invocation on codex, and cross-provider context all held.

Context-format compatibility across providers is not an issue by construction: every call is a
stateless system+prompt pair, and inter-role context travels as text in the outbox.

Still unverified: Water's MCP tools on codex (`-c mcp_servers.*`) — deliberately unwired; codex
`--image` attachments (flag confirmed present in `codex exec --help`, delivery not exercised).

## Part 5 — rate limits as the real budget (built)

- Every claude call now runs with `--output-format stream-json` so the CLI's `rate_limit_event`
  reaches Water; `Response.RateLimit` carries the five-hour and seven-day utilisation and reset
  times, persisted to `~/.water/ratelimit.json` after each call.
- `water status` prints a `budget` line; `/status` in chat shows the same.
- A call refused by the window is `backend.ErrRateLimited`: `orchestrate` exits 5 with "interrupted
  by the subscription rate limit, not by a failure (window resets …); `--resume <id>` continues
  it"; `run` and chat say the same.
- The trace records `rate_limit` (observed state) and `rate_limited` (an interruption) events;
  `diagnose` adds a `rate-limit-interruption` finding with a fan-out recommendation when any occur.

## Part 2 — orchestration diagnostics, with numbers

Method. Four briefs, each run through the full graph and with `--solo` (CEO alone, delegating
nothing; same persona, same brief). A and B are decomposable multi-domain briefs (payments
migration; onboarding rebuild); C and D are single-domain judgment calls (a distressed
acquisition; a customer delivery-date commitment). No attachments or tool roots for any of the
four. Similarity is Jaccard over distinctive terms of the final outputs; influence is the share
of a specialist's distinctive terms that reached the final; all runs zero metered.

| brief | class | graph calls / wall | solo calls / wall | same headline decision? | final similarity | specialist influence (cto / design) | dissent msgs → survived |
|---|---|---|---|---|---|---|---|
| A payments migration | decomposable | 8 / 209 s | 1 / 29 s | **yes** (don't migrate before peak; fix a11y; negotiate fees) | 0.16 | 0.00 / 0.11 | 0 |
| B onboarding rebuild | decomposable | 11 / 330 s | 1 / 34 s | **yes** (fix web-view first; no native rebuild) | 0.13 | 0.08 / 0.09 | 2 → 2 (100%) |
| C distressed acquisition | single-domain | 1 / 25 s | 1 / 23 s | yes (CEO answered alone in both) | 0.21 | — | 0 |
| D delivery-date commitment | single-domain | 1 / 26 s | 1 / 26 s | yes (CEO answered alone in both) | 0.22 | — | 0 |

Plus the two evidence-bearing runs from Parts 1 and 3 (a config file under the CTO's root; a
migration brief demanding a feasibility range): 6 calls each, tool calls 4 and 0, specialist
influence 0.12 / 0.07 and (not measured) — and in both the final output contained facts or
ranges the CEO could not have produced alone (verified config values; method-attributed
feasibility ranges).

**Synthesis versus single-agent.** On C and D the graph collapsed to the solo run by the CEO's
own decision — "this is a judgment call, I'm making it myself" — so the topology already costs
nothing on that class. On A and B the graph produced the *same headline decision* as the solo
CEO, in more words, with lower lexical overlap because the graph's final spends its length on
epistemic bookkeeping (what is verified, what is unconfirmed, a dissent acknowledged, a gate
proposed) rather than on a different conclusion. With nothing to gather, the specialists' outputs
were labelled unconfirmed-on-word by the COO and, measured lexically, changed 0–11% of the final.
Eight to eleven calls and 7–10× the wall time bought framing, not a different decision.

**Delegate-with-no-influence.** CTO 0.00 on A (its deliverable in that run was the hallucinated
tool-syntax failure, later fixed), 0.08 on B, 0.12 on the evidence run; Design 0.07–0.11. The
`diagnose` warning threshold (below 5% with at least ten distinctive terms) fired on none of them;
the honest reading is that influence is uniformly low when there is no evidence to gather and
modest when there is.

**Convergence.** No pairwise specialist similarity above the 0.6 warning line in any run; CTO and
Design deliverables read as different roles. The personas are differentiated.

**Dissent survival.** 2 of 2 typed dissents reached the CEO verbatim (brief B, both rounds). Only
one brief produced genuine specialist disagreement; the mechanism is proven mechanically by
`TestDissentForwardedVerbatim` and the two real data points, and there is no larger sample yet.

**Recommendation (not applied in this pass, per the note on what not to do).** The graph earns
its cost when there is *something to gather*: an attachment, a declared tool root with relevant
files, or a brief that names artifacts to check. It does not earn its cost on decomposable
briefs that arrive as a paragraph of assertions — there the specialists can only reason, the COO
marks everything unconfirmed, and the CEO reaches the same decision alone in a thirtieth of the
time. Proposed routing rule for the CEO's frame step: **default to `ROUTE: answer` unless the run
carries evidence sources (attachments, non-empty tool roots for a specialist, or a brief that
explicitly asks for a specialist's measured deliverable); when delegating without evidence
sources, say so and cap the run at one COO round.** The second half is already the `max_rounds`
knob; the first half is a one-line addition to the CEO frame prompt plus a flag Water can set
from `Env` (attachments present, tool roots non-empty). Caveat: n=2 per class; the same brief set
should be rerun after any persona change before the rule is made default.

## Part 4 — distribution gate

Tag `v0.1.0-rc.1` was pushed; the release workflow built and published
`water_0.1.0-rc.1_{darwin,linux}_{amd64,arm64}.tar.gz` plus `checksums.txt` (CI green).

The anonymous `curl -fsSL …/install.sh | bash` path returned 404: **the repository is private**,
so raw files and release assets are not reachable without credentials. Making the repository
public is your decision, not one this pass took. Until then the installer accepts
`GITHUB_TOKEN`/`GH_TOKEN` and uses the GitHub API for the release lookup and the asset download
(octet-stream); that path was exercised end to end on this machine in a stripped environment
(`env -i`, an empty install prefix, a fresh `WATER_HOME`): download, checksum verification,
install, `water onboard` with a real round trip, `water orchestrate --solo`, zero metered, using
only the subscription logins already on the machine. One observation: in that `env -i` shell `claude auth status` itself reported logged out (its
credential lookup depends on the login shell's environment), so Water correctly reported claude
as not logged in and selected codex; in a normal shell the same home selects claude. What this
does not prove: a machine that has never had `claude` or `codex` installed, and Linux (no VM or Docker was available here; the Linux
binaries are built but unexercised).

---

# Harness cross-check against Hermes Agent (2026-09-14, evening)

Compared Water's interactive harness section by section with the documented Hermes Agent CLI
([CLI guide](https://hermes-agent.nousresearch.com/docs/user-guide/cli),
[slash commands](https://hermes-agent.nousresearch.com/docs/reference/slash-commands),
[repo](https://github.com/nousresearch/hermes-agent)). Water's idea (persona council on a
subscription, isolation invariants) is not what was compared; the harness affordances were.

| section | Hermes | Water before | Water now |
|---|---|---|---|
| slash autocomplete | dropdown on `/`, Tab accepts | none | dropdown over the chat area on `/`, Tab completes the top match |
| exit | Ctrl+D; prints resume command, session id, duration, message count | alt-screen closed silently | `exited water · role`, resume command, slug, turns, duration |
| clipboard | `/copy [N]`, paste images | terminal selection blocked by mouse capture | mouse capture off (selection works), `/copy [N]`, Ctrl+Y, pbcopy/wl-copy/xclip/OSC 52 fallback |
| context fill | status bar with "12.4K/200K" and colour thresholds | none | `ctx ▰▰▱▱ 12% 120K/1M` from the CLI's reported window; green/yellow/red at 50/80% |
| thinking feedback | animated spinner with elapsed time | static "thinking…" | "thinking as cto… (4.2s)" ticking |
| voice | `/voice on|tts`, Ctrl+B record key, STT+TTS | `--voice` flag only, no indicator | indicator left of the composer (♪ on / ○ off), `/voice on|off|status`, Ctrl+B; TTS only — listen stays a documented no-op |
| login routing | `hermes setup` wizard, `hermes auth` | onboard only; chat failed with an error when logged out | chat detects an installed-but-logged-out CLI and runs its login flow before starting |
| undo / retry | `/undo`, `/retry` | none | `/undo` (active context only; transcript is append-only), `/retry` |
| usage | `/usage` tokens, cost, provider limits | `/status` budget line | `/usage`: subscription windows, context on the last turn, session size |
| export | `/save`, `/export` | none | `/save [file]` markdown export |
| session naming | `/title` | `/name` | `/title` alias |
| multiline | Alt+Enter, Ctrl+J, Shift+Enter, backslash | Ctrl+J, Shift+Enter when detected | unchanged (deliberate: chords that may not arrive are not advertised) |
| streaming output | token streaming with tool feed | turn-level replies | unchanged — a turn-level pipeline with per-node tracing; streaming would touch the backend contract and the trace model, so it is a design change, not an upgrade |
| `!` shell passthrough | runs shell without the model | none | not taken: Water's tools are deny-by-default; a passthrough would be a second, unpoliced path |
| interrupt-and-redirect / queue / steer | yes | input paused while busy | not taken: a Water turn is one subprocess call; redirecting mid-call has nothing to redirect into |
| background sessions, cron, messaging gateways, skill hub, Docker/SSH backends | yes | no | out of scope for a single-user council CLI |
| session store | SQLite + FTS5 search | per-role JSONL, tail-bounded compaction | unchanged; search across sessions is a reasonable next step |
| memory | agent-curated with nudges, user model | curated, manual promotion | unchanged by design (invariant #4) |

What Water has that Hermes' harness does not: per-role memory isolation enforced at prompt
assembly, a typed message permission graph, mechanical verbatim forwarding of dissent, evidence
provenance verified against the run trace, and resume-from-checkpoint that survives a rate-limit
window. Those are the product, and none of the upgrades above touch them.


---

# Infrastructure judgment pass v2 (2026-09-14)

Full write-up: `docs/judgment-pass-v2.md`. Compared against the public source of `openai/codex` and
`NousResearch/hermes-agent`. 3 adopt, 5 adapt, 5 reject. Every gap was reproduced before fixing.

- **Debugging first:** `water replay` re-runs one recorded node call in isolation (`--print`,
  `--edit`); `water debug dump <run-id>` dumps a live run without stopping it; `--debug` logs real
  subprocess flags; errors now surface the real cause and name the controlling setting.
- **Isolation (invariant 2):** a tool root containing the water home let the CTO read Design's memory
  and the keyring. The water home is now protected from all tool paths. Memory writes take a
  cross-process lock (two writers were losing half their entries). `/remember` refuses to promote an
  untrusted reply wholesale.
- **MCP server:** a single blocked read (named pipe under a root) wedged the whole server and left it
  running after its parent died. Calls are now concurrent, deadlined, regular-files-only, and the
  server exits on stdin close.
- **Shell allowlist:** allowlisting `ls` authorised `ls && …`; compound commands are now refused, and
  no shell runs where no OS sandbox exists.
- **Skills:** a read-only `water skills` report (Curator's deterministic half, no archiving) already
  shows Design's `fitts-law` and `hicks-law` never loading, and the CEO's `disagree-and-commit`
  loading far more than its trigger suggests.
- **Rejected with reasons:** Codex config layers, a pluggable execution backend (until shell is
  granted), Hermes's autonomous reflect-and-update loop (no evidence: 0 flagged replies), external
  semantic memory, OpenTelemetry.
