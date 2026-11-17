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
| Optional: model key | `echo $ZAMNIUS_MODEL_API_KEY` | Not needed for scenarios marked *offline* |

**Setup**

```bash
git clone github.com/prompt-fu/zamnius && cd zamnius
CGO_ENABLED=0 go build ./...
go test ./... -race
```

Expected: build succeeds, unit tests pass, no network access required for the default suite.

---

## Scenario 1 — Single-phase run (offline)

Proves: phase execution, artifact commit, status reporting (US1, SC-011).

```bash
go run ./cmd/zamnius run start --file testdata/workflows/hello.yaml --state-dir /tmp/zq1
go run ./cmd/zamnius run status "$(ls /tmp/zq1/runs | head -1)" --json
ls /tmp/zq1/artifacts
```

Expected:

- Exit `0`; final line names the run id and `succeeded`.
- `--json` shows one phase with status `succeeded`, one attempt, and at least one artifact.
- The artifact file's digest equals the `sha256` recorded in the JSON (`sha256sum` on the file).

## Scenario 2 — Fan-out with join

Proves: concurrent branches, deterministic aggregation, join policy (US1, SC-006).

```bash
go run ./cmd/zamnius run start --file testdata/workflows/fanout.yaml --state-dir /tmp/zq2
```

Expected:

- Three branch attempts recorded with overlapping start/end timestamps (concurrency is visible in the event log).
- Exactly one `aggregate` artifact whose content lists branch results in declared order.
- Wall clock for the parallel phase ≤ 1.5× the duration of a comparable single branch in the same run (`research.md` R6 note).

## Scenario 3 — Interrupt and resume

Proves: recoverability (US2, SC-001–SC-003).

```bash
# terminal A
go run ./cmd/zamnius run start --file testdata/workflows/fanout.yaml --state-dir /tmp/zq3
# after the second branch starts:
kill -9 <pid>

# terminal B — a fresh process, no conversation state
go run ./cmd/zamnius run resume "$RUN_ID" --state-dir /tmp/zq3
```

Expected:

- Resume exits `0` and completes the run.
- Artifacts are byte-identical to a clean control run of the same workflow.
- Event log contains `attempt.abandoned` naming the interrupted attempt and a `resume_restart` decision.
- No side effect appears twice: `artifact.committed` cannot list the same `sha256` for the same attempt.

## Scenario 4 — Concurrent resume is refused

Proves: single ownership (US2 scenario 5, FR-016).

```bash
go run ./cmd/zamnius run start --file testdata/workflows/slow.yaml --state-dir /tmp/zq4 &
go run ./cmd/zamnius run resume "$RUN_ID" --state-dir /tmp/zq4; echo "exit=$?"
```

Expected: exit `4`, message names the owner and lease expiry, and the first run is unaffected.

## Scenario 5 — Budget exhaustion

Proves: budgets are enforceable and recorded (FR-005, FR-007).

```bash
go run ./cmd/zamnius run start --file testdata/workflows/budget-tokens.yaml --state-dir /tmp/zq5
```

Expected: exit `3`; event `phase.budget_exhausted` names `tokens`; the decision record states the
limit and the consumed amount; no further attempts start.

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
go run ./cmd/zamnius run start --file testdata/workflows/hello.yaml  --state-dir /tmp/zq7a
go run ./cmd/zamnius run start --file testdata/workflows/hello.json  --state-dir /tmp/zq7b
go run ./cmd/zamnius run start --file testdata/workflows/hello.toml  --state-dir /tmp/zq7c
go run ./cmd/zamnius run start --file testdata/workflows/invalid.yaml --state-dir /tmp/zq7d; echo "exit=$?"
```

Expected:

- All three valid formats produce equivalent runs (same phase count and artifact kinds).
- The invalid file exits `5` and names the offending field path.

## Scenario 8 — No secrets leak

Proves: FR-054, SC-014.

```bash
ZAMNIUS_MODEL_API_KEY=sk-test-canary go run ./cmd/zamnius run start \
  --file testdata/workflows/hello.yaml --state-dir /tmp/zq8 --json >/tmp/zq8.json
rg -i "sk-test-canary" /tmp/zq8 /tmp/zq8.json .zamnius 2>/dev/null; echo "matches=$?"
```

Expected: no matches outside the process environment; `matches=1` from `rg` (no results).

---

## Automated equivalents

| Scenario | Test location | Invocation |
|---|---|---|
| 1, 2, 5, 7 | `test/integration` | `go test ./test/integration -run 'TestRun|TestBudget|TestConfig'` |
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

- [ ] Every scenario runs with `CGO_ENABLED=0` and no provider key (except where a live key is named).
- [ ] Scenario 3 reliably reproduces on 10 consecutive runs; the suite runs it with `-count=10`.
- [ ] Failure paths assert exit codes from [contracts/cli.md](contracts/cli.md) §3, not prose.
