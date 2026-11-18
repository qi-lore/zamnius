# Memory Synthesis: 001-multi-agent-framework

**Generated**: 2026-11-11 · **Scope**: constraints and decisions relevant to planning Zamnius
**Source**: governance layer (`.specify/memory/constitution.md`); no durable memory store is initialized in this repo.

## Hard constraints (from constitution)

1. **Recoverability is the top acceptance criterion.** Applies twice: to this project's own development artifacts, and to the product (workflows, phases, sessions, boards, memory, messaging). A fresh agent with only persisted state must know the exact state and next action.
2. **Spec-first and TDD.** Behaviour changes start in the spec, then plan, then tasks, then a failing test, then implementation. No PR without tests.
3. **Roles are explicit.** Orchestrator, Project Manager, Planner, Researcher, Coder, Tester. Out-of-role actions require explicit delegation and are audited.
4. **Everything consequential is auditable.** Event log plus decision tree; decision trees carry the reasoning path, not just outcomes.
5. **Security boundaries are non-negotiable.** No plaintext secrets in artifacts, prompts, or logs; untrusted content is never executed without sandboxing or explicit approval; worktrees and session isolation are the preferred concurrency mechanisms.
6. **Context is scarce.** Compression, summarization, semantic cache, RAG, vector and graph memory are required capabilities, not optional add-ons.
7. **Public contracts are stable.** Agent, Tool, Memory, Workflow, Orchestrator, Prompt Template. Extensions must not require core modification.
8. **Go discipline.** Uber Style Guide; `context.Context` first; wrapped errors; `slog` with key-value pairs; strong ID types (UUID); integer minor units for money; no `any` without justification; no transport imports in domain; no circular packages.
9. **Git:** branches from `main`, feature PRs to `dev`, conventional single-line commits.

## Architectural boundaries implied by memory

- Domain/application layers must not import delivery layers → CLI, HTTP, and gRPC are thin adapters over the same application services. This makes the "surface parity" success criterion (SC-011) a structural property, not a test hack.
- Persistence must be behind selectable interfaces because both the durable store choice and the vector backend are still open (deferred to plan).
- Every state-changing operation needs a committed checkpoint *and* an event record; the two must not be allowed to diverge silently (this is the failure mode that breaks resume).

## Open items for the plan

- **Resolved in the grill**: configuration formats (all three now), scope breadth (core-first), storage (hybrid), ownership (single-host lease), session granularity, resume granularity, model access, surfaces, authoring model, role scope.
- **"No plaintext secrets" vs persistence**: redaction must be enforced before persistence, not at read time — this lands with the tool-registry increment, but the artifact writer must not become a bypass.
- **Complexity budget**: the constitution demands justification for complexity; the recovery design must stay one rule (attempt atomicity) rather than growing per-feature exceptions.

## Settled decisions (grilled 2026-11-11)

- **Increment scope**: P1–P3 committed (phases + fan-out/join, resumability, audit); P4–P8 separate feature specs.
- **Resume semantics**: a step attempt is atomic; uncommitted attempts restart from recorded inputs with a recorded cause; side effects carry idempotency keys.
- **Storage**: transactional single-host store for run state, checkpoints, leases and event log (state + event commit atomically); content-addressed files for artifacts.
- **Ownership**: time-bounded lease, single host, recorded stale-owner takeover. Cross-host is deferred.
- **Session**: one agent instance's isolated context thread; shared knowledge goes through board/messaging/long-term memory (later increments).
- **Model access**: provider-neutral contract (prompt, structured result, token usage, cancellation, error classes) + one reference adapter; deterministic fakes in tests.
- **Surfaces now**: embedded library + minimal CLI (start, resume, status, list artifacts).
- **Authoring**: declarative workflow files (YAML/JSON/TOML) plus programmatic builders over one validation path.
- **Budgets**: attempt count, wall-clock time, tokens; run and phase level.
- **Vocabulary**: run > phase > session > step attempt; "branch" is a parallel lane; "workspace" general, "git worktree" specific. See `docs/domain_knowledge/CONTEXT.md`.

## Defaults proposed for planning (not yet accepted)

- Single-writer ownership per run, with expiring leases and recorded takeovers.
- Checkpoint after each committed step; artifacts content-addressed and written atomically.
- Idempotency keys on side-effecting tool calls, derived from run + phase + step attempt.
- Deterministic fakes for model and tool providers so recoverability tests run offline.
