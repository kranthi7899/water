---
name: wcag-conformance
description: Checks a user-facing surface against the objective, pass-or-fail success criteria of WCAG 2.2 at levels A and AA, reporting each finding against a named criterion and never treating a passing automated scan as a full audit. Use for any user-facing surface being shipped or reviewed, however minor; the only tool in this role that can ground a direct escalation, and only when a named failure also creates legal exposure.
keywords:
    - accessibility
    - WCAG
    - a11y
    - contrast
    - alt text
    - keyboard
    - screen reader
    - tap target
    - conformance
    - legal exposure
role_id: 9f4c2a7d-1e58-4c3b-a6f9-2b7d0c5e8a41
file_type: skill
content_hash: sha256:41a29c491280bfcbeae5e6d71140a4bbcfd74ffd812276bfdd2bf03fbe6cad2d
---
# WCAG 2.2 accessibility conformance check

A thinking tool. Name it in a phrase when you use it; do not turn the answer into a tutorial about it.

## When to use
- Any user-facing surface being shipped or reviewed, regardless of how minor it seems.

## How to apply
1. Check contrast ratios, alt text presence and quality, label association, tap-target sizing, and keyboard operability against the WCAG 2.2 A and AA success criteria. These are pass or fail, not matters of taste.
2. Report each finding against a named success criterion (for example "1.4.3 Contrast (Minimum)"), not as a general "accessibility could be better".
3. Do not treat a passing automated scan as a full audit. Automated tools catch a subset of criteria (contrast and alt-text presence, not alt-text quality or most keyboard-operability issues).

## Escalation note
This is the only tool in this role's set that can ground a direct escalation to the CEO, and only when a named A or AA failure is also asserted to create legal exposure. A failure alone does not automatically trigger escalation.

## Evidence grade
Grade A for the standard itself: a W3C Recommendation stating objective, checkable success criteria, the one genuinely objective surface available to this role. Grade B for the WebAIM Million prevalence data cited below: real, large-N observational data, not a controlled experiment.

## Worked example
Documented. The February 2026 WebAIM Million report found 95.9 percent of home pages had at least one detectable WCAG 2 failure, up from 94.8 percent the year before and reversing six consecutive years of gradual improvement. The two highest-yield, cheapest fixes remain low-contrast text (found on 83.9 percent of pages) and missing alt text (53.1 percent). Check these first under any time constraint.

## Source
WCAG 2.2, W3C Recommendation, published 5 October 2023 (updated 12 December 2024). WebAIM Million, webaim.org/projects/million, annual automated census of the top one million home pages.
