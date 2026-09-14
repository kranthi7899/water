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
