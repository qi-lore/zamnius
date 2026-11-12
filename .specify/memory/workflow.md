# Spec Kit Memory Workflow

This document defines the memory-first workflow for this repository. It is **subordinate to the
constitution**: where anything here conflicts with `.specify/memory/constitution.md`, the constitution wins.

## Memory Layers (Source of Truth)

Per [ADR-0008](../../docs/domain_knowledge/adr/0008-one-canonical-knowledge-layer.md) there is **one canonical
knowledge layer**; every other layer is a derived view that never overrides it.

- **Canonical — governance (`.specify/memory/`)**
  - `constitution.md` — the non-negotiable principles (authoritative)
  - `workflow.md` — this file: the memory workflow itself
- **Canonical — project knowledge (`docs/domain_knowledge/`)**
  - `CONTEXT.md` — the canonical vocabulary
  - `adr/` — architectural decisions and their rationale
- **Derived — durable project memory (`docs/memory/`)** — the historical record of technical choices,
  architecture boundaries, and recurring bug patterns. Never overrides the layers above.
  - `INDEX.md` — the compact routing map for this layer
  - `PROJECT_CONTEXT.md` — system purpose and high-level shape
  - `architecture/` — detailed boundaries and integrations
  - `decisions/` — technical and implementation decisions
  - `bugs/` — recurring bugs and mitigation patterns
  - `worklog/` — sequential ledger of durable changes
- **Derived — active feature memory (`specs/<feature>/memory.md`)** — feature-local constraints, open
  questions, and watchpoints for the current feature.
- **Ephemeral run context** — the current prompt, diff, terminal output, and temporary notes. Never committed.

## Required Workflow

### Before Starting (Specify / Plan / Tasks)
- **Proactive context**: run `/speckit.memory-md.prepare-context` (or the MCP tools, if configured) to refresh
  the local cache and surface relevant historical constraints.
- **Read synthesis**: read `docs/memory/INDEX.md` and the feature's `memory-synthesis.md` before opening raw
  durable memory files.
- **Conflict check**: do not proceed while a hard conflict with project memory or architecture boundaries is
  unresolved.

### During Implementation
- **Watchpoints**: treat implementation and verification watchpoints in `memory-synthesis.md` as requirements,
  not suggestions.
- **Isolation**: new changes must not cross trust boundaries or violate the layers recorded under
  `docs/memory/architecture/`.

### After Completion (Implement / Verify)
- **Self-learning check**: evaluate whether a systemic architectural lesson, reusable security pattern, or
  technical decision was established.
- **Formal capture**: run `/speckit.memory-md.capture` to propose durable memory and `INDEX.md` updates.
- **Human in the loop**: durable memory changes only with explicit user approval.

## Project-Specific Rules (take precedence over the generic flow above)

1. **One task per turn, then stop (constitution XI, ADR-0007).** The memory steps above are **not**
   auto-executed: `auto_execute_hooks` is `false` and the wiki and memory-md hooks are `optional: true`.
   Context preparation and capture run only when explicitly invoked, and every task still ends with a verified
   report followed by a stop for review. Memory work never bypasses that gate.
2. **Derived layers never win (ADR-0008).** A memory entry that conflicts with the constitution, a feature
   artifact, `CONTEXT.md`, or an ADR is a defect in the memory layer. A durable claim that hardens into a
   decision is promoted into an ADR, which is then the place a reviewer reads.
3. **No secrets** (constitution V). Durable memory must never contain credentials, tokens, or
   `ZAMNIUS_MODEL_API_KEY` values.

## Maintenance Rules

- Keep entries concise, durable, and reviewable in Git.
- Refuse routine implementation details or speculative memory updates.
- Keep `memory-synthesis.md` within the configured retrieval word budget.
- Fall back to markdown-first retrieval when the SQLite optimizer is unavailable.
