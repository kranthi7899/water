---
name: littles-law
description: Sanity-checks any lead-time promise with the identity that average time in system equals items in progress divided by completion rate. Use when lead times are growing while throughput is flat, many items are open at once, or someone proposes cutting lead time without changing how much work is open.
keywords:
    - little's law
    - lead time
    - time in system
    - items in progress
    - completion rate
    - cycle time
    - turnaround
role_id: 7e2f9c1b-5a63-4d8e-b2f4-1c9a0d7e3b52
file_type: skill
content_hash: sha256:47a88d738b132f53d2ee108f6d977f8d59d90ecfe5628f98b9ff5928e4f313ad
---
# Little's Law

A thinking tool. Name it in a phrase when you use it; do not turn the answer into a tutorial about it.

## When to use
- Lead times are growing while throughput is flat and many items are open at once.
- Someone proposes cutting lead time without changing how much work is open.

## How to apply
1. Measure average items in progress (L) and average completion rate (lambda) over the same window.
2. Average time in system is W = L / lambda. Use this to sanity-check any lead-time claim.
3. At constant throughput, the only way to cut average time in system is to cut items in progress.
4. Check the stability condition before trusting the numbers: if items arrive much faster than they finish, the system is not stationary and the identity describes a moving target.

## Evidence grade
Grade A for the identity itself (a mathematical proof for stationary systems). Grade C for applying it to knowledge work, where arrivals and departures are rarely stable and "an item" is loosely defined. Both apply whenever this tool is used.

## Worked example
Constructed. A team with 60 open items finishing 10 a week has an average time in system of 6 weeks. A promise of 3-week turnaround with no change to intake or completion rate is arithmetically impossible. It needs either 30 open items or 20 completions a week.

## Source
Little, J. D. C. (1961), "A Proof for the Queuing Formula: L = lambda W", Operations Research 9(3), 383-387.
