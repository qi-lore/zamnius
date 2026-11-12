---

description: "Task list for the Zamnius core increment (stories P1–P3)"
---

# Tasks: Zamnius Multi-Agent Framework — Core Increment

**Input**: Design documents from `/specs/001-multi-agent-framework/`

**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md), [research.md](research.md), [data-model.md](data-model.md), [contracts/](contracts/), [quickstart.md](quickstart.md), [constitution](../../.specify/memory/constitution.md)

**Scope**: stories P1–P3 only (ordered phases with fan-out/join, resumability, audit reconstruction). P4–P8 are follow-on feature specifications and produce **no** tasks here.

**Tests**: MANDATORY in this feature, not optional. Constitution Principle I and X.3 require TDD, and plan.md §Testing Strategy names the gates. Every implementation task is preceded by the test task that must fail first.

**Organization**: Tasks are grouped by user story so each story is independently implementable and testable.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: Which user story this task belongs to (US1, US2, US3)
- Include exact file paths in descriptions

## Path Conventions

Single Go module `github.com/hIMEI29A/zamnius`, per [plan.md](plan.md) §Project Structure:

- `cmd/zamnius/` — process entry
- `pkg/zamnius/` — public surface
- `internal/{app,cli,config,definition,domain,engine,model,store/sqlite,store/artifactfs,audit,observability,fault}`
- `test/{integration,recoverability,audit,secrets,fakes,testdata/workflows}`

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Module initialization and the build/lint gates that every later task runs against.

- [X] T001 Initialize the Go module in `go.mod` with module path `github.com/hIMEI29A/zamnius` and `go 1.26.0` (raised from 1.25.0 on 2026-11-12 because `modernc.org/sqlite v1.62.0` declares `go 1.26.0`; see [research.md](research.md) R1); confirm `go build ./...`, `go vet ./...`, and `CGO_ENABLED=0 go build ./...` succeed on go1.27.3 before any dependency is added
- [X] T002 Add the pinned dependencies to `go.mod` exactly as decided in [research.md](research.md) — `modernc.org/sqlite v1.62.0`, `github.com/goccy/go-yaml v1.19.2`, `github.com/pelletier/go-toml/v2 v2.4.3`, `github.com/google/uuid v1.6.0`, `golang.org/x/sync v0.25.0`, `go.opentelemetry.io/otel v1.47.0`, `github.com/google/go-cmp v0.7.0` — and verify `CGO_ENABLED=0 go build ./...` still passes (the SQLite driver must stay pure Go)
- [X] T003 [P] Add `.golangci.yml` enabling `errcheck`, `govet`, `staticcheck`, `revive`, `gocritic`, `gofmt`, and `goimports`, to enforce the Uber style the constitution requires (Principle X)
- [X] T004 [P] Add `.github/workflows/ci.yml` running, in order, `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `go test ./... -race -count=1`, and `CGO_ENABLED=0 go build ./...` on Go 1.27.4
- [X] T005 [P] Extend `.gitignore` with `/zamnius`, `/bin/`, and `/test/.state/` so locally built binaries and recoverability scratch state never dirty the worktree (`.zamnius/`, `*.out`, `*.test` are already ignored)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Domain model, persistence, artifact CAS, fault points, fakes, observability, and engine configuration. No user story can start until this phase is complete.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [X] T006 [P] Define `RunStatus` (`pending`, `running`, `paused`, `succeeded`, `failed`, `cancelled`), `PhaseStatus` (`pending`, `running`, `succeeded`, `failed`, `degraded`, `cancelled`, `skipped`), `AttemptStatus` (`running`, `succeeded`, `failed`, `abandoned`), and the closed `ErrorClass` set (`transient`, `rate_limited`, `auth`, `permanent`, `context_overflow`, `invalid_response`, `timeout`, `budget_exhausted`, `cancelled`, `internal`) with `String`, `Valid`, and terminal-state predicates in `internal/domain/status.go`, covered by table tests in `internal/domain/status_test.go`
- [X] T007 [P] Define the exported sentinel errors `ErrRunAlreadyOwned`, `ErrStaleRecord`, `ErrIntegrity`, `ErrInvalidDefinition`, `ErrInvalidConfig`, `ErrTerminalRun`, and the classified error type carrying `ErrorClass`, message, and a required `hint` for configuration/integrity failures ([contracts/cli.md](contracts/cli.md) §2) in `pkg/zamnius/errors.go`, with tests in `pkg/zamnius/errors_test.go`
- [X] T008 Create the entity structs of [data-model.md](data-model.md) — `Run` (including `owner_id`, `lease_expires_at`, `record_version`, `schema_version`, `spent`), `PhaseState`, `Attempt` (including `restarted_from`, `prompt_version`, `input_fingerprint`), `Artifact`, `Event` (including `prev_hash`, `hash`), `Decision`, `Lease`, `Consumption`, `Budget` — plus invariant helpers (run invariant 3: terminal status never transitions; attempt invariant 1: no committed outcome means no durable state) in `internal/domain/entity.go`, tested in `internal/domain/entity_test.go`
- [X] T009 Implement canonical JSON (`Marshal` with sorted keys, no insignificant whitespace, HTML escaping disabled) and the sha256 helpers `HashBytes`/`ChainHash(prev, payload)` in `internal/domain/canonical.go`, with tests in `internal/domain/canonical_test.go` proving byte-identical output for map key permutations and a stable digest across processes
- [X] T010 [P] Declare the public seams in `pkg/zamnius/contracts.go`: `StateStore` (`CreateRun`, `LoadRun`, `AcquireLease`, `RenewLease`, `ReleaseLease`, `CommitAttemptResult`, `CommitPhaseResult`, `CommitRunResult`, `Phases`, `Attempts`, `Events`, `Decisions`), `ArtifactStore` (`Put`, `Get`, `Verify`), `ModelClient`/`Request`/`Response`/`Usage`/`Message`/`Role`/`StopReason` ([contracts/model-client.md](contracts/model-client.md) §1), `ModelRegistry`, and `Observer` ([contracts/library-api.md](contracts/library-api.md) §3); the package must compile with `go build ./...`
- [X] T011 Implement SQLite open/pragmas (`journal_mode=WAL`, `synchronous=FULL`, `busy_timeout=5000`, `foreign_keys=ON`, `SetMaxOpenConns(1)`) and the full schema of [contracts/store.md](contracts/store.md) §3 including the `CHECK ((owner_id IS NULL) = (lease_expires_at IS NULL))` constraint and the `events_no_update`/`events_no_delete` triggers, plus the `schema_migrations` runner, in `internal/store/sqlite/db.go` and `internal/store/sqlite/migrate.go`, tested in `internal/store/sqlite/db_test.go` — including conformance test 4: `UPDATE`/`DELETE` on `events` fails at the schema level
- [X] T012 Implement the transaction protocol of [contracts/store.md](contracts/store.md) §1 and the sha256 hash chain for `events` (`hash = sha256(prev_hash || canonical(payload-with-metadata))`), plus `CommitAttemptResult`/`CommitPhaseResult`/`CommitRunResult` writing state + events + decision records + already-renamed artifact rows in one transaction, in `internal/store/sqlite/commit.go`, tested in `internal/store/sqlite/commit_test.go` — conformance test 1: an abort between commit points leaves no half-committed row and no orphan event
- [X] T013 Implement `AcquireLease` as the single conditional `UPDATE runs SET owner_id = :owner, lease_expires_at = :until, record_version = record_version + 1 WHERE id = :id AND (lease_expires_at IS NULL OR lease_expires_at < :now) RETURNING record_version` (no read-then-write), returning `ErrRunAlreadyOwned` on zero rows and appending `run.taken_over` with the previous owner and expiry evidence when taking over an expired lease, plus `RenewLease`/`ReleaseLease`, in `internal/store/sqlite/lease.go`, tested in `internal/store/sqlite/lease_test.go` — conformance tests 2 and 3, two concurrent acquisitions yield exactly one success
- [X] T014 Implement the read paths `Phases`, `Attempts`, `Events(fromSeq)`, `Decisions` and the `record_version` optimistic guard (stale writer gets `ErrStaleRecord`) in `internal/store/sqlite/read.go`, tested in `internal/store/sqlite/read_test.go`
- [X] T015 [P] Implement the content-addressed artifact store with the mandated write order — stream to a temp file on the same filesystem, compute sha256 while writing, `fsync` file, `rename` to `artifacts/<sha[0:2]>/<sha>`, `fsync` the directory — returning a descriptor and never a row; `Get` verifies the fingerprint before returning any bytes; `Put` of identical content is a no-op ([contracts/store.md](contracts/store.md) §4) in `internal/store/artifactfs/store.go`, tested in `internal/store/artifactfs/store_test.go`
- [X] T016 [P] Implement the named fault-point harness (`before_artifact_rename`, `after_artifact_rename`, `before_commit`, `after_commit`, `before_lease_renew`) that is a no-op unless explicitly enabled by a test, in `internal/fault/fault.go`, with tests in `internal/fault/fault_test.go` proving zero overhead when disarmed and exact panic sites when armed
- [X] T017 [P] Build the deterministic test doubles: scripted `ModelClient` (per-call queue of responses/errors with token usage), fixed `Clock`, counter-based `IDSource`, recording `Observer`, and a `t.Helper`-based temp state-dir builder in `test/fakes/fakes.go`, tested in `test/fakes/fakes_test.go`
- [X] T018 [P] Implement the observability bootstrap — `slog` handler selection from `ZAMNIUS_LOG_LEVEL`/`ZAMNIUS_LOG_FORMAT`, an OTel tracer/metrics facade behind an internal interface whose default is a no-op so library adopters are never forced into a global provider ([research.md](research.md) R8), and a redactor that strips `Authorization` headers, bearer tokens, and `ZAMNIUS_MODEL_API_KEY` values before any event, artifact, or log write — in `internal/observability/observability.go` and `internal/observability/redact.go`, tested in `internal/observability/redact_test.go`
- [X] T019 [P] Implement engine configuration resolution with the mandated precedence `CLI flags > ZAMNIUS_* environment > explicit --config file > discovered config (zamnius.yaml, .zamnius.yaml, zamnius.json, zamnius.toml) > built-in defaults`, strict decoding per format (`goccy/go-yaml` strict mode, `pelletier/go-toml/v2` `DisallowUnknownFields`, stdlib `encoding/json` `DisallowUnknownFields`), duplicate keys inside one file being an error rather than last-wins, and refusal messages that name the offending setting and its accepted values (FR-052) in `internal/config/config.go`, tested in `internal/config/config_test.go`

**Checkpoint**: Foundation ready — user story implementation can now begin.

---

## Phase 3: User Story 1 — Run an ordered multi-phase workflow with parallel branches (Priority: P1) 🎯 MVP

**Goal**: A workflow definition (declarative file or builder) drives ordered phases, fans out to concurrent branches with a declared join rule, applies retries, timeouts, budgets, and failure policies, commits artifacts, and is driven from the CLI.

**Independent Test**: `testdata/workflows/hello.yaml` plus `fanout.yaml` run from scratch to `succeeded`, producing the expected artifacts in declared order, with `attempt.started`/`attempt.succeeded` timestamps showing the three fan-out branches overlapping and one `aggregate` artifact listing branch results in declared order ([quickstart.md](quickstart.md) scenarios 1, 2, 5, 7).

### Tests for User Story 1 (write first, must fail) ⚠️

- [X] T020 [P] [US1] Write a ≥30-case invalid-definition table test asserting every rejection names the offending field path — covering `version != "1"`, empty `phases`, duplicate phase names, `mode=single` without `agent`, `mode=single` with `branches` or `join`, `mode=fanout` with `agent`, one branch, duplicate branch names, `rule=n_of_m` without `n`, `n` below 2 or above branch count, `n` present with `rule=all`/`any`, unknown `on_failure`, `depends_on` naming an unknown phase, `depends_on` cycles, `when.after_phase` naming an unknown phase, retry `max_attempts` outside 1–20, `multiplier` outside 1–10, non-single-unit duration such as `1m30s`, `attempts`/`tokens` below 1, empty budget, agent `instructions` exceeding 20000 chars, names violating `^[a-z0-9][a-z0-9-]*$`, `outputs` entries violating `^(plan|diff|report|evidence|aggregate|log|custom:[a-z0-9-]+)$`, branch `timeout` on a single-mode phase, unknown top-level keys, and unknown nested keys — in `internal/definition/validate_test.go`
- [X] T021 [P] [US1] Write canonicalisation and fingerprint tests proving YAML, JSON, and TOML encodings of the same workflow produce one identical `sha256` fingerprint, that key order and whitespace do not change the fingerprint, and that `defaults` inheritance produces the same resolved definition as an explicitly written one (FR-061) in `internal/definition/canonical_test.go`
- [ ] T022 [P] [US1] Write the shared adapter conformance suite of [contracts/model-client.md](contracts/model-client.md) §6 against `httptest` — successful completion populates content/usage/stop reason; `429 → rate_limited`; `401 → auth`; `500 → transient`; malformed body → `invalid_response`; missing `usage` → `invalid_response`; `response_format` rejection falls back to `json_object` and validates locally; context cancellation returns `cancelled` within the deadline plus a small grace period; no secret appears in captured log lines — in `internal/model/openai/conformance_test.go`
- [ ] T023 [P] [US1] Write integration tests for phase ordering and joins in `test/integration/order_test.go`: four ordered phases each start only after the preceding phase is terminal and each phase's artifacts exist before the next phase's `phase.started`; a failed `all` join produces no partial aggregate and names the failed branch (US1 scenario 5); a `n_of_m` join with `n=2` of 3 branches requires exactly two successes; `when.after_phase` with `outcome=failed` records `phase.skipped`
- [ ] T024 [P] [US1] Write integration tests for fan-out concurrency, retries, budgets, and provenance in `test/integration/execution_test.go`: three branch attempts with overlapping `started_at`/`ended_at` and a wall clock ≤ 1.5× a comparable single branch (SC-006); `retry.max_attempts=3` succeeding on the third attempt records three attempts and runs downstream phases exactly once; `budget.tokens` exhaustion writes `phase.budget_exhausted` naming `tokens` with a decision record stating limit and consumed amount and starts no further attempt; a `continue` failure policy records `phase.degraded`; every `artifacts` row carries `run_id`/`phase`/`attempt_id` provenance and `succeeded` requires each declared `outputs` kind to have a committed artifact
- [ ] T025 [P] [US1] Write CLI tests in `test/integration/cli_test.go` asserting the golden `--json` envelope for `run start`, `run status`, and `run artifacts` (`api_version: "1"`, `command`, `ok`, `run`, `artifacts`, `error`), human output ending with `run <id> finished: succeeded (<n> phases, <n> attempts, <n> artifacts)`, `zamnius version`, and exit `5` with a hint naming the offending field path for an invalid definition

### Implementation for User Story 1

- [X] T026 [US1] Implement the definition types (`Definition`, `Phase`, `Branch`, `AgentRef`, `ModelRef`, `JoinPolicy`, `Budget`, `Retry`, `Backoff`, `Condition`, `PhaseDefaults`) with the exact field rules and limits of [data-model.md](data-model.md) §1 and [contracts/workflow-definition.schema.json](contracts/workflow-definition.schema.json) in `internal/definition/types.go`
- [X] T027 [US1] Implement the programmatic builders (`NewDefinition`, `WithPhase`, `WithFanout`, `WithJoin`, `WithRetry`, `WithBudget`, `WithOutputs`) so both the declarative and programmatic paths converge on the same in-memory `Definition` and the same validation (FR-061) in `internal/definition/builder.go`
- [X] T028 [US1] Implement strict decoders per format — `.yaml`/`.yml` via `goccy/go-yaml` strict mode, `.toml` via `pelletier/go-toml/v2` with `DisallowUnknownFields`, `.json` via `encoding/json` with `DisallowUnknownFields`, extension-based dispatch, and a single shared strict struct set — in `internal/definition/decode.go`
- [X] T029 [US1] Implement canonical JSON encoding, the `sha256` definition fingerprint, and the immutable `definition_json` snapshot used by runs, delegating to `internal/domain/canonical.go`, in `internal/definition/canonical.go`
- [X] T030 [US1] Implement validation and resolution in `internal/definition/validate.go`: every rule in [data-model.md](data-model.md) §1, `defaults` inheritance for `retry`/`timeout`/`budget`/`on_failure` with `retry.max_attempts` defaulting to 1, `initial 1s`/`max 30s`/`multiplier 2`, `retry_on [transient, rate_limited]`, `depends_on` defaulting to the previous phase, cycle detection, and a `ValidationError` carrying the dotted field path with the accepted values so FR-052 and SC-012 messages are produced by one code path
- [ ] T031 [US1] Implement the provider registry and error-classification helpers — `Client(provider)` returning an error naming known providers, and `Classify(err)` mapping adapter errors onto the closed `ErrorClass` set — in `internal/model/registry.go`, tested in `internal/model/registry_test.go`
- [ ] T032 [US1] Implement the OpenAI-compatible reference adapter — `POST {base_url}/chat/completions` with `Authorization: Bearer $ZAMNIUS_MODEL_API_KEY` read from the environment only, `system` mapped to the system message and `messages[]` mapped in order, `max_output_tokens → max_tokens`, temperature passthrough, `response_format: {type: "json_schema", ...strict: true}` with a single `json_object` fallback plus local validation, `usage.prompt_tokens`/`completion_tokens` required (absent usage is `invalid_response`), `finish_reason` mapped to `stop`/`length`/`content_filter`/`unknown`, no internal timeout and no hidden retries — in `internal/model/openai/client.go`
- [ ] T033 [US1] Implement the phase execution loop — ordered traversal honouring `depends_on`, `when` skip conditions recorded as `phase.skipped` with a `skip` decision record, `phase.started`/`attempt.started`/`attempt.succeeded`/`attempt.failed`/`phase.succeeded`/`phase.failed`/`phase.degraded` events committed with their state change in one transaction (FR-011), and the `abort`/`continue`/`escalate` failure policies — in `internal/engine/engine.go`
- [ ] T034 [US1] Implement fan-out with `errgroup.WithContext` plus a weighted semaphore capped at 8 branches by default and overridable through `StartOptions.MaxConcurrency`, deterministic aggregation ordered by declared branch order into a single `aggregate` artifact, and the `all`, `any`, `n_of_m` join rules where a failed `all` join produces no partial aggregate (US1 scenario 5), in `internal/engine/fanout.go` and `internal/engine/join.go`
- [ ] T035 [US1] Implement retry, timeout, and backoff — `max_attempts`, exponential backoff bounded by `max` and `multiplier`, `retry_on` matched against `ErrorClass`, per-phase and per-branch `timeout` via `context.WithTimeout`, and a `retry` decision record whose `rationale` identifies the triggering condition (US3 scenario 2) — in `internal/engine/retry.go`
- [ ] T036 [US1] Implement budget accounting per [research.md](research.md) R13 — remaining allowance checked before an attempt (`attempts`, `wall_clock`) and after a response (`tokens`), exhaustion writing `phase.budget_exhausted` naming `attempts`/`wall_clock`/`tokens` plus a `budget_stop` decision record, then applying the declared failure policy, in `internal/engine/budget.go`
- [ ] T037 [US1] Implement cancellation and pause — context cancellation stops every active branch before the run reaches a terminal state (FR-008), commits the attempt boundary, and leaves the run `paused` and resumable, with no goroutine leaks, in `internal/engine/cancel.go`
- [ ] T038 [US1] Implement the public engine surface and composition root — `New(Options)` rejecting missing `Store`/`Artifacts`/`Models`, `Start`, `Status`, `Artifacts`, `RunResult` populated even when the run fails so callers keep the identifiers, `Observer` callbacks fired in order — in `pkg/zamnius/engine.go`, with the config→store→models→engine wiring in `internal/app/app.go`
- [ ] T039 [US1] Implement the CLI dispatcher and rendering — subcommand routing, `flag`-based parsing, usage errors exiting `2`, the single-object `--json` envelope with `api_version: "1"`, human output that ends with the citable `run <id> finished: <status> (...)` line, the exit-code table of [contracts/cli.md](contracts/cli.md) §3, and `zamnius version` — in `internal/cli/cli.go` and `internal/cli/render.go`
- [ ] T040 [US1] Implement the `run start`, `run status`, and `run artifacts` commands as thin adapters over the engine, mapping definition/config failures to exit `5` with a `hint` naming the offending setting, in `internal/cli/run.go`
- [ ] T041 [US1] Implement `cmd/zamnius/main.go` as process entry only — bind flags, construct the app from `internal/app`, run, map the returned error to an exit code, and leave all business logic to the engine
- [ ] T042 [US1] Add the workflow fixtures and samples — `test/testdata/workflows/hello.yaml`, `hello.json`, `hello.toml` (the same workflow three ways), `fanout.yaml` (three branches plus `all` join), `slow.yaml`, `budget-tokens.yaml`, `invalid.yaml` — plus documented copies under `examples/workflows/` with a pointer from `README.md`

**Checkpoint**: User Story 1 is fully functional and independently testable — MVP complete.

---

## Phase 4: User Story 2 — Resume any interrupted run from a fresh instance (Priority: P2)

**Goal**: Any interruption — process death, expired lease, uncommitted attempt — is resumable by a brand-new process holding only the store, without repeating committed work or duplicating recorded side effects.

**Independent Test**: Interrupt a run at a parallel branch, between phases, and mid-artifact-write, then resume from a fresh process limited to the persisted store; the run completes with results equivalent to an uninterrupted control run, zero re-executed committed steps, and zero duplicated side effects ([quickstart.md](quickstart.md) scenarios 3 and 4).

### Tests for User Story 2 (write first, must fail) ⚠️

- [ ] T043 [P] [US2] Write the fault-injection resume suite in `test/recoverability/resume_test.go` covering the four interruption points — before/after artifact rename and before/after transaction commit, through `internal/fault` — asserting for each: the resume completes, artifacts are byte-identical to the control run, completed branches are not re-executed, an `attempt.abandoned` names the interrupted attempt, a `resume_restart` decision record states the cause, and no `sha256` is committed twice for one attempt (SC-001, SC-002, SC-003); parametrise so the suite can run with `-count=10`
- [ ] T044 [P] [US2] Write the genuine-process-death harness — `TestMain` re-exec of the test binary as a child that starts a run and is killed with `SIGKILL` mid-branch, the parent then resuming in-process — in `test/recoverability/sigkill_test.go`
- [ ] T045 [P] [US2] Write ownership and idempotence tests in `test/recoverability/ownership_test.go`: two concurrent `AcquireLease` calls on one run yield exactly one success and the loser gets `ErrRunAlreadyOwned`; resuming a terminal run exits `0` and appends no events (FR-018); a resume after lease expiry records `run.taken_over` with the previous owner; resuming with a mismatched definition fingerprint is refused with an actionable diagnostic unless `--allow-definition-drift` is set, in which case the override is recorded as a decision

### Implementation for User Story 2

- [ ] T046 [US2] Implement the resume protocol of [contracts/store.md](contracts/store.md) §5 — load the run, verify `definition_fingerprint` and `schema_version`, acquire the lease, reconcile events after the last committed position, verify the run's event chain, then continue from the current position — in `internal/engine/resume.go`, returning a terminal-or-paused state with a recorded reason on any failure instead of proceeding on partial knowledge
- [ ] T047 [US2] Implement attempt reconciliation — an attempt with no committed outcome is marked `abandoned` with `restarted_from` and a restart cause, restarted from the phase's recorded inputs with a fresh `attempt_no`, a `resume_restart` decision record, and idempotency keys on side-effecting actions so a recorded side effect is never repeated (FR-063, US2 scenario 7) — in `internal/engine/reconcile.go`
- [ ] T048 [US2] Implement the lease heartbeat — 30s default TTL, renewal at TTL/3, renewal failure demoting the run to `paused` with the cause recorded rather than continuing to write — in `internal/engine/lease.go`
- [ ] T049 [US2] Implement the definition-drift guard — refuse to resume when the supplied fingerprint differs, name the expected and supplied fingerprints, and record the `--allow-definition-drift` override as a decision, in `internal/engine/drift.go`
- [ ] T050 [US2] Implement schema version handling — a newer binary resuming older state either runs a recorded migration writing `state.migrated`, or refuses with `state.migration_refused` and an actionable message; never a best-effort interpretation (FR-017) — in `internal/store/sqlite/version.go`
- [ ] T051 [US2] Implement the `run resume` command — resuming `paused`/`pending`/runs with an expired lease, exit `4` naming the live owner and lease expiry, exit `0` with "already complete" and no work for a terminal run, `--allow-definition-drift` passthrough, and the same JSON envelope — in `internal/cli/resume.go`, wired from `internal/cli/cli.go`
- [ ] T052 [US2] Implement signal handling in `cmd/zamnius/main.go` — SIGINT/SIGTERM cancels the context so the engine commits the current attempt boundary, releases the lease, and leaves the run `paused`, exiting `7`; a second signal exits immediately with `1` and the run stays resumable ([contracts/cli.md](contracts/cli.md) §3)
- [ ] T053 [US2] Implement the pause/resume persistence path — `run.paused` and `run.resumed` events committed with the status change and the `current_phase` update, with `Status` reporting the paused position and the next expected action, in `internal/store/sqlite/pause.go`

**Checkpoint**: User Stories 1 and 2 both work independently; recovery is proven at four interruption points plus a real `SIGKILL`.

---

## Phase 5: User Story 3 — Reconstruct what happened and why from the audit record (Priority: P3)

**Goal**: A reader with no prior context reconstructs the ordered timeline, the cause of each retry or escalation, and the final accepted result from persisted records alone, and can prove the record has not been altered.

**Independent Test**: Take a completed run containing at least one retry and one escalation, hand only the store to a reader with no context, and confirm the reconstruction names the current phase, completed branches, pending work, next action, and every retry cause; corrupting an artifact file makes verification fail naming the file and expected digest ([quickstart.md](quickstart.md) scenario 6).

### Tests for User Story 3 (write first, must fail) ⚠️

- [ ] T054 [P] [US3] Write chain-integrity tests in `test/audit/chain_test.go`: verification over a clean run passes; a single mutated `payload`, `hash`, or `prev_hash` is detected at the correct `seq`; a deleted event is detected as a gap; `UPDATE`/`DELETE` on `events` is rejected at the schema level (US3 scenario 3)
- [ ] T055 [P] [US3] Write artifact-integrity tests in `test/audit/artifact_test.go`: every referenced artifact verifies against its recorded `sha256` (US3 scenario 4); a corrupted file fails with exit `6` naming the file and expected digest; a row whose file is missing is an integrity failure and never triggers a silent re-run ([contracts/store.md](contracts/store.md) §5 step 3)
- [ ] T056 [P] [US3] Write the audit completeness check in `test/audit/completeness_test.go` — every phase transition and every retry, escalation, budget stop, takeover, skip, and resume restart in the event log has a corresponding decision record whose `rationale` names the triggering condition, and every event carries an actor, timestamp, phase, and cause (SC-005, US3 scenarios 1 and 2)
- [ ] T057 [P] [US3] Write the reconstruction test in `test/audit/reconstruct_test.go` — from the store alone, a reader derives current phase, completed branches, pending work, and next expected action for at least 20 sampled scenarios, including one paused mid-fan-out and one failed on an `all` join (SC-004, FR-023)

### Implementation for User Story 3

- [ ] T058 [US3] Implement the audit views in `internal/audit/view.go` — ordered timeline with decision records interleaved at their `seq`, run status view (run row + latest events + phase rows), reconstruction view (current position, completed and pending work, next expected action), and verification view (chain validity plus artifact fingerprints) — all computed on demand and never stored as a second source of truth
- [ ] T059 [US3] Implement streaming chain and fingerprint verification — recompute `sha256(prev_hash || canonical(payload-with-metadata))` event by event, cross-check every artifact row against its file, and return a `VerifyReport` distinguishing chain failure, missing file, and digest mismatch — in `internal/audit/verify.go`, exposed as `Engine.Verify` in `pkg/zamnius/engine.go`
- [ ] T060 [US3] Implement the orphan artifact sweep — identify files under the artifact root with no `artifacts` row, delete them only when no run is `running`, and return the count and bytes reclaimed — in `internal/store/artifactfs/sweep.go`, tested in `internal/store/artifactfs/sweep_test.go`; the command surface stays out of the CLI for this increment ([contracts/store.md](contracts/store.md) §4)
- [ ] T061 [US3] Implement the integrity-facing CLI behaviour — `run artifacts --out <dir>` verifying each fingerprint before writing and aborting with exit `6` on mismatch, and `run status` reporting run status, current phase, per-phase status, attempt counts, budgets consumed, and the last five events in both human and `--json` form — in `internal/cli/artifacts.go` and `internal/cli/status.go`
- [ ] T062 [US3] Extend `test/integration/cli_test.go` with golden assertions for the enriched status view, artifact export verification, and exit `6` on a deliberately corrupted artifact file, so the audit surface is asserted through the same contract tests as the rest of the CLI

**Checkpoint**: All three stories are independently functional; the audit record is provable and the reconstruction claim is tested.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Gates that span stories and the artefacts a fresh session needs.

- [ ] T063 [P] Add the secret-leak suite in `test/secrets/secrets_test.go` — run the full offline corpus with `ZAMNIUS_MODEL_API_KEY=sk-test-canary`, then scan every artifact file, event payload, decision record, JSON envelope, and captured log line for the canary and for `Authorization` values, asserting zero findings (SC-014, FR-054)
- [ ] T064 [P] Write the getting-started guide in `README.md` — install, the three formats, one single-phase run, one fan-out run, status, resume — meeting the 15-minute first-run target (SC-013)
- [ ] T065 [P] Add `CHANGELOG.md` recording the initial public contract set (`pkg/zamnius`) with a compatibility note stating the semver policy of [contracts/library-api.md](contracts/library-api.md) §5 (SC-016)
- [ ] T066 [P] Document `examples/workflows/` — annotate each sample with the behaviour it demonstrates and link it from `README.md` and [quickstart.md](quickstart.md)
- [ ] T067 Measure and record coverage — `internal/engine`, `internal/store/sqlite`, and `internal/audit` at ≥ 80% statements and no package below 60% — using `go test ./... -coverprofile=coverage.out` and record the result in [quickstart.md](quickstart.md) §Automated equivalents
- [ ] T068 Run the full verification sweep and every quickstart scenario — `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `go test ./... -race -count=1`, `CGO_ENABLED=0 go build ./...`, `go test ./test/recoverability -count=10` with zero flakes, and the eight manual scenarios in [quickstart.md](quickstart.md) — then record the outcome in [quickstart.md](quickstart.md) §Definition of done

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — start immediately. T003–T005 are parallel after T001.
- **Foundational (Phase 2)**: Depends on T001–T002. **BLOCKS all user stories.**
- **User Story 1 (Phase 3)**: Depends on Phase 2. No dependency on US2 or US3.
- **User Story 2 (Phase 4)**: Depends on Phase 2 and on the execution loop of US1 (T033–T038) because resume continues a run that Start produced.
- **User Story 3 (Phase 5)**: Depends on Phase 2; the reconstruction and completeness views are testable against any run produced by US1. `Engine.Verify` (T059) depends on T038.
- **Polish (Phase 6)**: Depends on all three stories being complete.

### Within Each User Story

- Test tasks come first and must fail before their implementation tasks turn them green.
- Models/types → services → endpoints → integration.
- Each source file commits **together** with the tests that cover it; no commit claims behaviour without a test (AGENTS.md working agreement 1).

### Task-Level Dependency Notes

- T008 depends on T006; T009 depends on T008.
- T011–T014 depend on T006–T009 and must land in order: schema → commit path → lease → read paths.
- T012, T015, and T016 depend on T009 (canonical JSON and hashing).
- T019 depends on T007 (config error type with `hint`).
- T026–T030 are sequential within `internal/definition`; T028 depends on T026, T030 depends on T026 and T029.
- T031 → T032 (registry before adapter).
- T033 → T034 → T035 → T036 → T037 are sequential in `internal/engine`.
- T038 depends on T030, T031, T033–T037; T039 depends on T038; T040 depends on T039; T041 depends on T040.
- T046 depends on T033–T038 and T053; T047 depends on T046; T048 depends on T013; T049 depends on T029 and T046; T050 depends on T011; T051 depends on T046 and T039.
- T058 depends on T012 and T014; T059 depends on T058 and T038; T061 depends on T059.

### Parallel Opportunities

- Setup: T003, T004, T005 in parallel after T001.
- Foundational: T006, T007, T010, T015, T016, T017, T018, T019 are parallel once T001–T002 land (`[P]` marks them).
- US1 tests T020–T025 are all parallel (six independent test files).
- US1 implementation splits into three parallel tracks after T026–T030: `internal/model` (T031–T032), `internal/engine` (T033–T037), `internal/cli` (T039–T040 after T038).
- US2 tests T043–T045 are parallel; US3 tests T054–T057 are parallel.
- Polish T063–T066 are parallel.

---

## Parallel Example: User Story 1

```bash
# Write all six US1 test files at once — they must fail first:
Task: "Invalid-definition table test … in internal/definition/validate_test.go"
Task: "Format-parity fingerprint tests … in internal/definition/canonical_test.go"
Task: "OpenAI adapter conformance suite … in internal/model/openai/conformance_test.go"
Task: "Phase ordering and join tests … in test/integration/order_test.go"
Task: "Concurrency, retry, budget, provenance tests … in test/integration/execution_test.go"
Task: "CLI golden JSON tests … in test/integration/cli_test.go"
```

---

## Parallel Example: User Story 2

```bash
# Three independent recoverability suites:
Task: "Fault-injection resume suite at four commit boundaries … test/recoverability/resume_test.go"
Task: "SIGKILL re-exec harness … test/recoverability/sigkill_test.go"
Task: "Lease contention, terminal no-op, drift refusal … test/recoverability/ownership_test.go"
```

---

## Implementation Strategy

### MVP First (User Story 1 only)

1. Complete Phase 1: Setup — the module builds and vets on go1.27.3.
2. Complete Phase 2: Foundational — **critical**, blocks every story.
3. Complete Phase 3: User Story 1.
4. **STOP and VALIDATE**: run quickstart scenarios 1, 2, 5, and 7 against a scratch state dir.
5. This is a demonstrable product increment: ordered phases, parallel branches, budgets, artifacts, CLI.

### Incremental Delivery

1. Setup + Foundational → foundation ready.
2. US1 → validate → MVP.
3. US2 → validate with `-count=10` → recoverable engine.
4. US3 → validate reconstruction and tamper detection → auditable engine.
5. Polish → coverage gates, secret scan, documentation, full sweep.

### Notes

- `[P]` marks tasks that touch different files and have no dependency on an incomplete task.
- Verify every test task fails before implementing.
- Commit after each logical group; a source file never lands without its tests.
- Never push or touch the remote — only the `gitt` agent performs remote operations, on explicit instruction.
- Any behaviour change goes spec → plan → tasks → code, never code first.
