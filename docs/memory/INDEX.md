# Memory Index

Compact routing map for durable project memory (`docs/memory/`). Every entry is a committed markdown
file; this index only points at them, so it stays short.

> [!NOTE]
> Governance lives in `.specify/memory/` (constitution, workflow); canonical project knowledge lives in
> `docs/domain_knowledge/` (`CONTEXT.md`, `adr/`). This layer is **derived** and never overrides either:
> a claim that hardens into a decision is promoted to an ADR, which is then the place a reviewer reads
> (ADR-0008).

## Architecture

Empty by design so far: package boundaries and transport rules are stated canonically in
`specs/001-multi-agent-framework/plan.md` and the ADRs, and a derived copy would only duplicate them.
Add an entry here when a boundary is learned *from an incident* rather than chosen up front.

## Bugs

- [`2026-11-18-fanout-timing-flake.md`](bugs/2026-11-18-fanout-timing-flake.md) — the SC-006 overlap
  test failed CI at 1.51× while the branches overlapped: fixed cost vs branch delay, and the mutation
  check that keeps the bound honest.

## Decisions

- [`2026-11-18-verify-install-claims-against-the-tag.md`](decisions/2026-11-18-verify-install-claims-against-the-tag.md)
  — distribution claims (`go install …@tag`) are proved from an empty `GOBIN`, not inferred from a tag.

## Workflow

Empty by design. The worklog is a ledger of durable lessons, **not** a changelog: releases, version
bumps, and implementation summaries belong in `CHANGELOG.md`, and the worklog template rejects them by
name. Records are written by the capture flow when it is available, and by direct edits under explicit
user approval when it is not (no MCP server or `/speckit.*` command is configured in this repository's
sessions).
