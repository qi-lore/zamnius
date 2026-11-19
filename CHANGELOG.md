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

## [Unreleased]

Follow-ups to the 0.1.0 increment. Applying
[ADR-0010](docs/domain_knowledge/adr/0010-two-configuration-surfaces.md), the two configuration
surfaces now share one decoder and one definition per shared default, and no setting is accepted and
then ignored; the command line also gains the verification command the library already had.

### Added

- `zamnius run verify <run-id>` (**additive — minor**): recomputes a run's event chain and re-hashes
  every committed artifact against its recorded fingerprint, without resuming the run. It is the
  command-line counterpart of the library's `Verify`, so every core operation is now available on both
  surfaces (SC-011); a mismatch exits `6` and names the sequence or digest that disagrees.
- `Options.LeaseTTL time.Duration` in `pkg/zamnius` (**additive — minor**): the ownership window, with
  zero selecting the 30-second default. The engine always accepted it; only the library surface did not.
- The process setting `lease_ttl` — a duration such as `45s`, from a config file or
  `ZAMNIUS_LEASE_TTL`, minimum `1s`, default `30s`. A shorter window recovers faster from a killed
  process, a longer one tolerates a slower machine.

### Changed

- **A workflow definition that sets `model.base_url` is now refused** (definition format change). The
  key was accepted and silently ignored since 0.1.0 — the endpoint came from `ZAMNIUS_MODEL_BASE_URL`
  whatever the file said — so the refusal names that variable. A snapshot written by 0.1.0 may still
  carry the field, and such a snapshot is still decoded, so a run started then still resumes.
- `agent.model.base_url` removed from
  [`contracts/workflow-definition.schema.json`](specs/001-multi-agent-framework/contracts/workflow-definition.schema.json).

### Fixed

- A duplicate key in a JSON workflow definition is refused instead of silently keeping the last value.
  `internal/strict` is now the one decoder for both surfaces, so a document refused as a
  process-settings file cannot be accepted as a definition.
- An unknown key in a TOML document now names the key in the hint; the library's strict-mode error
  reported only that "fields in the document are missing in the target struct".
- The concurrency default is defined once (`domain.DefaultMaxConcurrency`) and aliased by the engine
  and the process settings alike, so the two can no longer drift apart.

---

## [0.1.0] - 2026-11-18

The first increment: ordered phases with fan-out/join, recoverability, and audit reconstruction.
The build reports `zamnius 0.1.0` (`api_version "1"`, `schema 1`) and the release is tagged `v0.1.0`,
so `go install …@latest` resolves from this release on. This entry records the initial public
contract set.

### Compatibility note

The import path is `github.com/qi-lore/zamnius`. It was renamed from `github.com/prompt-fu/zamnius`
before any tag was cut: the exported surface is unchanged by the rename, no released path existed to
break, and a pre-release importer updates its imports. Once a version is tagged, moving the path
falls under the **major** rule in the policy below.

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
