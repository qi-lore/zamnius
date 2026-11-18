# ADR-0007: One task per turn, with a mandatory human review gate

**Status**: accepted · 2026-11-12

## Context

The implementation agent works from `tasks.md`, whose 68 tasks are already review-sized. Nothing in the process,
however, forced a stop between them: a single agent turn could carry a whole phase — several tasks, one commit —
to completion. The resulting changeset is too large to review line by line, so review degrades into a glance and
the gate becomes nominal.

The project's own constitution makes this a correctness problem, not a style preference. Principle II.1 requires a
fresh agent to resume the development process from the exact point it was left, and Principle IV requires the
record of what happened to be inspectable. A batch that is never reviewed, or reviewed only as a whole, leaves no
reliable record of what was accepted at which point, and no place for a reviewer to intervene between decisions.

The alternatives were: batch by user story or phase (fewest interrupts, unreviewable diffs); batch by
commit-sized group (better, but the boundary is the agent's judgement rather than a fixed unit); or one task per
turn with a mandatory stop (smallest reviewable unit, highest interaction cost).

## Decision

Work proceeds **one `tasks.md` task at a time**, where a task is a single numbered entry — not a phase, not a
story, not a batch. Every task ends with a **mandatory checkpoint**: the agent verifies the task (its tests plus
the project's build and vet gates), records the outcome in `tasks.md` (the checkbox and the
`## Implementation status` section), reports what changed and how it was verified, and then **stops** and waits
for the reviewer's explicit go-ahead before starting the next task. Silence, a resumed session, or the agent's
own confidence is not approval. Bundling several tasks into one turn or one commit requires explicit,
per-instance human instruction.

Ratified as constitution Principle XI in constitution 0.2.0, and mirrored in `AGENTS.md` working agreement 7,
`specs/001-multi-agent-framework/tasks.md` (execution protocol), and the tasks template so future features
inherit it.

## Consequences

- Every change stays review-sized, so review is substantive and the reviewer can intervene between tasks.
- Throughput per turn drops and each turn ends idle awaiting a human — accepted deliberately, because
  unreviewable change is the more expensive failure.
- The stopping point is citable from artifacts rather than chat, so a fresh session resumes at the same task
  (Principle II.1): the checkbox plus the status section already answer "done, in review, or next?".
- The unit of accountability is fixed and unambiguous: one task, one report, one go-ahead.
- Applies to the development process only. The framework's runtime fan-out/join, leases, and attempt atomicity
  describe how *Zamnius* executes user workflows and are unaffected.
- Adds interaction overhead on the critical path of every future increment; a project that no longer values
  review-at-task-granularity must amend the constitution, not quietly ignore this ADR.
