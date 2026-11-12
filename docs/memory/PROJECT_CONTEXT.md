# Project Context

Last reviewed: 2026-11-12

## Product / Service

**Zamnius** — a recoverable multi-agent workflow framework in Go (`github.com/hIMEI29A/zamnius`).
An engineer declares a workflow as ordered phases, optionally fanning out to concurrent branches with a
declared join rule; the framework drives each phase, applies retries, timeouts, and budgets, commits the
artifacts each phase produced, and can resume any interrupted run from a fresh process holding only the
persisted store. Delivered as a Go library (`pkg/zamnius`) plus a minimal CLI (`cmd/zamnius`).

The committed increment covers stories **P1–P3 only**: ordered phases with fan-out/join, resumability, and
audit reconstruction. Roles, task board, messaging, memory, tools/MCP, HTTP, gRPC, A2A, and AG-UI are
deferred to later feature specifications.

## Key Constraints

- **Recoverability is the highest-priority principle** (constitution II), in two layers: the development
  process and the product. Given only persisted artifacts plus the constitution, a fresh agent must determine
  the exact state and the next correct action.
- **Spec-first and test-driven** (constitution I, VIII, X.3): durable truth lives in versioned artifacts, never
  in chat; a behaviour change updates spec → plan → tasks → code, with the failing test first.
- **Attempt-level atomicity** (ADR-0003) and the **single-host ownership lease** (ADR-0004) are load-bearing;
  weakening either requires a new ADR.
- **One task per turn, then stop for review** (constitution XI, ADR-0007).
- **No plaintext secrets** in artifacts, events, logs, or prompts (constitution V); secrets come from the
  environment (`ZAMNIUS_MODEL_API_KEY`) and are redacted before persistence.
- Single Go module; no transport imports in domain packages; no circular packages; Uber Go Style Guide
  applies without exception (constitution X).

## Important Domains

- **Workflow definition** — ordered phases, agents, join rules, budgets, retries, timeouts, failure policies.
- **Execution entities** — Run > Phase > Session > Step attempt, plus Branch for a parallel lane inside a
  fan-out phase, each with its own state machine.
- **Durable store** — a transactional single-host store holding run state, checkpoints, leases, and the
  append-only event log.
- **Artifact CAS** — content-addressed files with recorded fingerprints, human-readable for audit and export.
- **Audit** — hash-chained events, decision records, and fingerprint verification.

## Current Priorities

- Complete **US2 (resumability)** of `specs/001-multi-agent-framework`, then **US3 (audit reconstruction)**,
  then the Polish gates (coverage, secret scan, documentation, full verification sweep).

## Keep Here

- durable product constraints
- domain language and invariants
- project-wide priorities that shape feature tradeoffs

## Never Store Here

- feature-specific acceptance criteria
- task lists
- transient implementation notes
- changelog entries

Update the review date when constraints or priorities materially change.
