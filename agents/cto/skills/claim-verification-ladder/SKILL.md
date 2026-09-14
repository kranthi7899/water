---
name: claim-verification-ladder
description: Ranks the support behind a load-bearing technical claim on a ladder from independent reproduction with code and data down to a marketing assertion, asks what was held out and whether variance was reported, and makes any recommendation conditional on the rung reached or a local measurement. Use when a technical claim from a vendor, a benchmark, a leaderboard or a single reported result is load-bearing for the decision.
keywords:
    - vendor claim
    - benchmark
    - leaderboard
    - reproduce
    - verified
    - measured
    - contamination
    - held out
    - faster than
role_id: 3b8d1f6a-9c24-4e7b-8a5d-f0e2c6b1d934
file_type: skill
content_hash: sha256:4ac3ebfd72cae439d0de60f97a7eb3420a05a3a45709e3afc6f94161bf3eca49
---
# Claim-Verification Ladder

The role's signature tool: the one the integrity check (no unfounded technical commitment) most directly exercises. Name it in a phrase when you use it.

## When to use
- A technical claim is load-bearing for the decision.
- It originates from a vendor, a benchmark, a leaderboard, or a single reported result rather than from something checked independently on this system.

## How to apply
1. Rank the claim's support on a ladder, highest to lowest: independent reproduction with released code and data; peer-reviewed result; single-run published benchmark; vendor-reported benchmark; marketing assertion with no methodology given.
2. Ask what was held out (did the test set overlap anything the system was tuned or trained on), whether contamination is plausible, and whether any variance or run-to-run spread was reported at all.
3. State the rung the claim actually sits on in the output, not just the claim's headline number, and make any recommendation conditional on either a higher rung being reached or a local measurement.

## Evidence grade
Grade B. Each cited study is itself a peer-reviewed empirical result about the reliability of other reported results: of 30 highly cited AI studies, reproduction succeeded for 86 percent of those sharing both code and data versus 33 percent sharing data only; benchmark contamination is detectable and exploitable when present; undisclosed private testing and selective submission materially inflate public leaderboard rankings.

## Worked example
Constructed. A vendor's product page states "2 times faster than the leading alternative". No held-out test set, no variance across runs, no independent reproduction. The claim is reported as sitting on the vendor-reported-benchmark rung, and the recommendation is made conditional on a local measurement against the actual workload before it is trusted for capacity planning.

## Source
Gundersen, O. E., Cappelen, K. C., Molna, M. and Nilsen, E. R. (2025), "The Unreasonable Effectiveness of Open Science in AI: A Replication Study", Proceedings of AAAI 39. Magar, I. and Schwartz, R. (2022), "Data Contamination: From Memorization to Exploitation", Proceedings of ACL 2022. Singh, S. and colleagues (2025), "The Leaderboard Illusion", NeurIPS 2025 Datasets and Benchmarks.
