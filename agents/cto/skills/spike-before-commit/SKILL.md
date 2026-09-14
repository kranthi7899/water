---
name: spike-before-commit
description: Resolves the single riskiest unknown in a high-commitment decision with a time-boxed prototype whose decision-changing result is defined in advance, treating its output as evidence rather than a deliverable. Use when high uncertainty and high commitment cost sit in the same decision and the uncertainty is concentrated in one identifiable unknown a bounded experiment could settle.
keywords:
    - spike
    - prototype
    - proof of concept
    - de-risk
    - time-box
    - vendor benchmark
    - before committing
role_id: 3b8d1f6a-9c24-4e7b-8a5d-f0e2c6b1d934
file_type: skill
content_hash: sha256:4c42adb20d2a47e34d9ede5035921a1fac4ef51a122a0cb07fdf679dd4f4a46c
---
# Spike-Before-Commit

A thinking tool. Name it in a phrase when you use it; do not turn the answer into a tutorial about it.

## When to use
- High uncertainty and high commitment cost sit in the same decision.
- The uncertainty is concentrated in one identifiable unknown that a bounded experiment could resolve.

## How to apply
1. Time-box a prototype aimed at the single riskiest unknown, not a general-purpose proof of concept.
2. Define, in advance, what result would change the recommendation. If no result would change it, the spike is not answering a real question.
3. Treat the spike's output as evidence for the decision, not as a deliverable in its own right. Throw away the code if the production path looks different.

## Evidence grade
Grade B minus. Effort reduction from prototyping is measured (a controlled multi-project comparison found roughly 40 percent less code and 45 percent less effort in prototyped projects), but that is a single, dated study measuring effort, not downstream failure avoidance, and a 2023 systematic map confirms the field still lacks a general theory of when prototyping pays off. Say both things: the effort-saving effect is real and measured; the claim that spikes reliably prevent bad commitments is inferential.

## Worked example
Constructed. Before committing to a vector store for a retrieval feature, a two-day spike measures recall and latency against the actual production corpus, rather than accepting a vendor's published benchmark numbers as a stand-in for how the system will behave on this data.

## Source
Boehm, B. W., Gray, T. E. and Seewaldt, T. (1984), "Prototyping Versus Specifying: A Multiproject Experiment", IEEE Transactions on Software Engineering SE-10(3), 290-302. Bjarnason, E., Lang, D. and Mjoberg, A. (2023), "An empirically based model of software prototyping", Empirical Software Engineering 28, article 138.
