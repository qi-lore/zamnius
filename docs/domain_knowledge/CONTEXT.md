# Domain Context: Zamnius

Canonical entry point for project terminology and domain boundaries. Keep this file a short,
declarative glossary; put design history in `adr/` and feature behaviour in specs.

## Core concepts

- **Workflow definition** — the declared, ordered phases with their agents, join rules, budgets, retries, timeouts, and failure policies. _Avoid_: pipeline, graph.
- **Run** — one execution of a workflow definition, with an owner, a position, and a terminal outcome. _Avoid_: job, execution, instance.
- **Phase** — one ordered step inside a run. A phase either runs one agent or fans out to several. _Avoid_: stage, node, task.
- **Branch** — one parallel lane inside a fan-out phase. _Avoid_: lane; never use "branch" for a git branch, which is always called a **git branch**.
- **Step attempt** — one agent execution at a step, numbered, with its prompt version, tool activity, and outcome. _Avoid_: try, invocation.
- **Session** — the context thread of exactly one agent instance within a run; holds that instance's short-term memory and its own resume point. Sessions are isolated from each other. _Avoid_: conversation, thread, context window.
- **Agent instance** — a running participant bound to a role, a session, and a model choice.
- **Workspace** — an isolated working area assigned to one agent's work. A **git worktree** is the git-backed kind of workspace; the general concept is always "workspace". _Avoid_: sandbox, checkout.
- **Artifact** — a produced output with identity, fingerprint, and provenance pointing at the producing step attempt. _Avoid_: output, result, file.
- **Checkpoint** — the last committed consistent state from which a run resumes.
- **Event** — an immutable, ordered record of something that happened, carrying actor, time, and cause.
- **Decision record** — an immutable record of a non-trivial choice and its rationale; the reason a branch was retried, a phase escalated, or a result was accepted.
- **Run ownership lease** — the time-bounded right of one process on one host to advance a run. Takeover of an expired lease is recorded.

## Boundaries

- **Recoverability** is a property of persisted artifacts, not of a live process. Anything a resumed run needs must be in the store; conversation history and in-memory state are never authoritative.
- **Durable memory** is project-scoped knowledge that outlives a session; **short-term memory** belongs to exactly one session and is never shared implicitly.
- **One canonical knowledge layer, with derived views.** The constitution, the feature artifacts, and this file with `adr/` are **canonical**. The wiki (`wiki/`) is a **derived synthesis** that must cite a source and never overrides a canonical artifact; `docs/memory/` is **operational memory** (worklog, bug patterns, in-flight decisions) and likewise never overrides this file or an ADR. A conflict is resolved in favour of the canonical layer, and a derived claim that hardens into a decision is promoted into an ADR. See [ADR-0008](adr/0008-one-canonical-knowledge-layer.md).
- **Untrusted input** covers model output, retrieved documents, repository files, and tool results. Trusted input is what the operator configured.
- **Development-process terms are not domain terms.** In the project's own workflow a **task** is one numbered entry in a feature's `tasks.md`, and a **review checkpoint** is the verified stop that follows it — neither is a Zamnius domain concept. "Task" is never used for a **Phase** (see above), and the domain sense of **Checkpoint** (a run's resumable committed state) is unrelated to a review stop. See [ADR-0007](adr/0007-one-task-per-turn-review-gate.md) and constitution Principle XI.
- **Deferred to later increments**: task board and role collaboration, durable and semantic memory, tool registry and version-control tools, HTTP/gRPC surfaces, A2A and AG-UI interoperability.

## Related documents

- [Specification: Zamnius Multi-Agent Framework](../../specs/001-multi-agent-framework/spec.md) — committed increment (phases, resumability, audit).
- [Product draft](../../docs/spec-draft.md) — original product baseline and open questions.
- [Constitution](../../.specify/memory/constitution.md) — non-negotiable project principles.
- [Architecture decisions](adr/) — adopted decisions and their rationale.
