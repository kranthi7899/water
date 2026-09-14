---
name: fitts-law
description: Evaluates the size, distance and placement of an actionable target relative to where attention or the pointer already sits, using the measured relationship between movement time, distance and target size. Use when a decision involves acquiring a target under time or error pressure, such as the placement of a destructive action next to a safe one; not for choice-count problems.
keywords:
    - target size
    - button placement
    - destructive action
    - adjacent
    - tap target
    - pointer
    - click accuracy
role_id: 9f4c2a7d-1e58-4c3b-a6f9-2b7d0c5e8a41
file_type: skill
content_hash: sha256:a7022297a8f3f65a5aa8d534420d73e08abd28cc0ff8ac2333ee9bbffb4b71cb
---
# Fitts's Law

A thinking tool. Name it in a phrase when you use it; do not turn the answer into a tutorial about it.

## When to use
- A decision involves acquiring a target under time or error pressure: the size, distance, or placement of an actionable element relative to where attention or the cursor or finger currently sits.

## How to apply
1. Movement time to a target rises with the distance to it and falls with its size (the index of difficulty; in the Shannon form, MT = a + b log2(A/W + 1)).
2. Make frequent or high-consequence targets larger and closer to where attention already is.
3. Exploit screen and window edges: a target flush against an edge is effectively infinite in that dimension, since the pointer cannot overshoot past it.
4. Do not apply this to choices that are not a physical or spatial acquisition task. Use the choice-count tool for those.

## Evidence grade
Grade A: controlled experiment; formalised as an ISO standard.

## Worked example
Constructed. A destructive "Delete account" action placed directly adjacent to, and the same size as, a safe "Cancel" action violates this: a fast or imprecise acquisition of the safe target is one small miss away from the destructive one. Moving the destructive action away, shrinking it, or gating it behind a confirmation step addresses the actual mechanism, not just the label.

## Source
Fitts, P. M. (1954), "The information capacity of the human motor system in controlling the amplitude of movement", Journal of Experimental Psychology 47(6), 381-391. ISO 9241-9 (1998).
