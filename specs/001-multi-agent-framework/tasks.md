---

description: "Task list for the Zamnius core increment (stories P1–P3)"
---

# Tasks: Zamnius Multi-Agent Framework — Core Increment

**Input**: Design documents from `/specs/001-multi-agent-framework/`

**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md), [research.md](research.md), [data-model.md](data-model.md), [contracts/](contracts/), [quickstart.md](quickstart.md), [constitution](../../.specify/memory/constitution.md)

**Scope**: stories P1–P3 only (ordered phases with fan-out/join, resumability, audit reconstruction). P4–P8 are follow-on feature specifications and produce **no** tasks here.

**Tests**: MANDATORY in this feature, not optional. Constitution Principle I and X.3 require TDD, and plan.md §Testing Strategy names the gates. Every implementation task is preceded by the test task that must fail first.

**Organization**: Tasks are grouped by user story so each story is independently implementable and testable.

**Execution protocol (mandatory — constitution Principle XI)**: implement **one task at a time** and stop for
review after each. A *task* is one numbered entry below (`T044`), not a phase and not a batch. Per task: make the
change, run the task's tests plus `go build ./...` / `go vet ./...`, tick the checkbox, update
`## Implementation status`, then **report and wait** — do not begin the next task until the reviewer approves.
Never bundle several tasks into one turn or one commit; a human reviews each change on its own.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: Which user story this task belongs to (US1, US2, US3)
- Include exact file paths in descriptions

## Path Conventions

Single Go module `github.com/prompt-fu/zamnius`, per [plan.md](plan.md) §Project Structure:

- `cmd/zamnius/` — process entry
- `pkg/zamnius/` — public surface
- `internal/{app,cli,config,definition,domain,engine,model,store/sqlite,store/artifactfs,audit,observability,fault}`
- `test/{integration,recoverability,audit,secrets,fakes,testdata/workflows}`

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Module initialization and the build/lint gates that every later task runs against.

- [X] T001 Initialize the Go module in `go.mod` with module path `github.com/prompt-fu/zamnius` and `go 1.26.0` (raised from 1.25.0 on 2026-11-12 because `modernc.org/sqlite v1.62.0` declares `go 1.26.0`; see [research.md](research.md) R1); confirm `go build ./...`, `go vet ./...`, and `CGO_ENABLED=0 go build ./...` succeed on go1.27.3 before any dependency is added
- [X] T002 Add the pinned dependencies to `go.mod` as decided in [research.md](research.md) — `modernc.org/sqlite v1.62.0`, `github.com/goccy/go-yaml v1.19.2`, `github.com/pelletier/go-toml/v2 v2.4.3`, `github.com/google/uuid v1.6.0`, `golang.org/x/sync v0.25.0` — and verify `CGO_ENABLED=0 go build ./...` still passes (the SQLite driver must stay pure Go). `go.opentelemetry.io/otel v1.47.0` and `github.com/google/go-cmp v0.7.0` are dropped from this increment on 2026-11-12 (R8, R10): nothing imports them, so requiring them would only make `go mod tidy` dirty; each is re-pinned with the change that first uses it
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
- [X] T022 [P] [US1] Write the shared adapter conformance suite of [contracts/model-client.md](contracts/model-client.md) §6 against `httptest` — successful completion populates content/usage/stop reason; `429 → rate_limited`; `401 → auth`; `500 → transient`; malformed body → `invalid_response`; missing `usage` → `invalid_response`; `response_format` rejection falls back to `json_object` and validates locally; context cancellation returns `cancelled` within the deadline plus a small grace period; no secret appears in captured log lines — in `internal/model/openai/conformance_test.go`
- [X] T023 [P] [US1] Write integration tests for phase ordering and joins in `test/integration/order_test.go`: four ordered phases each start only after the preceding phase is terminal and each phase's artifacts exist before the next phase's `phase.started`; a failed `all` join produces no partial aggregate and names the failed branch (US1 scenario 5); a `n_of_m` join with `n=2` of 3 branches requires exactly two successes; `when.after_phase` with `outcome=failed` records `phase.skipped`
- [X] T024 [P] [US1] Write integration tests for fan-out concurrency, retries, budgets, and provenance in `test/integration/execution_test.go`: three branch attempts with overlapping `started_at`/`ended_at` and a wall clock ≤ 1.5× a comparable single branch (SC-006); `retry.max_attempts=3` succeeding on the third attempt records three attempts and runs downstream phases exactly once; `budget.tokens` exhaustion writes `phase.budget_exhausted` naming `tokens` with a decision record stating limit and consumed amount and starts no further attempt; a `continue` failure policy records `phase.degraded`; every `artifacts` row carries `run_id`/`phase`/`attempt_id` provenance and `succeeded` requires each declared `outputs` kind to have a committed artifact
- [X] T025 [P] [US1] Write CLI tests in `test/integration/cli_test.go` asserting the golden `--json` envelope for `run start`, `run status`, and `run artifacts` (`api_version: "1"`, `command`, `ok`, `run`, `artifacts`, `error`), human output ending with `run <id> finished: succeeded (<n> phases, <n> attempts, <n> artifacts)`, `zamnius version`, and exit `5` with a hint naming the offending field path for an invalid definition

### Implementation for User Story 1

- [X] T026 [US1] Implement the definition types (`Definition`, `Phase`, `Branch`, `AgentRef`, `ModelRef`, `JoinPolicy`, `Budget`, `Retry`, `Backoff`, `Condition`, `PhaseDefaults`) with the exact field rules and limits of [data-model.md](data-model.md) §1 and [contracts/workflow-definition.schema.json](contracts/workflow-definition.schema.json) in `internal/definition/types.go`
- [X] T027 [US1] Implement the programmatic builders (`NewDefinition`, `WithPhase`, `WithFanout`, `WithJoin`, `WithRetry`, `WithBudget`, `WithOutputs`) so both the declarative and programmatic paths converge on the same in-memory `Definition` and the same validation (FR-061) in `internal/definition/builder.go`
- [X] T028 [US1] Implement strict decoders per format — `.yaml`/`.yml` via `goccy/go-yaml` strict mode, `.toml` via `pelletier/go-toml/v2` with `DisallowUnknownFields`, `.json` via `encoding/json` with `DisallowUnknownFields`, extension-based dispatch, and a single shared strict struct set — in `internal/definition/decode.go`
- [X] T029 [US1] Implement canonical JSON encoding, the `sha256` definition fingerprint, and the immutable `definition_json` snapshot used by runs, delegating to `internal/domain/canonical.go`, in `internal/definition/canonical.go`
- [X] T030 [US1] Implement validation and resolution in `internal/definition/validate.go`: every rule in [data-model.md](data-model.md) §1, `defaults` inheritance for `retry`/`timeout`/`budget`/`on_failure` with `retry.max_attempts` defaulting to 1, `initial 1s`/`max 30s`/`multiplier 2`, `retry_on [transient, rate_limited]`, `depends_on` defaulting to the previous phase, cycle detection, and a `ValidationError` carrying the dotted field path with the accepted values so FR-052 and SC-012 messages are produced by one code path
- [X] T031 [US1] Implement the provider registry and error-classification helpers — `Client(provider)` returning an error naming known providers, and `Classify(err)` mapping adapter errors onto the closed `ErrorClass` set — in `internal/model/registry.go`, tested in `internal/model/registry_test.go`
- [X] T032 [US1] Implement the OpenAI-compatible reference adapter — `POST {base_url}/chat/completions` with `Authorization: Bearer $ZAMNIUS_MODEL_API_KEY` read from the environment only, `system` mapped to the system message and `messages[]` mapped in order, `max_output_tokens → max_tokens`, temperature passthrough, `response_format: {type: "json_schema", ...strict: true}` with a single `json_object` fallback plus local validation, `usage.prompt_tokens`/`completion_tokens` required (absent usage is `invalid_response`), `finish_reason` mapped to `stop`/`length`/`content_filter`/`unknown`, no internal timeout and no hidden retries — in `internal/model/openai/client.go`
- [X] T033 [US1] Implement the phase execution loop — ordered traversal honouring `depends_on`, `when` skip conditions recorded as `phase.skipped` with a `skip` decision record, `phase.started`/`attempt.started`/`attempt.succeeded`/`attempt.failed`/`phase.succeeded`/`phase.failed`/`phase.degraded` events committed with their state change in one transaction (FR-011), and the `abort`/`continue`/`escalate` failure policies — in `internal/engine/engine.go`
- [X] T034 [US1] Implement fan-out with `errgroup.WithContext` plus a weighted semaphore capped at 8 branches by default and overridable through `StartOptions.MaxConcurrency`, deterministic aggregation ordered by declared branch order into a single `aggregate` artifact, and the `all`, `any`, `n_of_m` join rules where a failed `all` join produces no partial aggregate (US1 scenario 5), in `internal/engine/fanout.go` and `internal/engine/join.go`
- [X] T035 [US1] Implement retry, timeout, and backoff — `max_attempts`, exponential backoff bounded by `max` and `multiplier`, `retry_on` matched against `ErrorClass`, per-phase and per-branch `timeout` via `context.WithTimeout`, and a `retry` decision record whose `rationale` identifies the triggering condition (US3 scenario 2) — in `internal/engine/retry.go`
- [X] T036 [US1] Implement budget accounting per [research.md](research.md) R13 — remaining allowance checked before an attempt (`attempts`, `wall_clock`) and after a response (`tokens`), exhaustion writing `phase.budget_exhausted` naming `attempts`/`wall_clock`/`tokens` plus a `budget_stop` decision record, then applying the declared failure policy, in `internal/engine/budget.go`
- [X] T037 [US1] Implement cancellation and pause — context cancellation stops every active branch before the run reaches a terminal state (FR-008), commits the attempt boundary, and leaves the run `paused` and resumable, with no goroutine leaks, in `internal/engine/cancel.go`
- [X] T038 [US1] Implement the public engine surface and composition root — `New(Options)` rejecting missing `Store`/`Artifacts`/`Models`, `Start`, `Status`, `Artifacts`, `RunResult` populated even when the run fails so callers keep the identifiers, `Observer` callbacks fired in order — in `pkg/zamnius/engine.go`, with the config→store→models→engine wiring in `internal/app/app.go`
- [X] T039 [US1] Implement the CLI dispatcher and rendering — subcommand routing, `flag`-based parsing, usage errors exiting `2`, the single-object `--json` envelope with `api_version: "1"`, human output that ends with the citable `run <id> finished: <status> (...)` line, the exit-code table of [contracts/cli.md](contracts/cli.md) §3, and `zamnius version` — in `internal/cli/cli.go` and `internal/cli/render.go`
- [X] T040 [US1] Implement the `run start`, `run status`, and `run artifacts` commands as thin adapters over the engine, mapping definition/config failures to exit `5` with a `hint` naming the offending setting, in `internal/cli/run.go`
- [X] T041 [US1] Implement `cmd/zamnius/main.go` as process entry only — bind flags, construct the app from `internal/app`, run, map the returned error to an exit code, and leave all business logic to the engine
- [X] T042 [US1] Add the workflow fixtures and samples — `test/testdata/workflows/hello.yaml`, `hello.json`, `hello.toml` (the same workflow three ways), `fanout.yaml` (three branches plus `all` join), `slow.yaml`, `budget-tokens.yaml`, `invalid.yaml` — plus documented copies under `examples/workflows/` with a pointer from `README.md`

**Checkpoint**: User Story 1 is fully functional and independently testable — MVP complete.

---

## Phase 4: User Story 2 — Resume any interrupted run from a fresh instance (Priority: P2)

**Goal**: Any interruption — process death, expired lease, uncommitted attempt — is resumable by a brand-new process holding only the store, without repeating committed work or duplicating recorded side effects.

**Independent Test**: Interrupt a run at a parallel branch, between phases, and mid-artifact-write, then resume from a fresh process limited to the persisted store; the run completes with results equivalent to an uninterrupted control run, zero re-executed committed steps, and zero duplicated side effects ([quickstart.md](quickstart.md) scenarios 3 and 4).

### Tests for User Story 2 (write first, must fail) ⚠️

- [X] T043 [P] [US2] Write the fault-injection resume suite in `test/recoverability/resume_test.go` covering the four interruption points — before/after artifact rename and before/after transaction commit, through `internal/fault` — asserting for each: the resume completes, artifacts are byte-identical to the control run, completed branches are not re-executed, an `attempt.abandoned` names the interrupted attempt, a `resume_restart` decision record states the cause, and no `sha256` is committed twice for one attempt (SC-001, SC-002, SC-003); parametrise so the suite can run with `-count=10`
- [X] T044 [P] [US2] Write the genuine-process-death harness — `TestMain` re-exec of the test binary as a child that starts a run and is killed with `SIGKILL` mid-branch, the parent then resuming in-process — in `test/recoverability/sigkill_test.go`
- [X] T045 [P] [US2] Write ownership and idempotence tests in `test/recoverability/ownership_test.go`: two concurrent `AcquireLease` calls on one run yield exactly one success and the loser gets `ErrRunAlreadyOwned`; resuming a terminal run exits `0` and appends no events (FR-018); a resume after lease expiry records `run.taken_over` with the previous owner; resuming with a mismatched definition fingerprint is refused with an actionable diagnostic unless `--allow-definition-drift` is set, in which case the override is recorded as a decision

### Implementation for User Story 2

- [X] T046 [US2] Implement the resume protocol of [contracts/store.md](contracts/store.md) §5 — load the run, verify `definition_fingerprint` and `schema_version`, acquire the lease, reconcile events after the last committed position, verify the run's event chain, then continue from the current position — in `internal/engine/resume.go`, returning a terminal-or-paused state with a recorded reason on any failure instead of proceeding on partial knowledge
- [X] T047 [US2] Implement attempt reconciliation — an attempt with no committed outcome is marked `abandoned` with `restarted_from` and a restart cause, restarted from the phase's recorded inputs with a fresh `attempt_no`, a `resume_restart` decision record, and idempotency keys on side-effecting actions so a recorded side effect is never repeated (FR-063, US2 scenario 7) — in `internal/engine/reconcile.go`
- [X] T048 [US2] Implement the lease heartbeat — 30s default TTL, renewal at TTL/3, renewal failure demoting the run to `paused` with the cause recorded rather than continuing to write — in `internal/engine/lease.go`
- [X] T049 [US2] Implement the definition-drift guard — refuse to resume when the supplied fingerprint differs, name the expected and supplied fingerprints, and record the `--allow-definition-drift` override as a decision, in `internal/engine/drift.go`
- [X] T050 [US2] Implement schema version handling — a newer binary resuming older state either runs a recorded migration writing `state.migrated`, or refuses with `state.migration_refused` and an actionable message; never a best-effort interpretation (FR-017) — in `internal/store/sqlite/version.go`
- [X] T051 [US2] Implement the `run resume` command — resuming `paused`/`pending`/runs with an expired lease, exit `4` naming the live owner and lease expiry, exit `0` with "already complete" and no work for a terminal run, `--file` (supplied definition) and `--allow-definition-drift` passthrough, and the same JSON envelope — in `internal/cli/resume.go`, wired from `internal/cli/cli.go`
- [X] T052 [US2] Implement signal handling in `cmd/zamnius/main.go` — SIGINT/SIGTERM cancels the context so the engine commits the current attempt boundary, releases the lease, and leaves the run `paused`, exiting `7`; a second signal exits immediately with `1` and the run stays resumable ([contracts/cli.md](contracts/cli.md) §3)
- [X] T053 [US2] Implement the pause/resume persistence path — `run.paused` and `run.resumed` events committed with the status change and the `current_phase` update, with `Status` reporting the paused position and the next expected action. The pause co-commits the resume position in `internal/engine/cancel.go` and the run.resumed commit plus the shared position helper (`session.nextPhaseName`) live in `internal/engine/resume.go`; there is no `internal/store/sqlite/pause.go`, because pause/resume persistence is a run-level commit the engine owns and the `StateStore` contract exposes no pause/resume operation

**Checkpoint**: User Stories 1 and 2 both work independently; recovery is proven at four interruption points plus a real `SIGKILL`.

---

## Phase 5: User Story 3 — Reconstruct what happened and why from the audit record (Priority: P3)

**Goal**: A reader with no prior context reconstructs the ordered timeline, the cause of each retry or escalation, and the final accepted result from persisted records alone, and can prove the record has not been altered.

**Independent Test**: Take a completed run containing at least one retry and one escalation, hand only the store to a reader with no context, and confirm the reconstruction names the current phase, completed branches, pending work, next action, and every retry cause; corrupting an artifact file makes verification fail naming the file and expected digest ([quickstart.md](quickstart.md) scenario 6).

### Tests for User Story 3 (write first, must fail) ⚠️

- [X] T054 [P] [US3] Write chain-integrity tests in `test/audit/chain_test.go` (with the shared `test/audit/harness_test.go`): verification over a clean run passes; a single mutated `payload`, `hash`, or `prev_hash` is detected at the correct `seq`; a deleted event is detected as a gap; `UPDATE`/`DELETE` on `events` is rejected at the schema level (US3 scenario 3)
- [X] T055 [P] [US3] Write artifact-integrity tests in `test/audit/artifact_test.go`: every referenced artifact verifies against its recorded `sha256` (US3 scenario 4); a corrupted file fails with exit `6` naming the file and expected digest; a row whose file is missing is an integrity failure and never triggers a silent re-run ([contracts/store.md](contracts/store.md) §5 step 3)
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
- Implement one task at a time and stop after each for review (constitution Principle XI): verify the task, tick
  its checkbox, update `## Implementation status`, report, and wait for approval — never roll on into the next
  task unprompted.
- Verify every test task fails before implementing.
- Commit after each logical group; a source file never lands without its tests.
- Never push or touch the remote — only the `gitt` agent performs remote operations, on explicit instruction.
- Any behaviour change goes spec → plan → tasks → code, never code first.

---

## Implementation status (2026-11-12)

Written by the implementing agent mid-`/speckit.implement` so a fresh session can resume from
the files alone (constitution Principle II.1). The checkboxes above are authoritative for
"done"; this section records what is genuinely finished, what is implemented but untested, and
what is untouched. Update it (or fold it into the checkboxes) as phases complete.

**Execution rule in force (constitution Principle XI — added 2026-11-12):** implement **one task at a time** and
**stop for review after each**. Per task: verify (its tests + `go build ./...`/`go vet ./...`), tick the checkbox,
update this section, report what changed and how it was verified, then wait for the reviewer's go-ahead before
starting the next task. Never batch several tasks into one turn or one commit.

### Verified green at this checkpoint

- `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `go test ./... -race -count=1` all pass.
- `CGO_ENABLED=0 go build ./...` passes.
- `go test ./test/recoverability -run TestResumeAfterProcessKill -count=10` passes with no flakes; the
  SIGKILL harness is verified non-vacuous by mutation (see T044 below).
- The T051 CLI suite and the T052 signal cases pass with `-count=5`; the SIGTERM subprocess case is
  verified non-vacuous by mutation (see T052 below).
- The T053 pause-position test (`TestPauseRecordsTheResumePosition`) passes `-count=20` and under
  `-race` with no flakes; it is verified non-vacuous by mutation (see T053 below).
- `test/audit` passes `-race -count=10`; the chain- and artifact-integrity tests are verified
  non-vacuous by mutation (see T054 and T055 below).
- The T045 suite and the store lease tests pass with `-count=5`; the cross-connection lease test is
  verified non-vacuous by reverting the DSN (it fails with `SQLITE_BUSY` on the deferred form).
- Branch `001-multi-agent-framework`; all commits local — nothing is pushed. The session of 2026-11-12 added
  the process-hygiene documents (Principle XI, ADR-0007, ADR-0008), initialized the wiki and memory-md
  extensions, completed T044, fixed the deferred-transaction lease defect (ADR-0009), and completed T045,
  T047, and T048; see `git log` for the exact head.
- Toolchain: `go1.27.3 darwin/arm64` on `PATH`; `go.mod` floor is Go 1.26.

### Done (implemented and covered by a passing test)

- **T001–T005** Setup; **T006–T019** Foundational; **T020–T042** US1 — all committed.
- **T043** Fault-injection resume suite — `test/recoverability/resume_test.go` plus the shared
  `test/recoverability/harness_test.go`. It arms each of the four points (before/after artifact
  rename, before/after commit) through `internal/fault`, then resumes on a fresh instance and
  asserts: the resume completes, artifacts are byte-identical to the control run, completed work
  is not re-executed, an `attempt.abandoned` names the interrupted attempt, a `resume_restart`
  decision states the cause, and no `sha256` is committed twice for one attempt. It also carries
  a fan-out resume case (a finished branch is reused, the unfinished one runs, the join completes).
- **T046** Resume protocol — `internal/engine/resume.go`: loads the run, checks the schema
  version, refuses definition drift, takes the lease, verifies the event chain
  (`audit.VerifyChain`), reconciles attempts, then drives from the current position. Exercised
  end-to-end by the T043 suite.
  - **Residual coverage gap**: the failure paths — `failOnIntegrity` on a broken chain, and the
    pause-with-recorded-reason path — are not yet asserted by a dedicated test. T057/T059 must cover them.
- **T044** Genuine process-death harness — `test/recoverability/sigkill_test.go`. `TestMain` re-executes
  the test binary as a child (`ZAMNIUS_SIGKILL_CHILD=1`); the child wires the engine through the same
  `newInstance` path the in-process tests use, starts the fan-out definition, answers one branch, blocks
  in the other, and is then killed with `SIGKILL` by the parent. The child reports over stdout
  (`CHILD-RUN-ID`, `CHILD-BRANCH-BLOCKED`, `CHILD-BRANCH-COMMITTED`), and the parent waits for a
  *committed sibling branch plus a blocked branch* before killing, so the interruption point is observed
  from the durable record rather than assumed. The parent then asserts: the run was left `running`; the
  child really died by `SIGKILL` (`syscall.WaitStatus.Signaled()`); the resume completes; the committed
  branch is not re-executed; the interrupted branch restarts exactly once; artifacts are byte-identical to
  an uninterrupted control run; exactly one `attempt.abandoned` names the interrupted attempt; a
  `resume_restart` decision carries a rationale; and no digest is committed twice for one attempt.
  Non-vacuousness was confirmed by mutation: disabling `session.recordRecovered` fails the
  no-re-execution assertion, and skipping running attempts in `reconcileAttempts` fails the
  abandoned-attempt assertions. `harness_test.go` did not change behaviour: `openInstance` now delegates
  to a new error-returning `newInstance`, and `fanoutDefinition` to a new `branchingDefinition`, so the
  child and the parent share one construction path and one definition (DRY, constitution IX).
- **T045** Ownership and idempotence tests — `test/recoverability/ownership_test.go`, four cases:
  two instances over one state directory contend for a lease and exactly one wins while the loser
  gets `ErrRunAlreadyOwned`; resuming a terminal run returns the terminal status, calls no model,
  and appends no event (FR-018); a resume after the owner's lease lapsed records exactly one
  `run.taken_over` naming both owners plus a takeover decision (FR-016); and a run whose recorded
  fingerprint disagrees with its stored snapshot is refused with a hint naming both fingerprints
  and `--allow-definition-drift`, or, with `engine.ResumeOptions{AllowDefinitionDrift: true}`,
  proceeds and records exactly one drift-override decision with the fingerprints in its rationale
  and evidence (FR-014).
  - **Defect found and fixed**: the two-instance case first failed with `SQLITE_BUSY`, not
    `ErrRunAlreadyOwned`. The store began transactions deferred, so a read-then-write upgrade on a
    stale WAL snapshot is refused without consulting `busy_timeout`, turning cross-process lease
    contention into a raw database-lock error. Fixed by `_txlock=immediate` in
    `internal/store/sqlite/db.go` (BEGIN IMMEDIATE), pinned by
    `TestConcurrentAcquireLeaseAcrossConnectionsHasOneWinner` in
    `internal/store/sqlite/lease_test.go` (fails on the deferred DSN, passes immediate), and
    recorded in [ADR-0009](../../docs/domain_knowledge/adr/0009-sqlite-immediate-transactions.md).
    The fix is a store change, not a T045 change, so it lands in its own commit.
  - **Semantic gap closed by T049**: `Resume` now takes an optional supplied definition
    (`ResumeOptions.Definition`), so the drift guard is reachable on the operator's file rather than
    only on an internally inconsistent record. `Engine.Resume` still executes the stored snapshot.
- **T047** Attempt reconciliation — `test/recoverability/reconcile_test.go`, three cases:
  - **Crash restart links the replacement** — an attempt was `running` when the process died; resume
    marks it `abandoned` and the restarted attempt carries `restarted_from` naming it, with a fresh
    `attempt_no`; a phase that ran once and was not restarted records no `restarted_from`
    (data-model §4 invariant 2, FR-063).
  - **Pause restart links the replacement** — the owner was cancelled cleanly, so the attempt was
    already `abandoned` before resume; the restart still names it, so a paused-then-resumed branch
    keeps the link between the interrupted attempt and its replacement.
  - **A restart cannot duplicate the side effect** — the increment's one side-effecting action is
    the content-addressed artifact write. The interrupted attempt wrote its document but crashed
    before committing the row; the restart reproduces the same bytes, so the content key (`sha256`)
    identifies the side effect and it is recorded exactly once, owned by the restarted attempt, with
    the abandoned attempt owning none (FR-063, FR-014, SC-003).
  - **Implementation**: `session.restartFrom` (per phase and agent) is populated by
    `reconcileAttempts` from the latest attempt of each agent that has no committed outcome (running
    or already abandoned), and claimed by the next attempt of that agent via `takeRestartSource`,
    which clears the entry so a retry of the replacement is a retry, not a second restart. The
    idempotency key is the artifact's content hash — no schema change, no new field, because the
    data model and contracts define none and tools (the other side-effecting surface) are deferred.
  - **Non-vacuousness by mutation**: removing the `RestartedFrom: s.takeRestartSource(...)` wiring
    fails both `restarted_from` tests; making the artifact body depend on the attempt id (a
    non-idempotent side effect) fails the content-key test with the digest recorded 0 times.
- **T048** Lease heartbeat — `test/recoverability/heartbeat_test.go`, two cases:
  - **Renewal happens while a phase runs** — one phase spends several renewal intervals inside a
    single model call; the lease is renewed more than once during it, proving the heartbeat, not a
    phase boundary, keeps ownership alive (ADR-0004).
  - **A failed renewal pauses the run** — a renewal that fails (`ErrRunAlreadyOwned`) demotes the run
    to `paused`, the durable status is `paused`, and the `run.paused` reason names the lease failure
    rather than a generic cancellation, so an operator can tell it apart from a caller cancellation
    (FR-008, FR-016).
  - **Implementation**: `internal/engine/lease.go` gains `startHeartbeat`/`heartbeat` (a goroutine
    renewing at `leaseTTL/3` via a real ticker), `currentLease`/`setLease` (guarding `session.lease`
    under `leaseMu`), and `recordLeaseLoss`/`leaseLossCause`. The heartbeat is the sole renewer, so
    the old phase-boundary renewal in `drive` was removed. A renewal failure records the cause and
    cancels the run context; `pause` and the success path in `drive` consult `leaseLossCause` so the
    recorded reason is the lease failure. `releaseLease` stops the heartbeat before releasing, so a
    renewal can never race the release. The store decorator seam (`newInstanceWith`) lets a test
    observe or force a renewal without changing production wiring. Dead helper `leaseTTLForReports`
    removed.
  - **Non-vacuousness by mutation**: making the heartbeat body a no-op fails the cadence test
    (renewed 0 times) and times out the failure test; removing the `recordLeaseLoss`/`cancel` on a
    failed renewal times out the failure test.
  - **Defect found and fixed while verifying T050**: a lost lease that struck *before* a phase
    started escaped as a raw error against a status that still said `running`. The heartbeat
    cancels the run context, and the drive loop's first `Phases` read then failed with
    `context.Canceled`, which `drive` returned through `s.result(err)`. Found as a rare `-race`
    flake (`heartbeat_test.go:167: status = "running", want paused`) and pinned deterministically by
    `TestLeaseLossDuringAPhaseReadPausesTheRun`, which stalls the first phase read until the lost
    lease cancels the context. Fixed by splitting the loop into `walk` and wrapping it in `drive`:
    an error returned while `ctx.Err() != nil` and the run is still `running` becomes a pause that
    names the lease. Its own commit, because it is a T048 defect, not a T050 change.
- **T049** Definition-drift guard — `internal/engine/drift.go`, covered by
  `test/recoverability/drift_test.go` (three cases) plus the two T045 cases:
  - **A supplied definition that differs is refused** — a run recorded from a two-phase workflow,
    resumed with a caller-supplied three-phase workflow, returns `ErrDefinitionDrift`; the hint
    names the recorded fingerprint, the supplied fingerprint, and `--allow-definition-drift`; no
    events are appended and the run stays `pending`.
  - **The override records the drift and still finishes the recorded workflow** — with
    `AllowDefinitionDrift` the run succeeds, executes exactly the recorded phases (`draft`,
    `review`), never calls the model for the supplied definition's extra `publish` phase, and
    records exactly one override decision whose rationale and evidence carry both fingerprints.
  - **A supplied definition that matches is accepted silently** — a fresh build of the same workflow
    resumes normally and records no drift override.
  - **Decision**: `ResumeOptions` gained `Definition *Definition` (nil = resume from the snapshot
    alone, so FR-012 still holds), and `contracts/library-api.md` §2 and `contracts/cli.md` §1 were
    updated. The engine always **executes the stored snapshot**: committed phases were recorded
    against it, so a different definition cannot be adopted mid-run; the supplied definition is an
    intent check, and the override records that the operator knowingly proceeded. `run resume
    --file <path>` is the CLI half, wired in T051.
  - **Ordering**: the refusal is decided before anything is written (contract: a refused resume
    touches nothing), and the override decision is now recorded only **after** the lease is
    acquired, so a process that does not own the run can never append to its history; if the
    override cannot be committed, the lease is released rather than held.
  - **Non-vacuousness by mutation**: making `compareDefinitions` ignore the supplied definition
    passes the drifted definition to execution, so the refusal test fails (`error = <nil>`) and the
    override test records 0 decisions.
- **T050** Schema-version handling — `internal/store/sqlite/version.go` (moved out of
  `migrate.go`, which is deleted), covered by `test/recoverability/version_test.go`,
  `internal/store/sqlite/version_test.go`, and `internal/store/sqlite/version_internal_test.go`:
  - **Older state is migrated and the migration is recorded once** — a run whose recorded
    `schema_version` is older than the store's resumes to success, records exactly one
    `state.migrated` (`{from_schema_version, to_schema_version}`), and the same commit brings
    `runs.schema_version` up to the store's, so a later resume finds no gap.
  - **Newer state is refused with evidence** — a run recorded under a newer schema returns
    `ErrMigrationRefused` with a hint naming the schema the operator must upgrade to; exactly one
    `state.migration_refused` (`{run_schema_version, store_schema_version, message}`) is recorded,
    the run stays `pending`, and the lease taken to record it is released before the error is
    returned (proved by acquiring it again afterwards).
  - **Store-level matrix** — `CheckRunVersion` reports equal/older/newer as
    `domain.RunSchemaStatus`, refuses only the newer direction, and `Open` refuses a database whose
    highest applied migration is newer than the build.
  - **Migration machinery** — `TestMigrateAppliesAndRecordsAPendingMigration` appends a migration
    this build does not ship, opens, asserts the statements ran and were recorded once per version,
    and reopens to prove nothing is replayed.
  - **Design decisions**: `domain.RunSchemaStatus` (with `Older`/`Newer`) is the comparison both
    layers share; `domain.RunDelta.SchemaVersion` (0 = unchanged) lets the row bump ride the same
    transaction as the event (FR-011); the events are run-scoped because the event table requires a
    `run_id`, so the engine records them under the lease; `shippedSchemaVersion()` reads the newest
    entry of `migrations` instead of the `domain.SchemaVersion` constant (their equality is pinned
    by `TestMigrationsMatchTheBuildSchemaVersion`), which is what lets the migration list be
    exercised with an artificial step; `migrate`'s refusal and `CheckRunVersion`'s refusal now
    carry `domain.Error` hints, where before they were bare wrapped errors.
  - **Non-vacuousness by mutation**: skipping the `schema.Older()` recording fails the migration
    test (`state.migrated events = 0`); skipping `recordMigrationRefusal` fails the refusal test
    (`state.migration_refused events = 0`).
- **T051** `run resume` command — `internal/cli/resume.go` (moved out of `run.go`, wired from
  `internal/cli/cli.go`), covered by `test/integration/resume_test.go` (eight cases):
  - **A resumable run finishes** — a run left `pending` by an interrupted process resumes to success,
    emits the same versioned envelope (`command: run.resume`), commits an artifact, and records
    `run.resumed`.
  - **A paused run continues from its position** — a genuinely interrupted run (the scripted model
    reports `context.Canceled`, so the engine commits the attempt boundary and pauses) resumes to
    success: the interrupted attempt stays `abandoned`, exactly one replacement attempt `succeeded`,
    and `run.resumed` is recorded.
  - **A terminal run is a no-op** — exit `0`, prints "already complete", appends no event, and still
    succeeds with `ZAMNIUS_MODEL_API_KEY` removed, which proves the terminal check runs on the
    inspection engine and executes no work (FR-018, contracts/cli.md §5.2).
  - **A live owner is named** — exit `4`; the error hint names the owner and the lease expiry, the
    `--json` run view carries `owner_id`, and nothing is appended. The position is re-read after the
    refusal so the owner named is the one that refused the lease (FR-016, contracts/cli.md §5.3).
  - **`--file` drift is refused** — exit `5`; the hint names the fingerprints and
    `--allow-definition-drift`; no event is appended (FR-014).
  - **`--allow-definition-drift` finishes the recorded workflow** — the run succeeds, only the
    recorded `greet` phase executes (never the supplied fan-out's branches), and exactly one
    `resume_restart` decision records the override.
  - **A matching `--file` is silent** — the same workflow supplied by file resumes normally and
    records no drift decision.
  - **Newer state is refused with exit `6`** — the hint names the schema that wrote the run and the
    run stays `pending` (FR-017).
  - **Exit-code mapping** (carried in from T049/T050): `ErrDefinitionDrift` → exit `5` and
    `ErrMigrationRefused` → exit `6`; both rows are clarified in `contracts/cli.md` §3.
  - **Ordering**: the terminal check uses `composition.Inspector()`, so a run that needs no work never
    requires a model credential; a supplied definition is parsed before the model adapter is built.
  - **Non-vacuousness by mutation**: ignoring the supplied definition makes the drift refusal exit `0`
    and records 0 override decisions; removing the owner enrichment leaves the hint empty; bypassing
    the terminal pre-check fails the no-op test with the credential error; removing either exit mapping
    yields exit `1`.
- **T052** Signal handling — `internal/cli/signal.go` (`RunWithSignals`, called by
  `cmd/zamnius/main.go`), covered by `internal/cli/signal_internal_test.go` (two unit cases) and
  `test/integration/signal_test.go` (one subprocess case):
  - **A SIGTERM during a provider call pauses the run** — the parent re-executes this test binary as a
    child, which binds its signals through `cli.RunWithSignals`; the child starts `run start` against a
    provider that withholds its first answer, the parent waits for that call to arrive and then sends
    SIGTERM. The child exits `7`, the durable status is `paused`, `run.paused` is in the log, and the
    lease was **released** (proved by taking it again immediately); after the provider is released the
    ordinary `run resume` finishes the run (contracts/cli.md §3, §5.4, FR-008).
  - **One signal cancels the command context and returns exit `7`** — a unit case drives
    `runWithSignals` with a scripted command that waits on its context, sends SIGTERM to the test
    process, and asserts the command returns `ExitCancelled` without the immediate-exit path being
    taken.
  - **A second signal exits immediately with `1`** — a unit case blocks the scripted command *after*
    its context ended, sends a second SIGTERM once the first has been delivered (so the kernel cannot
    coalesce them), and asserts the injected exit receives `ExitError` while the command is still
    unwinding.
  - **Reporting survives the cancellation** — `runner.inspect` detaches to
    `context.WithoutCancel` when the context has already ended, because the view read after a
    signalled stop would otherwise fail with `context.Canceled` and downgrade the exit code from `7`
    to `1`. Non-vacuousness: reverting that detachment fails the subprocess test with
    `child exit = 1, want 7`.
  - **Design decisions**: the binding lives in `internal/cli/signal.go` and `main.go` stays the one
    line that binds args, streams, and signals, because package `main` cannot be imported by a test —
    the shipped code path (`RunWithSignals`) is what the child executes, so nothing about the tested
    path is a copy. `runWithSignals` takes the runner and the exit function as parameters so the
    second-signal path is deterministic to test; the process-level version of that case is
    inherently racy (the graceful pause commits in well under a millisecond, so the process usually
    exits `7` before a second signal lands) and is therefore asserted at unit level instead. Two
    registrations are deliberate: `signal.NotifyContext` carries the first signal to the work, and a
    buffered channel counts signals for the second. The paused-then-resumed behaviour of a run killed
    outright is already covered by T045 (lease takeover) and T047 (attempt reconciliation).
  - **Non-vacuousness by mutation**: dropping the `NotifyContext` binding (keeping only the counting
    channel) makes the child ignore the signal and the subprocess test times out; removing the
    immediate exit leaves the second-signal unit case waiting; exiting on the first signal fails the
    first-signal unit case.
- **T053** Pause/resume persistence — the pause co-commits the resume position in
  `internal/engine/cancel.go`, `run.resumed` is committed in `internal/engine/resume.go`, and both
  share `session.nextPhaseName(ctx)`. Covered by `test/integration/pause_test.go` plus a
  `current_phase` assertion added to `test/integration/cancel_test.go`:
  - **A run interrupted between phases records the next phase as its position** — a recording
    observer cancels the run's context the moment the first phase reaches a terminal state, so the
    interruption lands between phases deterministically rather than by racing the scheduler. The
    pause commits `run.paused` with the status change and `current_phase = "second"`; `Status`
    reports `paused`, the position, and a `NextAction` naming the phase a resume runs; `LoadRun`
    shows the position lives on the run row (the reader does not invent it); the second phase has no
    attempt; and a resume finishes both phases with exactly two model calls, so the committed phase
    is never repeated.
  - **A run interrupted mid-phase keeps the phase it interrupted** — `cancel_test.go` now asserts
    the paused `current_phase` names the fan-out phase that was in flight, so both pause scenarios
    pin the same invariant: `current_phase` always names where work stopped.
  - **Design**: `run.paused` already co-committed the status change but left `current_phase` at
    whatever `startPhase` last wrote — a finished phase when the run stops between phases. The fix
    is one field on the pause delta. `Status` needed no new field: `CurrentPhase` and `NextAction`
    already exist and are computed from the row, so the task's named store file was **not** created
    (see the task line — the `StateStore` contract exposes no pause/resume operation, and this is a
    run-level commit the engine owns). `nextPhaseName` now takes the caller's context instead of a
    detached `context.Background()`, so the position read is bounded by the pause commit's context
    and one helper defines the resume position for both the pause and the resume.
  - **Non-vacuousness by mutation**: removing `CurrentPhase: s.nextPhaseName(commitCtx)` from the
    pause delta fails the between-phase test (`paused current_phase = 0x…, want "second"`); the red
    state was confirmed this way before the fix, and the between-phase test passes `-count=20`.
- **T054** Chain-integrity tests — `test/audit/chain_test.go` plus the shared
  `test/audit/harness_test.go`. Every test name starts with `TestVerify`, so the quickstart's
  `go test ./test/audit -run TestVerify` selects all four:
  - **A clean run verifies** — a three-phase run (ordered → three-branch `all` fan-out → ordered)
    is driven through a real store, artifact store, and engine; `audit.VerifyChain` reports no
    problem, every event's `prev_hash` equals its predecessor's stored `hash` (the link is present,
    not merely unchecked), and `Engine.Verify` returns an `OK` report with the event and artifact
    counts.
  - **A single tampered event is caught at its `seq`** — a table mutates one interior event's
    `payload`, `hash`, or `prev_hash` and asserts `VerifyChain` reports a `chain` problem whose
    `seq` is exactly the mutated one (with a non-empty detail), for all three fields.
  - **A deleted event is a broken link** — removing an interior event makes its successor's
    `prev_hash` point at a hash no surviving event carries, and the problem is reported at the
    successor's `seq`.
  - **The append-only rule holds at the schema level** — `UPDATE` and `DELETE` against `events`
    both fail naming the `append-only` trigger, the event count is unchanged, and the record still
    verifies afterwards (US3 scenario 3).
  - **Design**: the tests read records a real run wrote — the fixture assembles the three
    collaborators the composition root assembles, with a deterministic clock and id source — so a
    defect in the writer or the reader surfaces here rather than being masked by hand-built rows.
    The harness is shared with T055–T057, which extend it with their own workflows.
  - **Non-vacuousness by mutation**: neutering `VerifyChain` to `return nil` fails the tamper table
    (all three fields) and the deleted-event test; the clean-run and schema-level tests are
    independent of that return, as they should be. The schema-level test is non-vacuous by
    construction: it requires an error and asserts the message names the trigger.
- **T055** Artifact-integrity tests — `test/audit/artifact_test.go`, extending the shared harness
  with the state-directory path so a test can read the file an operator would find:
  - **A clean run's artifacts verify** — every row an ordered-plus-fan-out run committed is checked
    three ways: `artifactfs.Verify` agrees, the file at the recorded `relative_path` hashes to the
    recorded `sha256`, and its length equals `size_bytes`; `Engine.Verify` then reports `OK` with
    the artifact count. Reading the file through the state directory (not the store's private root)
    is what makes a wrong recorded path fail here.
  - **A corrupted file exits 6 naming the file and digest** — a paused run (one committed phase with
    an artifact, interrupted deterministically between phases) has its artifact bytes rewritten
    behind the row. `Engine.Verify` reports a `digest_mismatch` for that `sha256` whose detail names
    both the relative path and the expected digest; `zamnius run resume` then exits `6`, prints the
    same two facts, appends exactly one `run.failed`, and executes no attempt.
  - **A missing file fails integrity and never re-runs** — after removing the artifact file,
    `Engine.Resume` returns an error wrapping `domain.ErrIntegrity`, the hint names the file and its
    digest, the run is `failed`, no attempt is executed, and a second resume is a terminal no-op
    that appends nothing (contracts/store.md §5 step 3, FR-018).
  - **The design change it proved missing** — resume verified the event chain but never checked
    artifact rows against their files, so a missing or altered artifact was silently reused and the
    interrupted phase re-executed. `internal/engine/resume.go` now runs `audit.VerifyArtifacts` over
    `Engine.Artifacts` before reconciliation and fails the run through `failOnIntegrity`, which is
    the contract's step 3 and the same evidence path a broken chain already used.
  - **Non-vacuousness by mutation**: with the artifact check removed, the corrupted test reaches a
    model call and exits `3` and the missing-file test reports `Resume() accepted a run whose
    artifact file is missing`; both fail as soon as the check is restored.

### Implemented but NOT yet covered by a dedicated test (left unchecked)

Nothing outstanding: the last entries — T052, T053, T054, and T055 — are all covered.
### Not started

- **T056–T062** US3 — both halves of verification are covered (T054 chain, T055 artifacts). The
  completeness check, the reconstruction suite, the audit views, streaming verification as
  `Engine.Verify`, the orphan-artifact sweep, and the integrity-facing CLI behaviour remain.
- **T063–T068** Polish — untouched. `README.md` exists but is not yet the getting-started guide;
  `CHANGELOG.md` does not exist.

**US2 is complete** (T043–T053): recovery is proven at four interruption points, a real `SIGKILL`,
lease contention and takeover, attempt reconciliation, definition-drift refusal, schema-version
migration/refusal, the `run resume` command, signal handling, and now the paused position. **US3 is
under way: T054 (chain integrity) and T055 (artifact integrity) are done.** The next task is
**T056**.

### Design decisions made while implementing (not yet written as ADRs)

- The fan-out aggregate (`persistAggregate`, `internal/engine/join.go`) projects branch outcomes
  **without** `attempt_no` (the local `branchSummary` type), so the `aggregate` artifact is
  byte-identical across a resume even when a branch restarts. The `join_result` record stored in
  the phase row keeps the full `domain.BranchOutcome`, `attempt_no` included.
  `domain.BranchOutcome.Artifacts` was replaced by `Digests []string` for the same reason.
- The `internal/store/sqlite` commit paths hit `fault.BeforeCommit` / `fault.AfterCommit` around
  each transaction, so the recoverability suite can crash exactly at a transaction boundary.
- `internal/store/sqlite` artifacts use `sha256` as a `UNIQUE` key with `ON CONFLICT DO NOTHING`;
  identical content dedupes to one row, so per-branch test content is made distinct deliberately.

### Known open defects to carry forward

- `quickstart.md` scenario 1 runs `ls /tmp/zq1/runs`, which does not match the real state layout;
  fix it when the scenarios are validated in T068.
- `AGENTS.md`'s ADR range and `plan.md` line 225 still say `0001–0007`; update them if the range is
  load-bearing (AGENTS.md was updated to `0001–0009` this session).
