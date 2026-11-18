# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The **public contract** is the importable surface of
`github.com/qi-lore/zamnius/pkg/zamnius`
([contracts/library-api.md](specs/001-multi-agent-framework/contracts/library-api.md)). Packages
under `internal/` are implementation and are **not** part of the contract; they may change in any
release without a compatibility note.

---

## [0.1.0] - unreleased

The first increment: ordered phases with fan-out/join, recoverability, and audit reconstruction.
The build reports `zamnius 0.1.0` (`api_version "1"`, `schema 1`); no git tag is published yet, so
`go install …@latest` does not resolve. This entry records the initial public contract set.

### Added

#### Entry point and operations

- `New(Options) (*Engine, error)` — the only constructor. Every dependency is explicit; there are
  no package-level defaults and no init-time configuration.
- `Options{ Store StateStore; Artifacts ArtifactStore; Models ModelRegistry; Logger *slog.Logger;
  Clock func() time.Time; IDSource func() uuid.UUID }` — `Store`, `Artifacts`, and `Models` are
  required and refused when missing; the rest default (logger discards, clock is the wall clock,
  ids are UUIDv4).
- `Engine` with the operations `Start`, `Resume`, `Status`, `Artifacts`, and `Verify`.

#### Workflow and result vocabulary

- `Definition` — one workflow: ordered phases, optional fan-out with a join rule, and inherited
  defaults.
- `RunResult`, `PhaseResult` — the committed outcome of a run and of one phase. A failed run still
  returns a populated `RunResult`, so the caller never loses the run id it needs to inspect or
  resume.
- `Status` — the on-demand position report, computed from the run row, its phases, and the tail of
  the event log.
- `StartOptions`, `ResumeOptions` — the per-call knobs (`MaxConcurrency`, `Observer`,
  `AllowDefinitionDrift`, and, for resume, an optional `Definition`).
- `VerifyReport` — the outcome of checking a run's event chain and every artifact fingerprint;
  returned even when verification fails, so the caller keeps the counts.

#### Durable vocabulary

- `Run`, `PhaseState`, `Attempt`, `ArtifactRef`, `Event`, `Decision`, `Lease`, `Consumption`,
  `Budget`, `Duration`.
- `RunSpec`, `AttemptCommit`, `PhaseCommit`, `RunCommit`, `RunDelta`, `EventDraft`,
  `DecisionDraft`, `JoinResult`, `BranchOutcome` — the atomic persistence units and their inputs.
- `ModelRequest`, `ModelResponse`, `Message`, `Usage`, `Role`, `StopReason` — the provider-neutral
  model vocabulary.

These are **type aliases**, not copies: a value produced by the store or the engine is the same
type an adopter names here, and a single definition keeps the store and the engine from drifting
apart.

#### Status and classification vocabulary

- `RunStatus`, `PhaseStatus`, `AttemptStatus`, `ErrorClass`, `DecisionKind`, `EventType`, `Error`.
- Constants: run statuses (`RunPending`, `RunRunning`, `RunPaused`, `RunSucceeded`, `RunFailed`,
  `RunCancelled`); phase statuses (`PhasePending`, `PhaseRunning`, `PhaseSucceeded`, `PhaseFailed`,
  `PhaseDegraded`, `PhaseCancelled`, `PhaseSkipped`); attempt statuses (`AttemptRunning`,
  `AttemptSucceeded`, `AttemptFailed`, `AttemptAbandoned`); error classes (`ErrClassTransient`,
  `ErrClassRateLimited`, `ErrClassAuth`, `ErrClassPermanent`, `ErrClassContextOverflow`,
  `ErrClassInvalidResponse`, `ErrClassTimeout`, `ErrClassBudgetExhausted`, `ErrClassCancelled`,
  `ErrClassInternal`); decision kinds (`DecisionRetry`, `DecisionJoin`, `DecisionFailurePolicy`,
  `DecisionBudgetStop`, `DecisionTakeover`, `DecisionSkip`, `DecisionResumeRestart`); message roles
  and stop reasons (`RoleUser`, `RoleAssistant`, `StopEnd`, `StopLength`, `StopContentFilter`,
  `StopUnknown`).
- `SchemaVersion` — the persistence format this build writes (`1`).

#### Extension contracts

- `StateStore` — the durable state port (leases, atomic attempt/phase/run commits, and the read
  side).
- `ArtifactStore` — the content-addressed artifact port (`Put`, `Get`, `Verify`).
- `ModelClient` — one model interaction, provider-neutral.
- `ModelRegistry` — resolves a provider name from a definition to a client, so a definition never
  constructs one.
- `Observer` — ordered engine facts (`OnPhaseStarted`, `OnAttemptFinished`, `OnPhaseFinished`);
  implementations must not block the engine.

#### Error vocabulary

- Sentinels matched with `errors.Is`: `ErrRunAlreadyOwned`, `ErrStaleRecord`, `ErrIntegrity`,
  `ErrInvalidDefinition`, `ErrInvalidConfig`, `ErrTerminalRun`, `ErrDefinitionDrift`,
  `ErrMigrationRefused`.
- `ClassOf(error) ErrorClass` and `HintOf(error) string` — every returned error carries a
  classification and, for configuration and integrity failures, an operator-facing hint naming the
  setting or file to fix.

### Compatibility policy

The public contract follows [contracts/library-api.md §5](specs/001-multi-agent-framework/contracts/library-api.md):

- **Additive changes** — new fields with a zero-value meaning, new interfaces — are **minor**
  versions.
- **Removing or changing an exported signature** is a **major** version.
- **Every public contract change carries a compatibility note in this changelog** (SC-016).
- **`internal/` packages are not part of the contract** and may change in any release.

Because this increment is still pre-1.0, an additive change may still land as a patch while the
surface stabilizes; the rules above describe the *intent* the contract is held to, and each change
states which rule it follows.
