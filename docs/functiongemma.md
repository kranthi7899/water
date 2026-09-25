# FunctionGemma (Tier 1 sidecar)

Slice R's Tier 1 ("the sous chef's second engine") uses Google's **FunctionGemma**
as a local, offline function-calling model for utterances Tier 0's deterministic
templates miss. This document covers what it is, why it runs as a sidecar, its
license terms, and how it is fetched. Tier 1 ships **disabled by default**
(`router.tier1.enabled=false`) and stays disabled until the live eval gate in
`docs/slices/R.md` §10 passes — nothing here enables it by itself.

## What it is

- Model: [`google/functiongemma-270m-it`](https://huggingface.co/google/functiongemma-270m-it)
  — a 270M-parameter, instruction-tuned function-calling variant of Gemma 3.
  It is small enough to run on a laptop CPU/GPU with low latency, and it is
  designed specifically to be fine-tuned for a narrow, fixed set of tool
  declarations, which matches this slice's use: proposing calls only against
  the `reflex_eligible` intents in `twins/ceo/intents/*.yaml`.
- It is **not** used for reasoning, judgment, drafting or planning. Its only
  job is to turn an utterance into at most one function call with arguments,
  which is then re-validated by the same shared validator Tier 0 uses
  (grounding-checked against the utterance) before anything is treated as an
  answer. See `docs/slices/R.md` §10 for the adapter and grounding rules.

## Why a sidecar, not a Go dependency

- The model runs behind **Homebrew `llama-server`** (from `llama.cpp`),
  owner-approved on 2026-09-24, as a **separate OS process** that the daemon
  starts, health-checks and keeps warm — never linked into the `water`
  binary.
- This adds **no new Go module** (`go.mod`/`go.sum` are unaffected) and **no
  cgo dependency**, honoring `CLAUDE.md`'s "zero metered spend" and
  "never a cgo dependency" rules: `llama-server` talks HTTP on `127.0.0.1`
  only, and Water's Go code is an ordinary loopback HTTP client to it
  (`internal/nervous/t1`).
- The sidecar is started once at daemon startup (only when Tier 1 is enabled
  and the eval gate has passed) and never spawned per turn.

## License: the Gemma Terms of Use

FunctionGemma is distributed under Google's **Gemma Terms of Use**, not an
OSI-approved open-source license:

- **Terms of Use:** <https://ai.google.dev/gemma/terms>
- **Prohibited Use Policy:** <https://ai.google.dev/gemma/prohibited_use_policy>
  (incorporated into the Terms of Use by reference)

Key points for this project:

- **Not OSI-approved.** Gemma 1–3 (which includes FunctionGemma) ship under
  Google's own custom terms, not Apache 2.0 or another OSI license.
- **Use, modification and commercial use are allowed**, subject to the
  Prohibited Use Policy and applicable law.
- **Restrictions travel with the weights.** If Water ever redistributed the
  model or a derivative (a fine-tune, say), it would have to pass the same
  use restrictions on to whoever received it, include a copy of the
  agreement, and note that it's subject to the Gemma Terms of Use.
- **Local use is not exempt.** The terms apply to anyone who uses,
  reproduces or modifies the model, not only to redistribution — so running
  it locally still means operating under the Prohibited Use Policy.
- **Water never redistributes weights.** The downloaded `.gguf` file lives
  only under `$WATER_HOME/models/`, outside this git repository entirely, on
  the owner's own machine, and is never committed or shipped to anyone else.
  This keeps the redistribution obligations above moot in normal operation —
  they would only become relevant if the owner chose to hand the model file
  (or a fine-tune of it) to someone else, which nothing in this codebase does.

## Hugging Face gating

The model page requires accepting Google's usage license on Hugging Face
before it can be downloaded:

1. The owner visits <https://huggingface.co/google/functiongemma-270m-it>
   (or the base <https://huggingface.co/google/gemma-3-270m-it>) while logged
   in to Hugging Face and clicks "Agree and access repository".
2. Some gated repos require a Hugging Face access token (`HF_TOKEN`) even
   after acceptance. `water model pull functiongemma` honors `HF_TOKEN` from
   the environment and says plainly when a download fails because a token is
   required or missing — it never prompts for or stores a token itself.

## The pull command

Nothing downloads implicitly. The owner runs it explicitly:

```
water model pull functiongemma --accept-gemma-terms
```

- `--accept-gemma-terms` is required; without it the command refuses and
  prints these terms URLs instead of downloading anything.
- It downloads a pinned GGUF build to
  `$WATER_HOME/models/functiongemma.gguf.part`, verifies its sha256 against a
  constant pinned in code, and only then renames it into place as
  `functiongemma.gguf`. A sha mismatch deletes the partial file and fails.
- The exact GGUF source and its sha256 are pinned as constants in
  `internal/nervous/sidecar/model.go`, confirmed live against the Hugging
  Face API on 2026-09-24 (task R-17):
  - Repo: [`ggml-org/functiongemma-270m-it-GGUF`](https://huggingface.co/ggml-org/functiongemma-270m-it-GGUF)
    — Hugging Face's own llama.cpp/GGUF-conversion org, converted directly
    from `google/functiongemma-270m-it`.
  - File: `functiongemma-270m-it-q8_0.gguf` (~292 MB). The repo publishes
    only two quantizations, `bf16` (~543 MB) and `q8_0`; `q8_0` is the
    CPU-friendly pick — there is no lower (Q4/Q5/Q6) build here, and at
    8-bit it is effectively lossless for a 270M-parameter model.
  - Revision: `2566ce14aedfc14fdd0de955ba67346425e67126` (the repo's HEAD
    commit at verification time).
  - sha256: `83940d4dd9676710856f43523bed096164a595a96f6b34771610a03937de5270`,
    read from the Hugging Face API's blobs listing
    (`GET /api/models/ggml-org/functiongemma-270m-it-GGUF?blobs=true`) and
    cross-checked against the `X-Linked-ETag` header on the file's
    `resolve` redirect.
- The daemon never starts the sidecar or attempts a download on its own;
  `water status` / `GET /v1/router` simply reports "model not found" when
  `router.tier1.enabled=true` but the file is absent.

## The eval gate before Tier 1 can answer

Even once the model is downloaded and `router.tier1.enabled=true`, Tier 1
never answers a real turn until a recorded live evaluation (`water route eval
--tier1`, task R-18) passes **all** of:

- false-accept rate ≤ **1%**
- a Wilson 95% upper confidence bound on the false-accept rate ≤ **2%**
- at least **200** evaluated cases (the plan uses ≥ 400 for headroom)
- warm p95 latency ≤ **400 ms**
- the recorded eval's model sha256 and intent-registry hash match what's
  currently loaded (a changed model or registry invalidates a stale pass)

`GET /v1/router` reports which of these is unmet (`eval_missing`,
`eval_stale`, or `eval_failed`) whenever Tier 1 is configured on but not
actually serving answers.

## What actually happens when you pull and eval it (2026-09-25)

Run for real during Slice R's Phase 4 verification (`docs/slices/
R-verification.md`), not just simulated — `water model pull functiongemma
--accept-gemma-terms` and `water route eval --tier1` against a real,
owner-approved Homebrew `llama-server` sidecar. Two things worth knowing
before you try this yourself:

- **This Mac's Homebrew `llama.cpp` bottle is CPU-only.** `otool -L` on
  `libggml-base.dylib` shows only `libSystem`/`libomp`/`libc++` — no
  `Metal.framework` linkage, no GPU acceleration at all. That's a property
  of the bottle, not of `llama-server`'s flags: no combination of startup
  flags turns on hardware acceleration that isn't compiled in. A
  from-source build with `-DGGML_METAL=ON` would very likely help a lot,
  but that's a different decision than "the Homebrew bottle," which is
  what was approved.
- **The gate correctly keeps Tier 1 off, for a real reason.** The first
  eval run looked like a pure latency problem (0% false accepts, 1313ms
  p95) — but that "0%" was vacuously true: a parsing bug
  (`internal/nervous/t1/client.go` only read a wire field `llama-server`
  never populates for this model) meant Tier 1 had never actually
  answered anything. Fixed (see `docs/known-gaps.md`'s Slice R section for
  the detail) — the honest re-run shows Tier 1 genuinely answering, with a
  real 2.2% false-accept rate and a 558ms p95, both still outside
  threshold. Expect roughly these numbers if you re-run the eval on
  similar (CPU-only, 8GB M-series) hardware; they are not a fluke of one
  run.
