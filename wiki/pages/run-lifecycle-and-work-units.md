---
title: Run lifecycle and work units
type: concept
sources: [S001, S004, S005, S010]
updated: 2026-11-19
---

A workflow is an ordered list of **phases**. Executing it once is a **run**. The canonical hierarchy
is run → phase → session → step attempt, where a *session* is one agent instance's context thread and
a **branch** is a parallel lane inside a fan-out phase, not a separate unit (S005).

## Modes and joins

A phase runs in `single` mode or in `fan-out` mode with a join rule of `all`, `any`, or `n_of_m`
(S001, S010). The join emits one aggregate artifact rather than exposing partial branch results —
with an `all` rule a permanently failed branch fails the phase instead of producing a partial join
(S001).

## Run states

`pending → running → paused | succeeded | failed | cancelled` (S004). `paused` is only reachable by
interruption (signal or lease loss) and is always resumable; `failed` and `cancelled` are terminal
for execution but remain readable and verifiable (S004).

Phases have their own states, including `degraded` — the state a phase takes when its `on_failure`
policy is `continue`, so the run proceeds while the record shows the phase did not fully succeed
(S001, S004).

## Recorded choices

Anything consequential is a **decision** with rationale and evidence, kinds being retry, join,
failure policy, budget stop, takeover, skip, and resume restart (S001). Budgets (attempts, wall clock,
tokens) exist at both run and phase level, and an exhausted budget stops the phase with a decision
naming the unit that ran out (S001).

A phase timeout, retry limit, or budget is therefore not silent control flow: it is an auditable
record attached to the run, which is what makes "reconstruct what happened and why" possible from
persisted state alone (S001).
