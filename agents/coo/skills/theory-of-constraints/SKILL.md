---
name: theory-of-constraints
description: Finds the single stage that limits total throughput and subordinates every other stage to it before adding capacity anywhere. Use when work piles up in front of one stage while other stages sit idle or run ahead, and adding effort everywhere has not raised overall output.
keywords:
    - bottleneck
    - constraint
    - subordinate
    - throughput
    - queue
    - piles up
    - five focusing steps
role_id: 7e2f9c1b-5a63-4d8e-b2f4-1c9a0d7e3b52
file_type: skill
content_hash: sha256:ee4b6095f706f6c60bd45d4b281c443f88c2472246c86159d90732291ee8d44b
---
# Theory of Constraints (bottleneck subordination)

A thinking tool. Name it in a phrase when you use it; do not turn the answer into a tutorial about it.

## When to use
- Work piles up in front of one stage while other stages sit idle or run ahead.
- Adding effort everywhere has not raised overall output.

## How to apply
1. Identify the single stage that limits total output: where work queues longest, or which has the highest sustained utilisation.
2. Exploit it: remove waste at that stage first. No idle time, no low-value work entering it.
3. Subordinate everything else to it: other stages release work at the pace it can absorb, even if that leaves them underused.
4. Only then elevate it (add capacity), and re-check where the limit has moved.

## Evidence grade
Grade C, practitioner-sourced (Goldratt), trending B. The one quantitative review is based only on reported successes and found no reported failures, so it cannot estimate how often the method fails. Say so when it carries the recommendation.

## Worked example
Constructed. A release pipeline where security review clears 6 items a week while development produces 15. Hiring developers raises the queue, not releases. Subordinating means capping developer intake to review's pace and moving a developer into review preparation. Throughput changes only when review's capacity changes.

## Source
Goldratt, E. M. and Cox, J. (1984), The Goal, North River Press. Mabin, V. J. and Balderstone, S. J. (2003), "The performance of the theory of constraints methodology", International Journal of Operations and Production Management 23(6), 568-595.
