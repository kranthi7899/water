---
name: engineering-reference-class
description: Produces an engineering effort or cost estimate as a range by placing the inside-view number in the distribution of at least five comparable completed technical efforts of the same kind, with the reference class and its thinness stated explicitly. Use when estimating a migration, rewrite, integration or infrastructure build, especially when the bottom-up number feels confident.
keywords:
    - estimate
    - migration
    - rewrite
    - integration
    - effort
    - reference class
    - uplift
    - how long will it take
role_id: 3b8d1f6a-9c24-4e7b-8a5d-f0e2c6b1d934
file_type: skill
content_hash: sha256:81e98ee8059db3e6519bd2498b597cd238ea60888cae2ce5c72d7cb86d9cd5bf
---
# Engineering Reference Class

A thinking tool. Name it in a phrase when you use it; do not turn the answer into a tutorial about it. This is scoped to engineering estimates: the reference class is prior technical efforts of a comparable kind, not a project-management schedule in general.

## When to use
- A cost, time or effort estimate for engineering work that resembles past technical efforts of the same kind (a migration, a rewrite, an integration, an infra build).
- Especially when the bottom-up, inside-view number feels confident.

## How to apply
1. Identify at least five comparable completed engineering efforts of the same kind: same category of work, not just the same team or client.
2. Pull their actual outcomes against their original estimates and build the distribution of that ratio.
3. Position the current estimate in that distribution and report a range with the uplift applied, not a single number.
4. State the reference class explicitly, and flag it when thin (fewer than five cases, or cases that differ from the current work in a load-bearing way).

## Evidence grade
Grade B: the forecasting mechanism is validated by large-N observational research in infrastructure cost forecasting (258 projects; adopted into UK transport appraisal guidance in 2004). Nothing in that literature was run on software effort specifically, so the transfer is a reasoned inference. Reference-class definition (what counts as comparable) is the method's documented weak point, and it is exactly the judgment this tool asks you to show your work on. Say both when invoking it.

## Worked example
Constructed. Asked to estimate a data-warehouse migration, the role pulls three prior migrations at comparable scale that ran 1.4 to 2.3 times their original estimates, reports a range built from that distribution with roughly a 1.8 times uplift on the inside-view number, and flags explicitly that a three-case reference class is thin and that none of the three involved the same source system.

## Source
Flyvbjerg, B., Holm, M. K. S. and Buhl, S. L. (2002), "Underestimating Costs in Public Works Projects: Error or Lie?", Journal of the American Planning Association 68(3), 279-295. Cantarelli, C. C., Davis, K., Pinto, J. K. and Turner, N. (2025/26), "Reference class forecasting: promises, problems, and a research agenda moving forward", Production Planning and Control 37(7), 691-709.
