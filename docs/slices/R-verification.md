# Slice R — Phase 4 verification

Per `docs/WORKFLOW.md`'s Phase 4: this is evidence from running the real thing, not just passing unit tests. Run against an isolated `WATER_HOME=/tmp/water-verify-r`, never `~/.water`. Google/GitHub connectors were reachable through the shared Keychain credential even in this isolated home (the credential lives outside `$WATER_HOME`, by design since Slice A3) — every live interaction below was kept strictly read-only, per this slice's own Phase 4 scope note ("write intents and voice approval are verified against the fake-connector daemon harness only, never a real account"). No event was created, no mail was sent, no write intent was exercised against a real account.

Date: 2026-09-25. Binary built from `feat/ceo-twin` at commit `ea5a257` (post-merge, post-fix).

## Build and test gates

| Check | Result |
|---|---|
| `go vet ./...` | clean |
| `gofmt -l .` | clean |
| `go test -count=1 -race ./...` | 47/47 packages pass (run repeatedly across this session, including post-fix) |
| `CGO_ENABLED=0 go build ./cmd/water` | succeeds |
| `clients/macos/build.sh` | clean, zero warnings |
| `clients/macos/test.sh` | 75/75 tests, 11 suites |

## Tier 0: live latency

`water ask "what's on my calendar today"`, isolated empty store, 10 runs:

```
real 0.03 / 0.03 / 0.03 / 0.03 / 0.02 / 0.02 / 0.02 / 0.02 / 0.02 / 0.03
```

Median ≈ 25ms wall-clock (criterion: ≤300ms). `water route report` over these turns:

```
turns 12   tiers t0=12   owners quick=12   outcomes answered=12
latency total p50=1ms p95=1ms   ack p50=0s p95=0s
possible misses 0
```

Internal p95 = 1ms (criterion: t0 p95 ≤ 50ms).

## Tier 0: offline eval, real registry

`TestTier0Eval` didn't exist before this verification pass — see "Bugs found" below. Once written, against the real embedded `twins/ceo/intents/*.yaml` registry (not a fixture):

```
N=298 hit_rate=0.604 intent_acc=1.000 slot_acc=1.000
fa_rate=0.0000 wilson95=0.0127 escalation_rate=0.611 reasoning_answered=0
```

- Criterion 1 (0 false accepts out of ≥276 cases): **met** — 0/298.
- Criterion 2 (hit rate ≥50% and ≥ legacy baseline+20pts, i.e. ≥26.2%): **met** — 60.4%.
- Reasoning/multi-clause never answered by Tier 0: **met** — 0.

## Main path: live escalation, real subscription call, real tool use

`water ask "what should I prioritize today"` (an `escalate_word` case — "prioritize") escalated correctly and streamed a real answer from the subscription `claude` CLI in 12.7s. `water route report --json` for this turn:

```json
"EscalationReasons": {"escalate_word": 1},
"LatencyP95": {"main": 12668000000},
"AckP50": 1000000, "AckP95": 1000000,
"FirstSentenceP50": 7589000000,
"QuickToolUsageCounts": {"quick.latest_mail": 1, "quick.pending_approvals": 1}
```

The model called `quick.latest_mail` and `quick.pending_approvals` live through the R-16 quick-tools bridge while composing its answer — proof the head-chef/sous-chef toolbox integration works end to end with a real model, not just in unit tests. Ack latency 1ms (criterion: ack emitted well inside 300ms of escalation).

**Conservative escalation confirmed live**, not just by design review: `water ask "status"` and `water ask "what can you help me with"` both escalated with reason `no_match` rather than guessing, because neither phrase is a literal configured template (`status_overview.yaml` only has "status check", not bare "status"; `help_intents.yaml` has close-but-not-identical phrasings). The main path answered both gracefully. This is the intended "never guess" behavior, observed live.

`water ask "never mind"` escalated (reason `escalate_word`, since "never" is in the escalate-words list) rather than answering via `control.stop` — the exact, deliberate limitation R-9 documented in `control_stop.yaml`'s own notes, confirmed live rather than only in review.

## FunctionGemma / Tier 1: live pull and eval

`brew install llama.cpp` (owner-approved, 2026-09-24) installed `llama-server` 0.5.0.

`water model pull functiongemma --accept-gemma-terms`: downloaded and verified 291,557,792 bytes — an exact match to the pinned `ModelSizeBytes` constant. The pinned sha256 (`83940d4d...5270`) was independently re-verified against the live Hugging Face API during this session (separately from the R-17 build), confirming filename, revision, and hash all still match `ggml-org/functiongemma-270m-it-GGUF`.

`water route eval --tier1` — first run crashed (see "Bugs found"); after the fix, a clean run over the real sidecar:

```
tier1 eval 410 cases, 0 false accepts (fa_rate 0.0000, wilson95 upper 0.0093), warm p95 1313ms
            hit_rate=0.395 intent_acc=1.000 escalation_rate=0.707 reasoning_answered=0
gate: FAIL — reason: eval_failed
```

| Threshold | Required | Measured | Pass? |
|---|---|---|---|
| False-accept rate | ≤1% | 0.00% | ✅ |
| Wilson 95% upper bound | ≤2% | 0.93% | ✅ |
| n | ≥200 (plan target ≥400) | 410 | ✅ |
| Warm p95 latency | ≤400ms | 1313ms | ❌ |
| Reasoning answered | 0 | 0 | ✅ |

**Every safety criterion passes with margin; latency does not, on this Mac's CPU-only `llama-server` configuration.** This is a genuine, expected real-world result, not a code defect — 270M-parameter constrained-grammar tool-call decoding at ~1.3s is plausible for unaccelerated inference. The eval-gate mechanism worked exactly as designed in response: setting `router.tier1.enabled=true` and restarting the daemon produced `water daemon: tier1 not started: eval_failed` in the daemon's own log — Tier 1 correctly stays inert even when explicitly enabled, because the recorded eval doesn't clear every threshold. Reset to `false` afterward (the safe default).

**Not investigated in this pass, worth a follow-up:** whether different `llama-server` flags (thread count, Metal-offload flags, a smaller context) would bring warm p95 under 400ms on this hardware. Recorded in `docs/known-gaps.md`.

## Audit log

`water audit verify` at two checkpoints during this session: `audit log verifies: 19 entries` (early) and `audit log verifies: 145 entries` (after the full session's turns, tool calls, and config changes). Chain never broke.

## Not covered in this pass

- **Voice ack, on real hardware.** No real `SFSpeechRecognizer`/`AVSpeechSynthesizer` session was run (would need the Swift client's UI, a microphone, and manual interaction). The underlying mechanism (`ack_ms`) was measured at 1ms via the CLI's equivalent escalation path above; the Swift-side wiring itself was verified by its own test suite (R-27) but not exercised against a live daemon end to end.
- **Write intents and voice approval against a real account.** Per this slice's own Phase 4 scope, these stay fake-connector-only — already covered by R-20/R-21's test suites, not re-run live here (no event was created, no mail sent).
- **Speculation/partials over a live voice session.** Same reason as voice ack — needs the Swift client's real audio path.

## Bugs found and fixed during this verification pass

1. **Nil `Deps.Brief` panic** (`internal/nervous/reflex/handlers.go`, `internal/nervous/eval/live.go`) — the live Tier 1 eval crashed the instant it reached a `brief.today`-shaped case, because `live.go` never set `Deps.Brief` and `cachedBriefHandler` called the nil func value directly. Every unit test masked this (the shared `testFixture` helper always sets a non-nil stub), so it was only reachable through `live.go`'s own separate construction — never exercised before because `TestTier1Live` always skipped without a real model. Fixed at both the call site (explicit clean-miss stub) and the handler (defensive nil-check, matching this file's existing `d.Tasks`/`d.Manifest`/`d.Health` pattern). Commit `83e15af`.
2. **`TestTier0Eval` didn't exist** despite being cited by name in both `docs/architecture.md` and `docs/slices/R.md`'s acceptance criterion 1 as the test enforcing Tier 0's 0%-false-accept-rate guarantee. Written for real against the actual embedded registry; see numbers above. Commit `ea5a257`.

Both are now part of `feat/ceo-twin`'s history, not just this verification pass's scratch environment.
