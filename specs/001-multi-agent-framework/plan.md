# Implementation Plan: Zamnius Multi-Agent Framework — Core Increment

**Branch**: `001-multi-agent-framework` | **Date**: 2026-11-11 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `/specs/001-multi-agent-framework/spec.md`

**Scope committed**: stories P1–P3 only — ordered phases with fan-out/join, resumability, and audit
reconstruction (spec Clarifications, 2026-11-11). P4–P8 are follow-on feature specifications.

## Summary

Build the core of a recoverable multi-agent workflow engine in Go: a declarative workflow definition
(YAML/JSON/TOML or programmatic builders, one validation path), a phase executor with parallel branches
and deterministic joins, attempt-atomic persistence in SQLite plus content-addressed artifact files, a
single-host ownership lease, an append-only hash-chained event log with decision records, a
provider-neutral model contract with one OpenAI-compatible adapter, and a library plus minimal CLI
surface. Recovery is one rule: an attempt commits wholly or not at all, and resume restarts the
uncommitted attempt from recorded inputs with idempotent side effects.

## Technical Context

**Language/Version**: Go 1.25 minimum in `go.mod`; development toolchain verified on this host as **go1.27.3 darwin/arm64** (`/Users/aiagent/.local/go/bin/go`); CI targets the latest stable, 1.27.4 (released 2026-11-02).

**Primary Dependencies** (all versions verified 2026-11-11):
`modernc.org/sqlite v1.62.0` (pure Go, no cgo) · `github.com/goccy/go-yaml v1.19.2` ·
`github.com/pelletier/go-toml/v2 v2.4.3` · `github.com/google/uuid v1.6.0` ·
`golang.org/x/sync v0.25.0` · `go.opentelemetry.io/otel v1.47.0` · `github.com/google/go-cmp v0.7.0`.
Stdlib `encoding/json`, `log/slog`, `flag`, `crypto/sha256`, `testing`.

**Storage**: SQLite (WAL, `synchronous=FULL`, single writer connection) for runs, phases, attempts,
events, decisions, schema migrations; content-addressed filesystem store for artifacts; no external
service.

**Testing**: stdlib `testing` + `go-cmp`; scripted fake `ModelClient`; named fault-injection points at
commit boundaries; subprocess `SIGKILL` tests for genuine process death; `-race` in CI.

**Target Platform**: Linux and macOS (workstation/server); `CGO_ENABLED=0` build.

**Project Type**: Go library + CLI (single module, two importable surfaces: `pkg/zamnius`, `cmd/zamnius`).

**Performance Goals**: fan-out of 8 equal branches within 1.5× a single branch (SC-006); attempt commit
latency dominated by one fsync pair; no unbounded goroutine growth under cancellation.

**Constraints**: offline-capable test suite; no plaintext secrets in artifacts/events/logs; attempt-level
atomicity (ADR-0003); single-host ownership (ADR-0004); one host at a time; no streaming, tool calling,
embeddings, HTTP, gRPC, or roles in this increment.

**Scale/Scope**: tens of branches per run, single operator/team per workspace, runs in the hundreds per
state directory; artifacts up to a few MB each (larger files stream through the CAS).

## Constitution Check

*GATE: evaluated before Phase 0 research, re-checked after Phase 1 design.*

| Principle | Assessment | Evidence / action |
|---|---|---|
| I. Spec-first, artifact-driven, TDD | **PASS** | Plan and tasks precede code; every behaviour change ships with its failing test first. |
| II. Recoverability (highest priority) | **PASS** | Attempt-atomic commit + lease + resume protocol ([contracts/store.md](contracts/store.md) §1, §5); fault-injection suite targets SC-001–SC-003. |
| III. Explicit roles & separation | **PASS (scoped)** | Roles, board, and messaging are deferred by explicit decision; the increment has agent identity only (FR-062) and does not violate the principle. |
| IV. Observability, auditability, decision trees | **PASS** | Hash-chained events, decision records, slog, OTel behind an interface; `Verify` checks chain and fingerprints. |
| V. Security boundaries & isolation | **PASS (scoped)** | Secrets come from the environment only and are redacted before persistence; no tool execution or sandboxing exists yet, so no sandbox boundary is claimed. |
| VI. Memory & context discipline | **PASS (scoped)** | Out of scope; no context-dumping behaviour is introduced. |
| VII. Extensibility & open architecture | **PASS** | `pkg/zamnius` exposes Engine + stable contracts; implementations stay under `internal/`. |
| VIII. Quality & testing | **PASS** | Unit + integration + recoverability + audit + secrets suites; coverage gates below. |
| IX. Simplicity & discipline | **PASS** | No CLI framework, no ORM, no DI container, no speculative interfaces; complexity tracked below. |
| X. Code style & quality | **PASS** | Uber style, `context.Context` first, wrapped errors, `slog` key-value, UUID ids, no `any` without justification, no transport imports in domain packages. |

**Gate result**: PASS. Two deliberate scope reductions (roles, memory/tools) are recorded in the spec's
Clarifications and do not weaken a principle.

## Project Structure

### Documentation (this feature)

```text
specs/001-multi-agent-framework/
├── plan.md              # this file
├── research.md          # Phase 0 decisions (R1–R14)
├── data-model.md        # Phase 1 entities, states, invariants
├── quickstart.md        # Phase 1 validation scenarios
├── contracts/
│   ├── workflow-definition.schema.json
│   ├── model-client.md
│   ├── store.md
│   ├── cli.md
│   └── library-api.md
├── spec.md
├── memory.md / memory-synthesis.md
├── checklists/requirements.md
└── tasks.md             # Phase 2 output (/speckit.tasks) — not created here
```

### Source Code (repository root)

```text
cmd/zamnius/main.go            # process entry: flags, signals, exit codes; no business logic

pkg/zamnius/                   # public, versioned surface (contracts/library-api.md)
├── engine.go                  # Engine, Options, RunResult, Status
├── contracts.go               # StateStore, ArtifactStore, ModelClient, ModelRegistry, Observer
└── errors.go                  # exported sentinel errors (ErrRunAlreadyOwned, ErrIntegrity, ...)

internal/
├── app/                       # composition root: config → store → models → engine wiring
├── cli/                       # command parsing, human/JSON rendering, exit codes
├── config/                    # YAML/JSON/TOML load, precedence, validation messages
├── definition/                # definition types, builders, canonical JSON, fingerprint, validation
├── domain/                    # run/phase/attempt/artifact/event/decision types, status & error enums
├── engine/                    # phase loop, fan-out/join, retry, budgets, cancellation, resume
├── model/                     # provider-neutral contract, registry
│   └── openai/                # reference adapter (OpenAI-compatible HTTP)
├── store/
│   ├── sqlite/                # StateStore impl, migrations, lease SQL
│   └── artifactfs/            # content-addressed artifact store
├── audit/                     # event chain construction/verification, reconstruction views
├── observability/             # slog + OTel wiring, secret redaction
└── fault/                     # named fault points; no-op unless enabled in tests

test/
├── integration/               # scenarios 1, 2, 5, 7
├── recoverability/            # scenarios 3, 4 (subprocess SIGKILL, -count=10)
├── audit/                     # scenario 6
├── secrets/                   # scenario 8
├── fakes/                     # scripted ModelClient, deterministic clock/ids
└── testdata/workflows/        # hello / fanout / slow / budget-tokens / invalid

examples/workflows/            # documented samples referenced by README/quickstart
```

**Structure Decision**: Single Go module with `cmd/` + `pkg/` + `internal/`. `pkg/zamnius` is the only
adopter-facing surface; `internal/engine` and `internal/store` hold the implementation. No
`api/`-style layering: the CLI is a single adapter package, and there is no HTTP layer to isolate yet.
This keeps domain packages free of transport imports (constitution X.2) without inventing layers the
increment does not need.

## Design Decisions Carried From Research

| Area | Decision | Reference |
|---|---|---|
| Store | SQLite, pure Go, WAL, single writer | [R2](research.md) |
| Artifacts | Content-addressed, temp→fsync→rename→row | [R3](research.md) |
| Config | Strict decoders per format, documented precedence | [R4](research.md) |
| CLI | stdlib `flag`, four run subcommands | [R5](research.md) |
| Fan-out | `errgroup` + semaphore, default cap 8 | [R6](research.md) |
| Lease | Conditional UPDATE, 30s TTL, heartbeat at 10s | [R11](research.md) |
| Audit | Append-only events, sha256 chain, decision records | [R12](research.md) |
| Models | Neutral contract, OpenAI-compatible adapter, no streaming | [R9](research.md) |
| Testing | Fake model, fault points, subprocess kill | [R10](research.md) |

## Execution Sequencing (input for `/speckit.tasks`)

1. **Skeleton**: `go mod init github.com/hIMEI29A/zamnius`, package skeleton, `go build ./...` and
   `go vet ./...` green on go1.27.3.
2. **Config & definitions**: strict loaders, canonicalisation, fingerprint, validation messages
   (SC-012) — TDD from the JSON Schema.
3. **Persistence**: schema + migrations, atomic commit helper, artifact CAS, lease SQL; concurrency test
   for double acquisition.
4. **Single-phase execution**: model contract, fake client, OpenAI adapter, one attempt end-to-end with
   artifact commit.
5. **Fan-out/join, retry, budgets, cancellation**: join policies, deterministic aggregation, budget stop
   events, SIGINT pausing.
6. **Resume**: reconcile protocol, abandoned-attempt restart, definition-drift refusal, fault-injection
   suite (SC-001–SC-003).
7. **Audit**: chain verification, reconstruction view, artifact export verification, `Verify` API.
8. **Surfaces**: CLI commands + JSON envelope + exit codes; public `pkg/zamnius` API; examples.
9. **Validation**: run every quickstart scenario, measure SC-006, confirm secret scan, close coverage.

## Testing Strategy

| Layer | What it proves | Gate |
|---|---|---|
| Unit (`internal/definition`, `internal/config`, `internal/engine` decisions) | validation, canonicalisation, retry/budget arithmetic, join rules | `go test ./...` |
| Integration (`test/integration`) | phase ordering, fan-out concurrency, artifact provenance, config parity, budget stop | all green, `-race` |
| Recoverability (`test/recoverability`) | SC-001–SC-003 at four interruption points; concurrent resume refused; SIGKILL restart | `-count=10`, zero flakes tolerated |
| Audit (`test/audit`) | event completeness (SC-005), chain detection of tampering, fingerprint verification | all green |
| Secrets (`test/secrets`) | no key material in events, artifacts, logs (SC-014) | zero findings |

Coverage targets: `internal/engine`, `internal/store/sqlite`, `internal/audit` ≥ 80% statements; no
package below 60%. Coverage is a floor, not a substitute for the scenario tests.

## Risks & Mitigations

| Risk | Mitigation |
|---|---|
| SQLite write serialisation throttles wide fan-out | short transactions (one per attempt commit), writes batched per attempt, default cap 8, measured by SC-006 |
| SIGKILL tests flake in CI | deterministic fault points for boundary logic; subprocess kill test repeated 10× with generous timeouts; failures print the event timeline |
| Provider variability breaks structured output | request `json_schema`, fall back once to `json_object`, always validate locally; `invalid_response` is terminal and recorded |
| Scope creep toward P4–P8 | plan and tasks limited to P1–P3; anything else becomes a new feature spec |
| No Go toolchain in CI before the first build | `go build ./...` runs as task 1 (verified locally on go1.27.3), so a broken module setup fails before any feature work |
| Two stores (DB + files) drift | files written before rows; orphan sweep; `Verify` cross-checks rows against content |
| Leaked secrets through tool output | no tools in this increment; redaction applied before event persistence and asserted by `test/secrets` |

## Complexity Tracking

| Complexity | Why needed | Simpler alternative rejected because |
|---|---|---|
| Two persistence mechanisms (SQLite + CAS files) | Transactional state/leases plus human-inspectable artifacts | DB-only makes artifacts opaque, weakening audit/export (ADR-0005) |
| Hash-chained event log | Tamper evidence required by US3 scenario 3 | Plain append-only table gives no evidence of out-of-band edits |
| Fault-injection harness (`internal/fault`) | SC-001 requires interruption at exact commit boundaries | Simulated errors cannot reach the post-rename/pre-commit window |
| Attempt reconciliation on resume | One recovery rule keeps every interruption path testable | Continuing partial conversations is a second recovery path (ADR-0003) |

No other unjustified complexity: no CLI framework, no ORM, no plugin loader, no message bus.

## Post-Design Constitution Re-check

Re-evaluated after Phase 1 artifacts: **PASS**. The added contracts introduce no transport layer in
domain code, no global state, and no new dependency beyond those listed. The two-store design and the
hash chain are the only added complexity and are justified above. Security scope remains honest: the
increment claims no sandbox because it executes no tools.

## Handoff State (for a fresh session)

- **Present**: `spec.md` (grilled, 12 clarifications), `plan.md`, `research.md`, `data-model.md`,
  `contracts/` (5 files), `quickstart.md`, `checklists/requirements.md`, feature memory files,
  `docs/domain_knowledge/CONTEXT.md`, ADRs 0001–0006, root `AGENTS.md`.
- **Not yet created**: `tasks.md` (next artifact, `/speckit.tasks`) and all source code.
- **Branch**: `001-multi-agent-framework`; `main` equals `dev`; nothing has been pushed.
- **Toolchain verified**: `go1.27.3 darwin/arm64`; `go build`, `go vet`, `go test -count=1`,
  `go test -race`, and `CGO_ENABLED=0 go build` all pass (see [research.md](research.md) R1 for the
  quarantine caveat).
- **Resume order**: `AGENTS.md` → `spec.md` → this plan → `research.md` → `contracts/` → `quickstart.md`.

## Out of Scope (explicit)

Roles, task board, messaging, memory (short/long term, RAG, semantic cache, vector/graph), tool
registry, MCP, git/worktree tools, prompts management UI/versioning beyond recording the version used,
HTTP, gRPC, A2A, AG-UI, streaming, embeddings, packaging/containers, backups and archival.

## Completion Criteria for This Plan

- [x] Technical Context has no unresolved clarification
- [x] Constitution gates pass before and after design
- [x] `research.md` resolves every unknown with a decision and alternatives
- [x] `data-model.md` covers every entity, state machine, and invariant the FRs require
- [x] Contracts exist for the definition schema, model access, persistence, CLI, and library surface
- [x] `quickstart.md` gives runnable validation for each committed story
- [ ] Implementation (next phase, `/speckit.tasks` → `/speckit.implement`)
