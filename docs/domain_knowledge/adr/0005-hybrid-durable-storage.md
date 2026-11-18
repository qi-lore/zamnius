# ADR-0005: Hybrid durable storage

**Status**: accepted · 2026-11-11

## Context

Resume correctness depends on two guarantees: a state change and its event record commit together, and ownership is enforceable. The draft left storage open ("files + event log; SQLite under consideration"). A pure file store would require hand-built torn-write safety and single-writer fencing; a pure database would make artifacts opaque to ordinary tools.

## Decision

Run state, checkpoints, ownership leases, and the event log live in a transactional single-host store. Artifacts live as content-addressed files with recorded fingerprints. Both remain behind replaceable interfaces.

## Consequences

- Atomic commits and fencing come from the store rather than from code we would have to prove correct.
- Artifacts stay reviewable and exportable with standard tools, preserving the audit story.
- A run now depends on two stores; the commit protocol must define which is written first and how an orphaned artifact is detected and reclaimed.
