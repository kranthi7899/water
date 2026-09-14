---
name: mirroring-check
description: Compares an intended module or service architecture against the actual team and communication boundaries, names any mismatch, and forces an explicit choice between realigning teams or accepting architectural drift. Use when a proposal sets a target architecture, such as a move to services with defined boundaries, or when a team or org change with architectural consequences is proposed.
keywords:
    - conway
    - mirroring
    - team boundaries
    - service boundaries
    - architecture
    - org structure
    - microservices
role_id: 3b8d1f6a-9c24-4e7b-8a5d-f0e2c6b1d934
file_type: skill
content_hash: sha256:db96528e33064647c2f3f035777391b309980d66527700da5118464bee0776ca
---
# Mirroring Check (Conway)

A thinking tool. Name it in a phrase when you use it; do not turn the answer into a tutorial about it.

## When to use
- A proposal sets a target architecture (for example a move to services with defined boundaries).
- A team or org change is proposed that has architectural consequences.

## How to apply
1. Compare the intended module or service boundaries against the actual team and communication boundaries: who talks to whom, not the org chart as drawn.
2. Where they do not match, name the mismatch explicitly: either the teams need to realign to the target architecture, or the architecture will drift toward the team boundaries that actually exist.
3. State which of those two is being chosen, and why. Do not let the target architecture stand unexamined against a team structure that contradicts it.

## Evidence grade
Grade B: observational, not experimental, but tested directly against real system data twice, in different settings, with the same direction of result both times (a design-structure-matrix test of commercial versus open-source products; organisational metrics predicting failure-proneness in Windows Vista).

## Worked example
Constructed. A target architecture calls for independent services, but the team building it is a single group with no internal ownership boundaries and constant cross-cutting communication. Flagged as likely to produce a de facto monolith behind a services facade unless the team is actually split along the intended service boundaries first.

## Source
MacCormack, A., Baldwin, C. and Rusnak, J. (2012), "Exploring the duality between product and organizational architectures: A test of the mirroring hypothesis", Research Policy 41(8), 1309-1324. Nagappan, N., Murphy, B. and Basili, V. R. (2008), "The Influence of Organizational Structure on Software Quality", Proceedings of ICSE 2008.
