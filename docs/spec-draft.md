# Specification: Multi-Agent Framework on Go

**Status**: Draft  
**Created**: 2026-11-11  
**Related Constitution**: specify/memory/constitution.md  

## 1. Overview

We are building a production-grade multi-agent framework written in Go. Project name - Zamnius. The framework provides orchestration of specialized agents, a workflow engine with phases and fan-out/join, rich memory systems, messaging, persistence, and full resumability.  

All concepts (agents, sessions, messaging, worktrees, tools) are native to the framework.

Primary audience: open-source community and engineering teams.

## 2. Goals

- Enable reliable multi-agent workflows (Planner → Coder → Tester → …) with parallel execution.
- Provide a first-class workflow engine that supports ordered phases, fan-out, join, retries, budgets, and artifacts.
- Guarantee that any interrupted process can be resumed by any agent / any model from the exact point of interruption.
- Offer rich memory (short-term + long-term), RAG, semantic cache, vector storage, and graph workflows.
- Support tools, MCP, git operations, and worktrees.
- Expose the system through CLI, HTTP API, and gRPC with flexible configuration (YAML/JSON).

## 3. Non-Goals (for now)

- Tight coupling to any specific LLM provider.
- Replacing general-purpose agent frameworks (LangGraph, CrewAI, etc.) in every domain — the focus is reliable engineering workflows and high recoverability.
- Perfect MVP scope definition (to be refined during planning).

## 4. Core Concepts

### 4.1 Roles (built-in)

- **Orchestrator** — top-level coordinator, decides which agents/phases to run.
- **Project Manager** — maintains task board, priorities, progress, and high-level status.
- **Planner** — produces plans, breaks work into tasks, defines interfaces and constraints.
- **Researcher** — gathers information, explores codebases, performs external research.
- **Coder** — implements code changes according to the plan.
- **Tester** — writes and runs tests, classifies failures, provides evidence.

Roles can be extended; the framework must support custom agents.

### 4.2 Workflow Engine

- Workflows consist of ordered phases.
- A phase can run one agent or fan-out to multiple agents in parallel and join results.
- Each phase produces artifacts.
- Support for retries, budgets, timeouts, and failure policies.
- Full persistence of workflow state so runs are resumable and auditable.

### 4.3 Communication & Shared State

- Messaging between agents.
- Shared task board / work queue.
- Shared memory (in-memory + persistent).
- Event log (append-only) and decision trees for audit.

### 4.4 Memory System

- Short-term (session-scoped) and long-term (persistent) memory.
- RAG support.
- Embedded / pluggable vector database.
- Semantic cache.
- Safe context compression and summarization.
- Graph-oriented memory / workflow representation.

### 4.5 Prompt System

- Unified prompt management with templates.
- Structured prompts (sections, variables, constraints).
- Versioning and auditability of prompts.

### 4.6 Sessions, Worktrees & Git

- First-class session control and isolation.
- Worktree support for concurrent isolated work.
- Native git operations (status, diff, commit, branch, etc.) as tools.

### 4.7 Tools & Extensibility

- Pluggable tool system.
- MCP support.
- Support for the **A2A** (Agent2Agent) protocol for standardized inter-agent discovery, task delegation, and communication.
- Support for the **AG-UI** (Agent-User Interaction) protocol for connecting agents to user-facing applications via event streams.
- Clear interfaces for custom tools, memory backends, and agents.

## 5. Technical Requirements

- Language: Go 1.25+
- External dependencies are allowed and expected.
- Interfaces:
  - CLI
  - HTTP API
  - gRPC
- Configuration: YAML, JSON
- Persistence: files + event log; SQLite is under consideration (to be decided later).
- Testing: unit tests + integration tests for critical paths (orchestration, persistence, resume, messaging).

## 6. Quality Attributes (MUST)

- **Recoverability** — highest priority. Any agent must be able to resume from persisted state.
- **Observability** — structured logs, traces, metrics, decision trees.
- **Security** — isolation boundaries, sandboxed execution where needed, no plaintext secrets in artifacts.
- **Extensibility** — stable public interfaces.
- **Auditability** — every important decision and phase transition is recorded.

## 7. Success Criteria (high-level)

1. A multi-phase workflow with parallel agents can be started, interrupted, and resumed by a completely new agent instance with no prior context.
2. Core roles can collaborate through messaging and a shared task board to complete a non-trivial coding task.
3. Artifacts, event log, and decision tree allow full reconstruction of what happened and why.
4. The system is usable both as a library/SDK and through CLI / HTTP / gRPC.
5. Unit and integration tests cover the recoverability and orchestration paths.

## 8. Open Questions (to be resolved in planning)

- Exact MVP feature set and prioritization.
- Choice of vector database / embedding strategy.
- Whether SQLite becomes the default durable store or remains optional.
- Concrete budget / retry / concurrency defaults.
- Packaging and distribution model (modules, binaries, Docker, etc.).
- Naming of the project and public package path.

## 9. Next Steps

1. Review and refine this specification (`/speckit-clarify` or equivalent).
2. Produce a technical plan (`plan.md`).
3. Break the plan into tasks (`tasks.md`).
4. Implement iteratively while continuously verifying the recoverability principle.

