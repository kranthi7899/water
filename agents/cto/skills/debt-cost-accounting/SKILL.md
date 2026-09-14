---
name: debt-cost-accounting
description: Prices a proposed or existing shortcut as a recurring cost in developer time, names which kind of debt it is, and refuses to quote a precise interest rate or future multiplier the evidence cannot support. Use when a shortcut is being proposed or paid for and the argument on the table is speed now versus cost later.
keywords:
    - technical debt
    - shortcut
    - interest rate
    - speed now
    - refactor
    - cleanup
    - pay down
role_id: 3b8d1f6a-9c24-4e7b-8a5d-f0e2c6b1d934
file_type: skill
content_hash: sha256:7729f4f646096ba04d6f77da01f6f8d5d0285cf67ce3876aeb6a98f04ece348b
---
# Debt-Cost Accounting

A thinking tool. Name it in a phrase when you use it; do not turn the answer into a tutorial about it.

## When to use
- A shortcut is being proposed, or an existing shortcut is being paid for.
- The argument on the table is speed now versus cost later.

## How to apply
1. State the recurring cost in developer time, not as an unquantified metaphor: "roughly a fifth of ongoing work on this surface" rather than "this is expensive".
2. Name which kind of debt it is (architectural, requirements, code, or test and documentation debt). The underlying survey found the cost is not evenly distributed across these kinds.
3. Refuse to quote a precise "interest rate" or a specific future multiplier. The debt metaphor is directional, not a validated financial model; say that plainly rather than let a specific-sounding number imply more precision than exists.

## Evidence grade
Grade B minus. The existence and rough scale of the recurring cost is measured by survey research (43 developers in a longitudinal study lost an average of 23 percent of development time to technical debt); the "interest rate" framing has no validated model behind it. Treat 23 percent as an order-of-magnitude finding, not a number to quote verbatim in a different organisation.

## Worked example
Constructed. A proposed schema shortcut to hit a deadline is priced as "costing roughly a fifth of ongoing development time on this data surface going forward, based on the closest measured study available", with an explicit refusal to name a precise future multiplier or payback date.

## Source
Besker, T., Martini, A. and Bosch, J. (2018), "Technical Debt Cripples Software Developer Productivity", Proceedings of TechDebt 2018. Cunningham, W. (1992), "The WyCash Portfolio Management System", Addendum to the Proceedings of OOPSLA 1992.
