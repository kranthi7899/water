---
name: decision-record
description: Captures the context, options considered, decision and consequences of a choice future technical work will build on, classifies it as a one-way or two-way door, and sets the evidence bar accordingly without letting the record substitute for evidence. Use when a choice is being made that later work depends on and whose reasoning would otherwise be lost.
keywords:
    - ADR
    - decision record
    - one-way door
    - two-way door
    - reversible
    - irreversible
    - architecture decision
role_id: 3b8d1f6a-9c24-4e7b-8a5d-f0e2c6b1d934
file_type: skill
content_hash: sha256:0fd16e2b96bbd29a5c63f1f10967f2da4fb7018ffc33a8f1b4c7556f1ca0916a
---
# Decision Record with Reversibility Class

A thinking tool. Name it in a phrase when you use it; do not turn the answer into a tutorial about it.

## When to use
- A choice is being made that future technical work will build on.
- The reasoning behind it will otherwise be lost to whoever picks it up later.

## How to apply
1. Capture context, the options actually considered, the decision, and its consequences, in a short durable record kept with the code or architecture it concerns.
2. Classify the decision as reversible (a two-way door) or not (a one-way door), and let that classification set how much evidence is required before the decision is made: more for one-way doors, less for two-way ones.
3. Do not let the record substitute for the evidence itself. It documents a decision; it does not validate one.

## Evidence grade
Grade C plus overall; the reversibility-classification half is Grade C on its own. An action-research study found improved inter-team cooperation after introducing decision records and flagged that where records are stored matters to whether they get used; a separate controlled study found documentation format had no significant effect on architecture comprehension, with familiarity with the source code dominating. No study measures whether either practice improves project outcomes. Say so when the classification is the load-bearing part of a recommendation.

## Worked example
Constructed. A choice between two message-queue vendors is logged with the options considered and the decision, and classified as a one-way door because of the migration cost of switching later, which is used to justify spending two extra days on comparative evaluation before committing, rather than to claim the resulting document itself improves the odds the decision was right.

## Source
Ahmeti, B., Linder, M., Groner, R. and Wohlrab, R. (2024), "Architecture Decision Records in Practice: An Action Research Study", ECSA 2024. "A Study of Documentation for Software Architecture", Empirical Software Engineering (2023), a 65-participant format-randomised experiment.
