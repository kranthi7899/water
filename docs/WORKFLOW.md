# Execution workflow for every slice

The loop is: **Explore, Plan, Approve, Implement in small tasks, Verify with evidence, Review, Wrap up, Stop.** Never skip a phase, and never start the next slice without the owner's go-ahead.

---

## Phase 1: Explore (read only)

- Work in plan mode. Make no edits.
- Delegate wide reading to a subagent so the main context stays clean. Only its findings come back.
- Read what the slice touches: the relevant packages, tests, config, the manifest, and existing docs.
- Write the findings into `docs/slices/<slice>.md` under "Findings": what exists, what must change, what is risky, what is unknown.

## Phase 2: Plan

Extend the same file with:

1. **Scope and non-scope**, in a few lines.
2. **Design:** interfaces, data schemas, file and package layout.
3. **Task list:** small tasks, each finishing in one commit, each with its own tests. Tasks are ordered by dependency.
4. **Acceptance criteria:** measurable and specific (for example "false-accept rate below X on the eval set", "gate refuses an outward action with no approved envelope", "p95 latency under Y ms"). "Works" is not a criterion.
5. **Invariant checks** this slice must not break (below).
6. **Risks and open questions**, each with a proposed default.

Then stop and wait for the owner's approval. They may edit the plan. Do not write code before approval.

## Phase 3: Implement, one task at a time

For each task:

1. Write tests from the acceptance criteria first, and watch them fail.
2. Implement the smallest change that makes them pass.
3. Run the full check: `go build ./...`, `go vet ./...`, `go test ./... -race`, plus the invariant checks.
4. Commit with a clear message, then tick the task in `docs/slices/<slice>.md`.

Rules while implementing:

- One task per commit. Do not batch.
- Fix root causes, not symptoms. When something fails, reproduce it, find the cause, fix that, and add a regression test. Do not suppress the error.
- After **two failed attempts** at the same problem, stop and report the diagnosis instead of looping.
- Bugs found outside the slice go into `docs/known-gaps.md`. Do not fix them silently unless they block the task.
- Stay on branch `feat/ceo-twin`. Do not force-push or rewrite history. Tag each finished slice (`slice-<name>-done`).

## Invariant checks (run on every task)

These are automated tests where possible:

- No connector function can run without a gate permit, and an outward (A-level) call is refused without an approved envelope.
- Untrusted content is tagged, and an action derived from it never runs autonomously.
- Reflex tiers run no shell and no subprocess, and perform no state changes.
- No metered API dependency was added. The binary still builds statically.
- No secret appears in logs, errors, test output or model context.
- The audit log still verifies.

## Phase 4: Verify with evidence

Passing unit tests is not "verified."

- Run the real thing: start the daemon, exercise the commands, run the eval or scenario harness, and capture real output and measured numbers.
- Save the evidence in `docs/slices/<slice>-verification.md`, and compare each acceptance criterion to what was actually measured.
- Live Google access stays **read-only** unless the owner explicitly approves otherwise. Never send email, create events or change anything in a real account during verification. Use fixtures or test accounts for anything else.
- Never use the owner's real data directory (`~/.water`) in tests. Use temporary directories.

## Phase 5: Independent review

Before wrapping up, start a fresh subagent as a reviewer with this brief: check the diff against the invariants, security, dead code, missing tests, unclear naming, and docs drift. Fix what it finds, then re-run the full check.

## Phase 6: Wrap up and stop

1. Update `docs/architecture.md` and `docs/known-gaps.md`.
2. Post a plain-language status: a table of what is done and **verified** (with evidence), what is running, what is next, and any decisions or trade-offs made. Separate "done, tests pass" from "verified live."
3. List anything the owner needs to decide.
4. Stop. Wait for the owner's go-ahead before the next slice.

## When to stop and ask

- A choice would break an invariant or contradict a decision in `docs/CONTEXT.md`.
- Adding a dependency, a paid service, or a network call not already declared.
- The scope needs to grow or the plan changes materially.
- Anything would touch a real account, real data, or the filesystem outside the repo.
- An acceptance criterion cannot be met.
- About to run a destructive command (deleting data, dropping tables, rewriting history).

## Context hygiene

- Keep the checklist in `docs/slices/<slice>.md` current so any new session can resume from it.
- Use subagents for exploration and review. Keep the main session for implementation.
- Clear context between unrelated tasks and between slices.
- Record decisions in docs, not only in the conversation.

## Definition of done

A slice is done when: every task is committed and ticked, all acceptance criteria are met **with recorded evidence**, all invariant checks pass, the reviewer's findings are resolved, the docs are current, and the status report is posted.
