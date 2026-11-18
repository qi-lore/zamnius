# ADR-0008: One canonical knowledge layer, with derived views

**Status**: accepted · 2026-11-12

## Context

Two SpecKit extensions are installed and initialized for this repository: **wiki** (a compounding,
LLM-maintained markdown knowledge base under `wiki/`) and **memory-md** (repository-native memory under
`docs/memory/`, plus per-feature `memory.md` and `memory-synthesis.md`). Both add a layer that describes the
project and its decisions.

The repository already has a knowledge layer, and the constitution makes it load-bearing: the
constitution itself, the spec/plan/tasks artifacts, and `docs/domain_knowledge/` — `CONTEXT.md` as the
canonical vocabulary and `adr/` as the record of architectural rationale. Principle I places durable truth in
versioned artifacts, and Principle II requires any agent to determine the current state and the next correct
action from those artifacts alone.

Two independently-written knowledge layers would be two sources of truth. The failure is concrete: a wiki page
that paraphrases a decision slightly differently, or a `docs/memory` worklog entry that records a constraint the
ADRs no longer state, gives a fresh agent contradictory answers to the same question — the exact defect
`CONTEXT.md` warns against, and a direct violation of Principles I and II.

## Decision

There is **one canonical knowledge layer**, and every other layer is a **derived view** of it.

- **Canonical**: `.specify/memory/constitution.md`; the feature artifacts (`spec.md`, `plan.md`, `tasks.md`,
  contracts, data model); and `docs/domain_knowledge/` — `CONTEXT.md` for vocabulary and `adr/` for decisions.
- **Derived, non-authoritative**: `wiki/` holds *synthesis*. Every synthesized claim carries a source
  identifier (the wiki already enforces `require_citations: true`), and a wiki page never overrides the artifact
  it cites.
- **Operational, non-authoritative**: `docs/memory/` holds working memory — worklog, bug patterns, in-flight
  decisions — and per-feature `memory.md` / `memory-synthesis.md` are its feature-scoped views.
- **Conflict rule**: where a derived or operational layer disagrees with a canonical artifact, the canonical
  artifact wins, and the derived layer is corrected. The reverse never happens silently.
- **Promotion rule**: when a memory or wiki claim hardens into a decision, it is promoted into an ADR or
  `CONTEXT.md`, which is then the place a reviewer reads.

The extension hooks that write these layers are not auto-executed (`auto_execute_hooks: false`, and the
wiki and memory-md hooks are `optional: true`), so writes happen on explicit invocation and are reviewed like
any other artifact, consistent with Principle XI and ADR-0007.

## Consequences

- A fresh agent can still resolve any question from the canonical set alone; the derived layers only accelerate
  retrieval, which is what Principle VI asks memory to do.
- Reviewers gain a single check: does a new wiki page or memory entry contradict a canonical artifact? If yes,
  that is a defect in the derived layer.
- The wiki and memory-md remain useful — synthesis, indices, and worklogs are genuinely valuable — without being
  trusted as the record of truth.
- Adds a maintenance duty: derived layers can go stale as canonical artifacts change, so `speckit.wiki.lint`
  and memory audits are maintenance, not optional polish.
- If the wiki is ever meant to *become* canonical, that requires amending this ADR and the constitution, not
  silently promoting its pages.
