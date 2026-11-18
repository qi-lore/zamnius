# Project Constitution

**Project**: Zamnius the Multi-Agent Framework (Go)
**Version**: 0.2.0
**Ratified**: 2026-11-11
**Last Amended**: 2026-11-12

This constitution defines the non-negotiable principles that govern all work on the project.
Any agent (human or AI), using any model, MUST be able to resume an interrupted process from the exact point it was left and continue correctly.
This recoverability requirement is the highest-priority principle.

## Core Principles

### I. Spec-First,  Artifact-Driven Development & Test-Driven Development
- Durable truth lives in versioned artifacts (`constitution`, `spec`, `plan`, `tasks`, event logs, decision trees), not in chat history or ephemeral context.
- Code is a consequence of an accepted specification. If behavior must change, the relevant artifact is updated first, then the plan/tasks, then the code.
- Every significant decision, phase transition, and artifact production MUST be recorded in an append-only event log or decision tree so that the process can be audited and resumed.
- Every feature **MUST** start with a technology-agnostic specification (`spec.md`).
- TDD is mandatory: write the failing test → get approval → implement → refactor.
- Unit and integration tests **MUST** be written for every behavioural change.
- Tests are a hard merge gate. No PR without corresponding tests.

### II. Recoverability & Continuity (Highest Priority)

This principle has two mandatory layers.

#### II.1 — Recoverability of the Development Process
- Any agent (human or AI), using any model, MUST be able to resume an interrupted process of working on *this project* from the exact point it was left.
- Durable state of the development process lives in versioned artifacts (`constitution`, `spec`, `plan`, `tasks`, event logs, decision records, etc.).
- Chat history and ephemeral context are never the source of truth.
- Partial or failed agent turns MUST leave the repository and artifacts in a consistent, reconstructible state.

#### II.2 — Recoverability as a Product Capability
- The multi-agent framework itself MUST support full persistence and resumability of:
  - workflows and phases,
  - agent sessions,
  - task boards / work queues,
  - short-term and long-term memory,
  - messaging and shared state.
- At any moment a new agent instance (possibly with a different model) MUST be able to reconstruct the current runtime state from persisted artifacts and continue without loss of progress or context.
- Partial failure of a phase or agent MUST leave the system in a consistent, recoverable state.
- This capability is a core product requirement, not an optional nice-to-have.

**Acceptance criterion (applies to both layers)**:

Given only the persisted artifacts and the current constitution, a fresh agent with no prior context MUST be able to determine the exact state and the next correct action.

### III. Explicit Roles & Separation of Concerns

- The framework ships with a clear set of core roles:
  **Orchestrator**, **Planner**, **Coder**, **Tester**, **Researcher**, **Project Manager**.
- Roles have well-defined responsibilities, allowed tools, and communication patterns.
  An agent MUST NOT perform work outside its role without explicit delegation.
- Parallel execution, fan-out/join, and hierarchical orchestration are first-class concepts.

### IV. Observability, Auditability & Decision Trees

- Every agent action that influences the workflow produces an auditable record (event log + decision tree).
- Decision trees capture the reasoning path of agents for later inspection, debugging, and compliance.
- Metrics, traces, and structured logs are required for production use.

### V. Security Boundaries & Isolation

- Agents operate within strict security boundaries (filesystem, network, tool access, secrets).
- Worktrees and session isolation are preferred mechanisms for concurrent work.
- Untrusted or user-supplied content is never executed without sandboxing or explicit approval.
- Secrets and credentials are never stored in artifacts or prompts in plaintext.

### VI. Memory & Context Discipline

- Agents have both short-term (session) and long-term (persistent) memory.
- Context windows are treated as a scarce resource. Safe compression, summarization, and semantic caching are mandatory capabilities.
- RAG, vector storage, and graph-based memory are supported and preferred over dumping full history into prompts.

### VII. Extensibility & Open Architecture

- The framework is designed for open-source use and team collaboration.
- Core interfaces (Agent, Tool, Memory, Workflow, Orchestrator, Prompt Template) MUST be stable and documented.
- External dependencies are allowed when they provide clear value; the project does not artificially restrict itself to the standard library.
- Support for CLI, HTTP API, gRPC, and configuration via YAML / JSON / TOML is required.
- The framework SHOULD support emerging open agentic protocols (A2A for agent-to-agent, AG-UI for agent-to-user) alongside MCP.

### VIII. Quality & Testing

- Unit and integration tests are mandatory for core packages.
- Critical paths (orchestration, persistence, resume, messaging) MUST have integration coverage.
- Changes that break recoverability or introduce non-deterministic behavior without explicit justification are rejected.

### IX. Simplicity & Discipline

- Apply **SOLID**, **DRY** and **KISS** rigorously.
- Prefer the simplest solution that satisfies the current requirements (YAGNI).
- Complexity **MUST** be justified in the plan or in a design decision record.

### X. Code Style & Quality

- Follow the [Uber Go Style Guide](https://github.com/uber-go/guide/blob/master/style.md) without exception.
- All public APIs and important internal functions **MUST** have clear, purposeful names and comments explaining *why*, not *what*.
- Error handling: always wrap errors with context (`fmt.Errorf("...: %w", err)`).
  Never return bare errors from deeper layers.

#### X.1 Must Always

- Use `context.Context` as the first parameter of every function that can block or call external services.
- Wrap every error with contextual information.
- Prefer `uuid.UUID` (or equivalent strong ID type) for entity identifiers.
- Keep monetary and precision-sensitive values as integers in the smallest unit.
- Write structured logs with `slog` (key-value pairs, no string interpolation of sensitive data).
- Keep packages focused and independently testable.

#### X.2 Must Never

- Never put business logic in HTTP handlers.
- Never import delivery/transport layers from domain or application layers.
- Never use interface{} / any without a strong, documented justification.
- Never commit secrets, .env files or API keys.
- Never skip error wrapping.
- Never introduce circular package dependencies.
- Never target main directly from a feature/task branch.

#### X.3 Testing Standards

- TDD is mandatory.
- Unit tests for pure logic and domain rules.
- Integration tests for gateway ↔ upstream providers, embeddings, and tool-calling flows.
- Coverage expectations will be defined per feature in the plan; critical paths require high coverage.
- Tests **MUST** be deterministic and runnable offline where possible (use mocks/fakes for external providers).

#### X.4 Git Workflow

- Branching Model
  - `main` — production-ready mainline.
  - `dev` — integration branch.
  - Feature/task branches are created only from current `main` (fetch + fast-forward first).
  - Feature branches **MUST** target `dev`.
  - Only the `dev → main` PR is allowed to merge into `main`.
- Commits
  - Single-line, descriptive messages only.
  - Strictly follow [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/).
  - No multi-line or noisy commit messages.
- Pull Requests
  - If the PR closes an issue, the description **MUST** start with: Closes: #[issue-number]
  - Provide a concise summary of the problem and the solution. Avoid boilerplate.
  - Suggest the most appropriate labels from the repository’s label set and always ask the user for confirmation before applying them.
  - Feature PRs target `dev`. Only the integration PR targets `main`.

### XI. Incremental Delivery & Human Review Gates

- Work proceeds **one task at a time**. A *task* is a single numbered entry in the feature's `tasks.md`
  (for example `T047`) — not a phase, not a user story, and not a batch. Several tasks are never executed in
  one agent turn.
- Every task ends with a **mandatory checkpoint**: the agent verifies the task (its own tests plus the
  project's build and vet gates), then reports what changed, where, and how it was verified, and then
  **STOPS** and waits for the reviewer's explicit go-ahead before starting the next task.
- The agent MUST NOT begin task *n+1* while task *n* awaits review, and MUST NOT treat silence, a resumed
  session, time pressure, or its own confidence as approval.
- The checkpoint MUST be citable from artifacts rather than chat alone: the task's checkbox and the feature's
  status section (`tasks.md` → Implementation status) record what is done, what is awaiting review, and the
  next action, so a fresh session resumes at the same stopping point.
- Changes MUST stay **review-sized**. Bundling several tasks into one turn or one commit is rejected because a
  human cannot review a large batch; this is what makes the audit and recoverability guarantees
  (Principles II and IV) hold for the development process itself.
- Proceeding in a larger batch requires explicit, per-instance human instruction (for example, "implement
  `T044`–`T046` without stopping"). The default is always one task, then stop.

## Governance

- This constitution supersedes ad-hoc instructions when they conflict.
- Amendments require an explicit edit to this file, a version bump, and a short rationale.
- Any agent working on the project MUST read the current constitution before starting or resuming work.
- The constitution itself may be refined and extended; it is not frozen.
- The dual recoverability requirement (process + product) is the primary acceptance criterion for major changes. Any change that weakens either layer requires explicit justification and a constitution amendment.
- Compliance with Principle XI is evaluated at every task checkpoint: a fresh session MUST be able to tell from `tasks.md` alone which task is finished, which is awaiting review, and what the next action is.

**Recoverability test (acceptance criterion for every major change)**:

Given only the persisted artifacts and the current constitution, a fresh agent with no prior context MUST be able to determine the exact state of the system and the next correct action.

## Amendment Log

- **0.2.0 (2026-11-12)** — Added Principle XI (Incremental Delivery & Human Review Gates), plus the checkpoint
  clause in Governance. Rationale: the deliverable must be human-reviewable, and reviewing large batch changes
  is impractical; work is therefore limited to one `tasks.md` task per turn, each ending in a verified report
  and a mandatory stop for review. This strengthens, and does not weaken, the process layers of Principle II;
  no existing principle is relaxed.
