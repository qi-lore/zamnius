# ADR-0002: A session belongs to one agent instance

**Status**: accepted · 2026-11-11

## Context

The spec used "session" both as an entity with a lifecycle and as the scope of short-term memory. Three models were plausible: one session per run, one per agent instance, or one per human conversation spanning many runs.

## Decision

A session is the context thread of exactly one agent instance inside a run. Sessions are isolated: one session's short-term memory is never readable by another. Shared knowledge flows through the task board, messaging, and project-scoped durable memory instead.

## Consequences

- Context pollution between agents is impossible by construction, and each session resumes independently.
- Persisted state is per agent rather than per run, which increases the number of recoverable units and their bookkeeping.
- A collaboration increment must add explicit shared-state mechanisms, since agents cannot lean on a common session.
