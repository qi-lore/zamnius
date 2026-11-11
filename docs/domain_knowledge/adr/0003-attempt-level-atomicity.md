# ADR-0003: Attempt-level atomicity for resume

**Status**: accepted · 2026-11-11

## Context

Recoverability is the highest-priority principle, but "resume from the exact point of interruption" admits several granularities: continue a partially completed agent conversation, checkpoint every model turn, or treat each step attempt as all-or-nothing. The choice determines the persistence format, the number of recovery paths, and how much in-flight work survives a crash.

## Decision

A step attempt commits atomically. On resume, an attempt whose outcome never committed restarts from the phase's recorded inputs; the restart is recorded with its cause, and side-effecting actions carry idempotency keys so a repeated attempt cannot duplicate them.

## Consequences

- One recovery rule covers every interruption point, which is simple to test and to reason about.
- In-flight model progress inside an interrupted attempt is lost by design; the audit shows the restart and why.
- Continuing a partial conversation remains possible later, but it would be a second, explicitly recorded recovery path rather than an implicit behaviour.
