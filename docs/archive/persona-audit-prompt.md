# Design-Rationale Audit — Water Agent Persona Files
### Run this in a fresh session with read access to the water repo.

## What this is, and what it isn't

This is a **design-rationale audit**, not a personality profile and not a behavior-validation
test. The question is not "what kind of person does this file describe" — it's **"why does
this already-written file have the shape it has, and does that shape hold up under scrutiny."**
Do not produce trait lists, growth suggestions, or coaching language of any kind — there is no
person here to coach, only a design artifact to examine.

## What to read

For each of the four roles (`ceo`, `coo`, `cto`, `design`), read all four files in
`agents/<role>/`: `soul.md`, `experience.md`, `role.yaml`, `.index.json`.

## What to check, per role

1. **Corroboration density.** For every entry in `experience.md`, cross-reference `.index.json`
   to count how many independent source entries back it. Flag any lesson resting on a single
   source as single-sourced, explicitly. Report the fraction of single-sourced vs.
   multiply-corroborated lessons for that role.
2. **Citation placement.** Does `.index.json` show any entries sourced to `soul.md` itself, or
   only to `experience.md`? A `soul.md`-embedded citation means foundational/trait-level
   grounding was baked into identity, not just accumulated case experience — note which roles
   have this and which don't, and why that split might make sense (or might not).
3. **Evidentiary character.** What KIND of source dominates this role's `experience.md` —
   named real-company cases, peer-reviewed academic studies, government/institutional reports,
   something else? Does that character match what the role's own `soul.md` claims to value? (For
   example: a role whose `soul.md` says grounding-over-assertion is its core job should have an
   `experience.md` that is itself rigorously grounded — check whether it actually is.)
4. **Coverage gaps.** What kinds of situations does this role's `experience.md` NOT address at
   all, based on what `soul.md` says the role is accountable for? Name the gap plainly if one
   exists; don't force a finding if the coverage is actually reasonable for the role's scope.

## What to check, across all four roles together

1. **The lesson-count gradient.** Rank the four roles by number of `experience.md` entries. Does
   the ordering make sense given what's independently knowable about how much real source
   material exists for each role's domain? State the ranking and whether it's a red flag
   (arbitrary) or expected (reflects genuine source scarcity in that domain).
2. **The corroboration-density gradient.** Same ranking exercise, but for what fraction of each
   role's lessons are multiply-corroborated vs. single-sourced. Compare it to the lesson-count
   ranking — do they track each other, or diverge in an interesting way?
3. **Unusual or self-referential sources.** Flag any citation that is structurally different from
   the rest of that role's sources — e.g., a source that isn't a real external case/study but
   something closer to the project's own design inspiration. State plainly what it is and why it
   might be a legitimate or questionable inclusion.
4. **Structural consistency.** Do all four roles follow the same file conventions (frontmatter
   schema, section headers, honesty-clause phrasing, "not a person you imitate" framing)? Flag
   any role that deviates from the pattern the other three establish.

## Output format

For each finding: the claim, the specific evidence for it (file, line-level detail where
possible), and a confidence label (High/Medium/Low) based on how directly checkable it is against
the actual files versus how much interpretation it required. End with the cross-role findings
listed separately from the per-role findings, not blended together.

## What NOT to do

Do not propose fixes, do not rewrite any file, do not suggest new lessons to add. This is a
read-only audit. If a finding suggests a real gap or inconsistency worth fixing, name it and stop
— the decision on whether and how to fix it belongs to a separate, later conversation.

## Where to save the result

Write the completed audit to `docs/persona-audit-<date>.md` in the water repo, alongside the
existing `architecture.md` and `decisions.md`.
