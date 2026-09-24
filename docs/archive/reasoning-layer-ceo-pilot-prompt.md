# Claude Code Prompt — Wire in the Reasoning Layer (CEO only, pilot)

Run this in the water repo. Scope is CEO only — do not touch coo/cto/design yet, this is a
pilot to validate the approach before rolling it to the other three roles.

## Before changing anything, read these to understand the existing mechanics precisely

- `internal/persona/persona.go` — the `Persona` struct, `Load()`, `Render()`, and `loadDoc()`.
- `internal/identity/identity.go` — how `content_hash` and `role_id` verification actually
  works. Do not hand-write a `content_hash` value by guessing; find and use whatever stamping
  mechanism already exists (check `internal/cli/cmd_persona.go` — there is very likely a
  `water persona` subcommand built for exactly this kind of file-authoring workflow; use it if
  it exists rather than reinventing it).
- `internal/agent/node.go` — specifically the `Assemble()` function's identity line, and the
  `ceoNode()` function's task strings.

## Change 1 — add `reasoning.md` as a fourth persona document, loaded and rendered exactly
like Soul and Experience already are

In `persona.go`:
- Add a `Reasoning Doc` field to the `Persona` struct, alongside `Soul` and `Experience`.
- In `Load()`, load it the same way Soul/Experience are loaded (tolerate a missing file as
  blank, same as the existing pattern — a role should still work if this file isn't written
  yet).
- In `Render()`, render a `# Reasoning` section. It must render AFTER `# Experience` and
  BEFORE the skill sections, because the reasoning content below explicitly refers back to
  "the Experience section above" — the ordering is load-bearing, not cosmetic.

## Change 2 — write `agents/ceo/reasoning.md`

Use the existing doc frontmatter schema (look at `agents/ceo/soul.md` for the exact format:
`schema`, `role`, `kind`, `status`, `role_id`, `file_type`, `content_hash`). Set `role_id` to
match the CEO's existing `role_id` from `role.yaml` (`c1a4b6e2-0f3d-4b7e-9a21-6d0e8f2c4a10`).
Compute `content_hash` correctly using whatever mechanism `internal/identity` actually expects
— do not guess.

Body content (adapt lightly for tone consistency with the existing `soul.md`, but keep the
seven-step structure and the two closing rules intact — they are the actual fix, not
decoration):

```
Before answering, walk through this, briefly — every time, even when it feels obvious:

1. What's the bare situation, stripped of interpretation?
2. How does a CEO read it — threat, challenge, opportunity? Which goal is at stake?
3. Check the Experience section above: does any lesson's trigger-shape match this situation?
   If yes, say so and say why it applies — or why it's close but doesn't quite fit, if that's
   the case. If nothing matches, say that plainly too. Do not silently skip this check.
4. Given that reading, what few options are actually live — not the full theoretical space,
   the bounded set you'd actually be choosing between?
5. Which option, and what outcome are you actually pursuing?
6. Does your stated reason match the deeper driver, or is there a gap worth naming? People
   (and this reasoning) can be wrong about their own "why."
7. State the call, the tradeoff you're accepting, and what would make you reverse it.

If asked directly why you hold a view, trace it to the Experience section above — the pattern
itself, not a source. You do not have access to which real companies or cases these lessons
were drawn from; reason from the lesson's content, not from anything outside what you can see.
```

## Change 3 — the identity-framing correction in `node.go`

In `Assemble()`, the current line is:

```go
sb.WriteString(fmt.Sprintf("You are %s, the %s role-agent in the water system.\n", role.Name, role.Slug))
```

Change this to state reasoning-application, not identity-assumption. The exact wording is your
call within this constraint: it must NOT say "you are the CEO" (identity claim) — it must say
something equivalent to "you reason like a CEO" / "you apply CEO-shaped reasoning" (method
application), while still clearly identifying the role slug for the rest of the system to key
off of. This is not cosmetic — it's the same emergence-not-impersonation principle already
enforced in `soul.md`'s own opening line ("not a person you are imitating, but a role you
occupy") and in the honesty clause; this line in `node.go` was the one place that principle
was never actually applied. Suggested wording, adjust only if there's a clear reason:

```go
sb.WriteString(fmt.Sprintf("You reason like a %s — not playing a character, but applying the reasoning pattern that role calls for, as the %s role-agent in the water system.\n", role.Name, role.Slug))
```

## Verification before declaring this done

1. Confirm the repo still builds (`go build ./...` or whatever this project's actual build
   command is — check `Makefile` or `README.md` if unsure).
2. Run the existing test suite, especially `internal/persona/persona_test.go` — a new required
   field could break existing fixtures that don't have a `reasoning.md`; if so, confirm the
   "tolerate missing file as blank" fallback actually holds rather than failing loudly.
3. Run one live interactive turn as CEO with a real, non-trivial scenario (not a meta-question
   like "what model are you" — an actual messy business situation) and check whether the
   response visibly shows the seven-step structure working — in particular, whether it
   references an Experience lesson by its content when one plausibly applies, without ever
   naming a real company. Paste that transcript back for review rather than declaring success
   from the code compiling alone.

Do not extend this to coo/cto/design in this pass. Report back what changed and the test
transcript.
