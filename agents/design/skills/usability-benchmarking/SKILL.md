---
name: usability-benchmarking
description: Grounds any claim that a design is or is not usable in a standardised questionnaire score compared against percentile norms, uses small samples to find problems rather than certify a design, and corrects the folklore that five users find most problems. Use for any usability verdict, any pre- and post-redesign comparison, or any proposal to run or stop at a small usability sample.
keywords:
    - usability
    - SUS
    - five users
    - user testing
    - usable
    - percentile
    - sample size
    - usability score
role_id: 9f4c2a7d-1e58-4c3b-a6f9-2b7d0c5e8a41
file_type: skill
content_hash: sha256:6777f61cfbc0b3e1ec40f4aca65727799905298cafd3fab6f83e213db34f0cc5
---
# Usability benchmarking (SUS and percentile norms), with an attached folklore warning

A thinking tool. Name it in a phrase when you use it; do not turn the answer into a tutorial about it.

## When to use
- Any claim that a design "is" or "is not" usable.
- Any pre- and post-redesign comparison.
- Any proposal to run, or to stop at, a small usability sample.

## How to apply
1. Run the 10-item System Usability Scale, convert to a 0 to 100 score, and compare against percentile norms rather than an arbitrary threshold. A score around 68 is roughly the 50th percentile, not a passing grade.
2. Use small-sample formative testing (5 to 8 users) to find problems cheaply and iteratively, not to certify that a design is done.
3. State sample-size coverage honestly: a single 5-user round does not reliably find "most" problems, so plan multiple rounds rather than one and done.

## Folklore correction
Cite these, not "5 users find 85 percent": random 5-user samples in Faulkner's data ranged from 55 to 99 percent problem-detection coverage, a wide range, not a reliable 85; 10 users raised the floor to about 80 percent and 20 users to about 95. On the more complex e-commerce sites Spool and Schroeder tested, the first 5 of 49 users found only about 35 percent of problems, and severe problems were still surfacing at users 13 and 15. The gap between the two studies is itself the finding: coverage from a small sample depends heavily on interface complexity and problem-severity distribution.

## Evidence grade
Grade B for the questionnaire and its norming (large-N psychometric research, not a controlled experiment). Grade C for the "5 users find 85 percent" sample-size claim. That distinction is the load-bearing part of this entry.

## Worked example
Documented. Output should read "5-user formative testing typically surfaces somewhere between a third and nearly all of the usability problems present, depending heavily on how complex the interface is; treat it as problem-finding, not sign-off", never "we tested with 5 users so usability is validated".

## Source
Brooke, J. (1996), "SUS: A quick and dirty usability scale", in Usability Evaluation in Industry, Taylor and Francis, 207-212. Sauro, J. (MeasuringU), analysis of 500-plus SUS studies. Faulkner, L. (2003), "Beyond the five-user assumption", Behavior Research Methods, Instruments and Computers 35(3), 379-383. Spool, J. and Schroeder, W. (2001), "Testing Web Sites: Five Users Is Nowhere Near Enough", CHI 2001 Extended Abstracts.
