# Quickstart Validation Guide: Zamnius Core Increment

Runnable checks that prove stories P1–P3 work end to end. This is a validation guide, not
implementation documentation; the code that satisfies it lives under `tasks.md` and the implementation.
Related: [contracts/cli.md](contracts/cli.md), [contracts/store.md](contracts/store.md), [data-model.md](data-model.md).
The workflow definitions these scenarios run are annotated one behaviour at a time in
[`examples/workflows/`](../../examples/workflows/README.md).

---

## Prerequisites

| Requirement | Check | Notes |
|---|---|---|
| Go 1.26 or newer | `go version` | Verified locally: `go1.27.3 darwin/arm64` at `/Users/aiagent/.local/go/bin/go`; CI targets 1.27.4 |
| No cgo required | `CGO_ENABLED=0 go build ./...` | The SQLite driver is pure Go by design (research R2) |
| POSIX filesystem | any Linux/macOS | Atomic `rename` and `fsync` are relied upon |
| A model endpoint | `echo $ZAMNIUS_MODEL_BASE_URL` | Every scenario drives the CLI, which refuses to start without `ZAMNIUS_MODEL_API_KEY` (exit `5`). The scenarios are *offline* only in that they need no real provider: point `ZAMNIUS_MODEL_BASE_URL` at any OpenAI-compatible endpoint and set a dummy key (the automated suites under `test/` need neither). |

**Setup**

```bash
git clone github.com/prompt-fu/zamnius && cd zamnius
CGO_ENABLED=0 go build ./...
go test ./... -race
```

Expected: build succeeds, unit tests pass, no network access required for the default suite.

The scenarios below drive the CLI, which always needs a provider. Before running them, start any
OpenAI-compatible endpoint and export it:

```bash
export ZAMNIUS_MODEL_API_KEY=sk-local                       # any non-empty value
export ZAMNIUS_MODEL_BASE_URL=http://127.0.0.1:8099/v1      # your stub's root
```

---

## Scenario 1 — Single-phase run (offline)

Proves: phase execution, artifact commit, status reporting (US1, SC-011).

```bash
go run ./cmd/zamnius run start --file test/testdata/workflows/hello.yaml --state-dir /tmp/zq1
# copy the run id from the final line: "run <id> finished: succeeded (…)"
go run ./cmd/zamnius run status <run-id> --state-dir /tmp/zq1 --json
ls /tmp/zq1/artifacts
```

Expected:

- Exit `0`; final line names the run id and `succeeded`.
- `--json` shows one phase with status `succeeded`, one attempt, and at least one artifact.
- The artifact file's digest equals the `sha256` recorded in the JSON (`sha256sum` on the file).

## Scenario 2 — Fan-out with join

Proves: concurrent branches, deterministic aggregation, join policy (US1, SC-006).

```bash
go run ./cmd/zamnius run start --file test/testdata/workflows/fanout.yaml --state-dir /tmp/zq2
```

Expected:

- Three branch attempts recorded with overlapping start/end timestamps (concurrency is visible in the event log).
- The join produces one `aggregate` artifact whose content lists the branch results in declared order
  (`correctness`, `security`, `performance`). The three branch outputs carry the `aggregate` kind too,
  so the run commits four of that kind plus the summary `report`.
- Wall clock for the parallel phase ≤ 1.5× the duration of a comparable single branch in the same run (`research.md` R6 note).

## Scenario 3 — Interrupt and resume

Proves: recoverability (US2, SC-001–SC-003).

```bash
# terminal A — slow.yaml is the delay-heavy fixture built for interruption drills
go run ./cmd/zamnius run start --file test/testdata/workflows/slow.yaml --state-dir /tmp/zq3
# after the second branch starts:
kill -9 <pid>

# A kill -9 releases nothing, so the dead process still holds the ownership lease
# for up to 30s and an immediate resume is refused with exit 4. Wait for the lease
# to expire, then resume from a fresh process that has no conversation state:
sleep 30

# terminal B
go run ./cmd/zamnius run resume "$RUN_ID" --state-dir /tmp/zq3
```

Expected:

- An immediate resume, before the lease expires, exits `4`; after the wait, resume exits `0` and
  completes the run.
- The branch that committed before the kill is reused, not re-executed; the interrupted attempt is
  abandoned and restarted exactly once.
- Event log contains `attempt.abandoned` naming the interrupted attempt and a `resume_restart` decision.
- No side effect appears twice: `artifact.committed` cannot list the same `sha256` for the same attempt.

## Scenario 4 — Concurrent resume is refused

Proves: single ownership (US2 scenario 5, FR-016).

```bash
go run ./cmd/zamnius run start --file test/testdata/workflows/slow.yaml --state-dir /tmp/zq4 &
# the only row in the store is the run the background process just created:
RUN_ID="$(sqlite3 /tmp/zq4/zamnius.db 'select id from runs limit 1')"
go run ./cmd/zamnius run resume "$RUN_ID" --state-dir /tmp/zq4; echo "exit=$?"
```

Expected: exit `4`, message names the owner and lease expiry, and the first run is unaffected.

## Scenario 5 — Budget exhaustion

Proves: budgets are enforceable and recorded (FR-005, FR-007).

The engine checks a budget before a phase starts, not after one finishes, so the shipped
`budget-tokens.yaml` — a single phase that completes in one call — never reaches its 40-token run
budget and succeeds (exit `0`). To see the stop, run a two-phase workflow whose first phase spends
past the budget, so the second phase cannot start:

```bash
cat > /tmp/budget-stop.yaml <<'YAML'
version: "1"
name: budget-stop
description: Two phases whose run-level token budget stops the second one.
budget:
  tokens: 40
phases:
  - name: draft
    mode: single
    agent:
      name: writer
      instructions: Write one short draft.
      model:
        provider: openai-compatible
        name: gpt-4.1-mini
        max_output_tokens: 128
    outputs:
      - plan
  - name: review
    mode: single
    depends_on:
      - draft
    agent:
      name: reviewer
      instructions: Review the draft.
      model:
        provider: openai-compatible
        name: gpt-4.1-mini
        max_output_tokens: 128
    outputs:
      - report
YAML
go run ./cmd/zamnius run start --file /tmp/budget-stop.yaml --state-dir /tmp/zq5
```

Expected: exit `3`; event `phase.budget_exhausted` names `tokens`; the `budget_stop` decision states
the limit and the consumed amount; the second phase never starts (zero attempts).

## Scenario 6 — Audit reconstruction and verification

Proves: reconstruction and tamper evidence (US3, SC-004, SC-005).

```bash
go run ./cmd/zamnius run status "$RUN_ID" --state-dir /tmp/zq6 --json | jq '.run.phases'
go run ./cmd/zamnius run artifacts "$RUN_ID" --state-dir /tmp/zq6 --out /tmp/zq6-out
```

Expected: artifact export verifies every fingerprint; a deliberately corrupted artifact file makes the
same command exit `6` with a message naming the file and expected digest.

## Scenario 7 — Configuration parity and rejection

Proves: FR-051, FR-052, SC-012.

```bash
# same workflow expressed three ways
go run ./cmd/zamnius run start --file test/testdata/workflows/hello.yaml  --state-dir /tmp/zq7a
go run ./cmd/zamnius run start --file test/testdata/workflows/hello.json  --state-dir /tmp/zq7b
go run ./cmd/zamnius run start --file test/testdata/workflows/hello.toml  --state-dir /tmp/zq7c
go run ./cmd/zamnius run start --file test/testdata/workflows/invalid.yaml --state-dir /tmp/zq7d; echo "exit=$?"
```

Expected:

- All three valid formats produce equivalent runs (same phase count and artifact kinds).
- The invalid file exits `5` and names the offending field path.

## Scenario 8 — No secrets leak

Proves: FR-054, SC-014.

```bash
ZAMNIUS_MODEL_API_KEY=sk-test-canary go run ./cmd/zamnius run start \
  --file test/testdata/workflows/hello.yaml --state-dir /tmp/zq8 --json >/tmp/zq8.json
rg -i "sk-test-canary" /tmp/zq8 /tmp/zq8.json .zamnius 2>/dev/null; echo "matches=$?"
```

Expected: no matches outside the process environment; `matches=1` from `rg` (no results).

---

## Automated equivalents

| Scenario | Test location | Invocation |
|---|---|---|
| 1 | `test/integration` | `go test ./test/integration -run TestPhasesRunInDeclaredOrderAndPublishArtifactsFirst` |
| 2 | `test/integration` | `go test ./test/integration -run TestFanoutBranchesOverlap` |
| 5 | `test/integration` | `go test ./test/integration -run TestTokenBudgetExhaustionStopsThePhase` |
| 7 | `test/integration`, `internal/definition` | `go test ./test/integration -run TestCLIInvalidDefinitionExitsFiveWithAFieldPath` and `go test ./internal/definition -run TestFingerprintIsIdenticalAcrossFormats` |
| 3, 4 | `test/recoverability` (subprocess `SIGKILL`) | `go test ./test/recoverability -run TestResume -count=1` |
| 6 | `test/audit` | `go test ./test/audit -run TestVerify` |
| 8 | `test/secrets` | `go test ./test/secrets` |

`test/recoverability` re-execs the test binary as a child process so `SIGKILL` produces genuine
process death rather than a simulated error (research R10).

### Coverage

The floor is set in [plan.md](plan.md) §Testing Strategy: `internal/engine`,
`internal/store/sqlite`, and `internal/audit` at ≥ 80% of statements, and no package below 60%.

```bash
# Whole-module total. -coverpkg=./... is required: internal/engine and internal/audit
# carry no in-package tests, so their coverage is attributed from test/integration,
# test/recoverability, and test/audit; the plain per-package profile reports them at 0%.
go test ./... -coverpkg=./... -coverprofile=coverage.out
go tool cover -func=coverage.out | tail -1

# Per-target figure (Go's own merge):
go test ./... -coverpkg=github.com/prompt-fu/zamnius/internal/engine -coverprofile=engine.out
go tool cover -func=engine.out | tail -1
```

Measured 2026-11-17:

| Package | Floor | Measured |
|---|---|---|
| `internal/engine` | 80% | 84.1% |
| `internal/store/sqlite` | 80% | 80.6% |
| `internal/audit` | 80% | 85.5% |
| whole module | — | 84.0% |

The lowest library package is `internal/store/artifactfs` at 67.9%, above the 60% floor. The only
package under the floor is `cmd/zamnius` at 0% — a one-statement `main` that calls `os.Exit`, so it is
excluded as untestable, not as a gap. `internal/store/sqlite` is held to its floor by two tests added
in T067: `TestOpenRequiresAPathAndReportsIt` (an empty path is refused, `Path` reports the opened file,
and `Open` creates the containing directory) and `TestLoadRunToleratesALegacyTimestamp` (the read path
accepts an RFC3339Nano timestamp and refuses an unparseable one). Both are non-vacuous — removing the
`parseTime` fallback fails the second.

---

## Definition of done for this guide

- [X] Every scenario runs with `CGO_ENABLED=0` against a local OpenAI-compatible endpoint — a dummy
  `ZAMNIUS_MODEL_API_KEY` and a `ZAMNIUS_MODEL_BASE_URL` pointing at a stub, no real provider (the CLI
  refuses to start without a key, exit `5`).
- [X] Scenario 3 reliably reproduces on 10 consecutive runs; the suite runs it with `-count=10`.
- [X] Failure paths assert exit codes from [contracts/cli.md](contracts/cli.md) §3, not prose.

### Verification record (2026-11-17)

The full sweep is green, in order, on `go1.27.3 darwin/arm64`:

```bash
go build ./...
go vet ./...
go test ./... -count=1
go test ./... -race -count=1
CGO_ENABLED=0 go build ./...
go test ./test/recoverability -count=10      # zero flakes
```

All eight scenarios were then run by hand against the built binary (`CGO_ENABLED=0 go build -o
bin/zamnius ./cmd/zamnius`) and a local OpenAI-compatible stub:

| Scenario | Observed |
|---|---|
| 1 single-phase | exit `0`; 1 phase `succeeded`, 1 attempt, 1 `plan`; the artifact file's `sha256` equals the JSON digest |
| 2 fan-out | exit `0`; 3 overlapping branch attempts; 1 join `aggregate` listing `correctness`, `security`, `performance` in order; parallel phase ≈ 1.07× a single branch |
| 3 interrupt/resume | immediate resume exit `4`; after the lease expiry exit `0`; the committed branch reused; `attempt.abandoned`, `run.taken_over`, `run.resumed` and a `resume_restart` decision recorded; only the interrupted branch restarts |
| 4 concurrent resume | exit `4`; message names owner and lease expiry; the first run finishes unaffected |
| 5 budget | exit `3`; `phase.budget_exhausted` names `tokens`; `budget_stop` decision records the limit and the consumption; second phase 0 attempts |
| 6 audit | export verifies every fingerprint (exit `0`); a corrupted stored artifact makes the export exit `6`, naming the file and the expected digest |
| 7 config parity | all three formats give the same phase/kind shape; `invalid.yaml` exits `5` naming `phases[0].mode` |
| 8 secrets | the canary never appears: `rg` finds no `sk-test-canary` in the envelope or the state directory |

Corrections made while running them — the guide was the defect, not the code:

- the scenario `--file` paths were `testdata/workflows/…`, which does not exist from the repository
  root; the fixtures are `test/testdata/workflows/…`;
- scenario 1's `ls /tmp/zq1/runs` assumed a `runs/` directory that does not exist — the run id is on
  the start command's final line (or the only `runs` row in the store);
- scenario 2 said "exactly one `aggregate` artifact"; there are four (three branch outputs plus the
  join), so the wording now distinguishes the join;
- scenario 3 used `fanout.yaml`, which has no delay to interrupt, and resumed immediately; it now uses
  the delay-heavy `slow.yaml` and waits for the 30s lease to expire (an immediate resume exits `4`);
- scenario 5 expected `budget-tokens.yaml` to exit `3`, but that fixture is a single phase that
  finishes in one call, so the budget is never reached and the run succeeds; the scenario now runs a
  two-phase workflow whose first phase spends past the budget — the shape the stop needs;
- the "Automated equivalents" table's `-run 'TestRun|TestBudget|TestConfig'` matched no test and is
  replaced with the names that actually cover each scenario.

One fixture observation left as-is: [examples/workflows/README.md](../../examples/workflows/README.md)
calls `budget-tokens.yaml` "the budget-stop path", but as a single phase it cannot reach the stop —
`test/secrets` already documents this ("the 40-token budget survives one 18-token call"). Making the
fixture two-phase would let it reach the stop but would change the secrets corpus expectation, so it
is left for a decision rather than changed here.
