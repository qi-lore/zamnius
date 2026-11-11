# ADR-0001: Commit to a core-first increment

**Status**: accepted · 2026-11-11

## Context

The product baseline spans orchestration, audit, collaboration, memory, tooling, deployment surfaces, and ecosystem protocols. The constitution's YAGNI principle (IX) forbids complexity that the current requirements do not justify, while the product draft deferred MVP scoping to planning.

## Decision

Feature `001-multi-agent-framework` commits to stories P1–P3 only: ordered phases with fan-out/join, resumability, and audit reconstruction. P4–P8 are recorded as accepted direction and become separate feature specifications before implementation.

## Consequences

- A runnable increment exists at the end of this feature, rather than a half-built framework.
- The increment ships an embedded library plus a minimal command line (start, resume, status, list artifacts); HTTP, gRPC, roles, board, memory, and tools follow later.
- Anything that cannot be demonstrated for P1–P3 is out of scope now, even if it appears in the product baseline.
