# Feature Memory: 001-multi-agent-framework

**Prepared**: 2026-11-11
**Feature**: Zamnius Multi-Agent Framework (`specs/001-multi-agent-framework/`)

## Retrieval performed

- Governance layer read: `.specify/memory/constitution.md` (v0.1.0, ratified 2026-11-11).
- Durable memory store: **not initialized in this repository** — `.specify/extensions/memory-md/config.yml` is absent, `docs/memory/` does not exist, and no `speckit_memory_*` MCP server is configured for this OpenCode session. Retrieval therefore fell back to the governance layer only.
- No prior `memory-synthesis.md` existed for this feature.

## Constraints carried from the constitution

- **Recoverability (Principle II, highest priority)**: two layers — the development process and the product itself. Given only persisted artifacts plus the constitution, a fresh agent must determine exact state and next correct action.
- **Spec-first + TDD (I, VIII)**: durable truth in versioned artifacts; failing test → approval → implement → refactor; tests are a merge gate.
- **Explicit roles (III)**: Orchestrator, Planner, Coder, Tester, Researcher, Project Manager; no out-of-role work without delegation.
- **Audit (IV)**: every workflow-influencing action produces an event-log/decision-tree record.
- **Security (V)**: isolation boundaries, sandboxing, no plaintext secrets in artifacts or prompts.
- **Memory discipline (VI)**: context is scarce; compression, semantic cache, RAG, vector and graph memory are mandatory capabilities.
- **Extensibility (VII)**: stable public contracts (Agent, Tool, Memory, Workflow, Orchestrator, Prompt Template); CLI, HTTP, gRPC, YAML/JSON/TOML configuration.
- **Style (X)**: Uber Go Style Guide, `context.Context` first, wrapped errors, `slog`, UUID identifiers, no `any` without justification, no circular packages, no transport imports in domain layers.
- **Git workflow (X.4)**: feature branches from `main`, feature PRs target `dev`, conventional single-line commits, never target `main` from a feature branch.

## Conflicts to watch

- The constitution requires TOML as a configuration option, while the draft spec states YAML/JSON. The specification follows the draft (YAML/JSON) and this discrepancy must be resolved in the plan — either TOML is added to the contract or the constitution is amended.
- The constitution demands the Uber style guide "without exception"; this constrains library and package choices in planning (for example, dependency injection style and constructor conventions).

## Settled in the grill (2026-11-11, 12 points)

Decisions are recorded in `spec.md` → `## Clarifications`, and architecturally in
`docs/domain_knowledge/adr/0001`–`0006`:

- Committed increment is stories P1–P3 only; P4–P8 become separate feature specs.
- Configuration supports YAML, JSON, and TOML (constitution VII kept).
- Run ownership is a single-host lease with recorded stale-owner takeover.
- A session is one agent instance's context thread; sessions are isolated.
- Vocabulary: run > phase > session > step attempt; "branch" = parallel lane; "workspace" is general, "git worktree" is git-specific.
- Storage: transactional store for state/checkpoints/leases/event log; content-addressed files for artifacts.
- Budgets: attempt count, wall-clock time, tokens (run and phase level).
- Model access: provider-neutral contract plus one reference adapter; fakes in tests.
- Surfaces in this increment: embedded library plus minimal CLI.
- Workflow definition: declarative file plus programmatic builders, one validation path.
- Role model in this increment: agent identity only; roles/board/messaging follow.
- Resume semantics: attempt-level atomicity plus idempotency keys.

## Remaining transient questions (plan level)

- Default values for retries, timeouts, concurrency, and lease duration.
- Packaging and distribution model.
- Run archival and export conventions (outstanding, minor).
