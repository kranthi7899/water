---
name: brooks-law
description: Checks a proposal to add people to late or tightly coupled work by classifying whether the work is partitionable and counting newcomer ramp-up and added communication paths before assuming added people shorten the schedule. Use whenever adding headcount to work that is late, or to work whose parts depend heavily on each other, is proposed.
keywords:
    - brooks
    - add people
    - late project
    - coordination overhead
    - communication paths
    - headcount
    - ramp-up
role_id: 7e2f9c1b-5a63-4d8e-b2f4-1c9a0d7e3b52
file_type: skill
content_hash: sha256:e2662d6de104b8eb30b1ee7a9a1150c69cf3aa59db2682b0d6e334abae6f9c7f
---
# Brooks's Law (coordination-overhead check)

A thinking tool. Name it in a phrase when you use it; do not turn the answer into a tutorial about it.

## When to use
- A proposal to add people to work that is late.
- A proposal to add people to work whose parts depend heavily on each other.

## How to apply
1. Classify the work: can it be split into parts that need little communication (partitionable), or does each part depend on the others (tightly coupled)?
2. For tightly coupled work, count the ramp-up time for newcomers and the added communication paths, n(n-1)/2 for n people, before assuming added people shorten the schedule.
3. Add people only where the work is partitionable and newcomers can contribute without pulling experienced people off the critical path.

## Boundary conditions
The law is strongest for late, tightly coupled work with high onboarding cost. In modular open-source projects, an empirical study found each added developer increased the odds of project success (odds ratio about 1.24), so do not apply it to partitionable, loosely coupled work.

## Evidence grade
Grade C: mixed evidence. Brooks's account comes from practitioner experience, and a large empirical study of open-source projects found the opposite relationship in that setting.

## Worked example
Documented. Brooks's account of OS/360: a late, tightly coupled system where added staff needed training and multiplied communication. Contrast the open-source projects in Schweik and colleagues, where modular work absorbed added contributors.

## Source
Brooks, F. P. (1975), The Mythical Man-Month, Addison-Wesley. Schweik, C. M., English, R. C., Kitsing, M. and Haire, S. (2008), "Brooks' versus Linus' law: an empirical test of open source projects", Proceedings of dg.o 2008.
