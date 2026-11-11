# Contract: persistence (state store, artifacts, leases)

Public surface: `pkg/zamnius.StateStore`, `pkg/zamnius.ArtifactStore` (implemented in `internal/store`).
Reference implementation: SQLite (`internal/store/sqlite`) + filesystem CAS (`internal/store/artifactfs`).
Related: [research.md R2, R3, R11, R12](../research.md), [data-model.md](../data-model.md), ADR-0005.

---

## 1. Transaction protocol (the core guarantee)

Every state transition is one unit:

```text
BEGIN
  update run/phase/attempt row(s)
  insert event(s)
  insert decision record(s)        (when the transition is a decision)
  insert artifact row(s)           (only for files already renamed into place)
COMMIT
```

Rules:

1. **State and event commit together** (FR-011). If they could diverge, resume would be a guess.
2. **Artifact files are written and fsynced before their row is inserted** (FR-015). Crash windows therefore leave an orphaned file (recoverable, sweepable) and never a row pointing at missing content.
3. **`events` is append-only.** The store exposes no update or delete operation; the schema has triggers rejecting `UPDATE`/`DELETE` on `events` as a second line of defence.
4. **Records are versioned** (`record_version` on `runs`). A writer holding a stale version fails with `ErrStaleRecord` and must re-read; this protects against two owners.
5. **Durability over speed**: `PRAGMA synchronous=FULL` in WAL mode. A run that cannot record halts rather than continue (FR-056).

---

## 2. Store interface (conceptual)

```go
type StateStore interface {
    CreateRun(ctx context.Context, spec RunSpec) (Run, error)
    LoadRun(ctx context.Context, id uuid.UUID) (Run, error)

    // AcquireLease succeeds only if the run is unowned or its lease has expired.
    AcquireLease(ctx context.Context, id uuid.UUID, owner string, ttl time.Duration) (Lease, error)
    RenewLease(ctx context.Context, l Lease) (Lease, error)
    ReleaseLease(ctx context.Context, l Lease) error

    // CommitPhaseResult and CommitAttemptResult apply state + events atomically.
    CommitAttemptResult(ctx context.Context, r AttemptResult) error
    CommitPhaseResult(ctx context.Context, r PhaseResult) error
    CommitRunResult(ctx context.Context, r RunResult) error

    // Read paths used by the CLI and reconstruction checks.
    Phases(ctx context.Context, runID uuid.UUID) ([]PhaseState, error)
    Attempts(ctx context.Context, runID uuid.UUID, phase string) ([]Attempt, error)
    Events(ctx context.Context, runID uuid.UUID, fromSeq int64) ([]Event, error)
    Decisions(ctx context.Context, runID uuid.UUID) ([]Decision, error)
}
```

`AcquireLease` is the only operation allowed to change ownership, and it must be a single conditional statement (no read-then-write):

```sql
UPDATE runs
   SET owner_id = :owner, lease_expires_at = :until, record_version = record_version + 1
 WHERE id = :id
   AND (lease_expires_at IS NULL OR lease_expires_at < :now)
RETURNING record_version;
```

Zero rows returned means someone else owns it → `ErrRunAlreadyOwned`. A successful acquisition on a previously owned run additionally writes `run.taken_over` with the previous owner and expiry evidence.

---

## 3. Schema (SQLite reference)

```sql
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);

CREATE TABLE runs (
  id                    TEXT PRIMARY KEY,
  definition_name       TEXT NOT NULL,
  definition_fingerprint TEXT NOT NULL,
  definition_json       TEXT NOT NULL,
  status                TEXT NOT NULL,
  owner_id              TEXT,
  lease_expires_at      TEXT,
  current_phase         TEXT,
  outcome_reason        TEXT,
  budget_json           TEXT NOT NULL,
  spent_json            TEXT NOT NULL,
  created_at            TEXT NOT NULL,
  updated_at            TEXT NOT NULL,
  record_version        INTEGER NOT NULL DEFAULT 1,
  schema_version        INTEGER NOT NULL,
  CHECK (status IN ('pending','running','paused','succeeded','failed','cancelled')),
  CHECK ((owner_id IS NULL) = (lease_expires_at IS NULL))
);

CREATE TABLE phases (
  run_id       TEXT NOT NULL REFERENCES runs(id),
  name         TEXT NOT NULL,
  ordinal      INTEGER NOT NULL,
  status       TEXT NOT NULL,
  started_at   TEXT,
  ended_at     TEXT,
  attempt_count INTEGER NOT NULL DEFAULT 0,
  join_result  TEXT,
  failure_reason TEXT,
  degraded     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (run_id, name),
  CHECK (status IN ('pending','running','succeeded','failed','degraded','cancelled','skipped'))
);

CREATE TABLE attempts (
  id            TEXT PRIMARY KEY,
  run_id        TEXT NOT NULL REFERENCES runs(id),
  phase         TEXT NOT NULL,
  attempt_no    INTEGER NOT NULL,
  status        TEXT NOT NULL,
  agent_name    TEXT NOT NULL,
  model_provider TEXT NOT NULL,
  model_name    TEXT NOT NULL,
  input_fingerprint TEXT NOT NULL,
  prompt_version TEXT NOT NULL,
  started_at    TEXT NOT NULL,
  ended_at      TEXT,
  error_class   TEXT,
  error_detail  TEXT,
  usage_json    TEXT NOT NULL,
  restarted_from TEXT,
  UNIQUE (run_id, phase, attempt_no)
);

CREATE TABLE artifacts (
  id          TEXT PRIMARY KEY,
  sha256      TEXT NOT NULL UNIQUE,
  run_id      TEXT NOT NULL REFERENCES runs(id),
  phase       TEXT NOT NULL,
  attempt_id  TEXT REFERENCES attempts(id),
  kind        TEXT NOT NULL,
  media_type  TEXT NOT NULL,
  size_bytes  INTEGER NOT NULL,
  relative_path TEXT NOT NULL,
  created_at  TEXT NOT NULL
);

CREATE TABLE events (
  seq       INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id    TEXT NOT NULL,
  ts        TEXT NOT NULL,
  type      TEXT NOT NULL,
  actor     TEXT NOT NULL,
  phase     TEXT,
  attempt_id TEXT,
  payload   TEXT NOT NULL,
  prev_hash TEXT,
  hash      TEXT NOT NULL
);
CREATE INDEX events_run_seq ON events(run_id, seq);

CREATE TRIGGER events_no_update BEFORE UPDATE ON events
BEGIN SELECT RAISE(ABORT, 'events are append-only'); END;
CREATE TRIGGER events_no_delete BEFORE DELETE ON events
BEGIN SELECT RAISE(ABORT, 'events are append-only'); END;

CREATE TABLE decisions (
  id         TEXT PRIMARY KEY,
  run_id     TEXT NOT NULL,
  phase      TEXT,
  attempt_id TEXT,
  ts         TEXT NOT NULL,
  kind       TEXT NOT NULL,
  summary    TEXT NOT NULL,
  rationale  TEXT NOT NULL,
  evidence   TEXT NOT NULL
);
```

**Pragmas at open**: `journal_mode=WAL`, `synchronous=FULL`, `busy_timeout=5000`, `foreign_keys=ON`, `SetMaxOpenConns(1)` for writes with a separate read pool when needed.

---

## 4. Artifact store

```go
type ArtifactStore interface {
    // Put writes content and returns its descriptor; it is idempotent by content hash.
    Put(ctx context.Context, runID uuid.UUID, kind, mediaType string, r io.Reader) (ArtifactRef, error)
    // Get returns the content and verifies the fingerprint before returning any bytes.
    Get(ctx context.Context, sha string) (io.ReadCloser, error)
    Verify(ctx context.Context, sha string) error
}
```

**Write sequence** (must not be reordered):

1. Stream to `<tmp>/zamnius-artifact-*` on the same filesystem as the artifact root.
2. Compute sha256 while writing.
3. `fsync` the file, then `rename` to `artifacts/<sha[0:2]>/<sha>`; `fsync` the directory.
4. Return the descriptor; the caller inserts the row **in the same transaction** as the attempt result.

`Put` of identical content is a no-op (same path, row already present or inserted by the caller).

**Orphans**: a file with no `artifacts` row is valid but unreferenced. `zamnius store gc` (out of this increment's CLI scope; implemented in the store package and exercised by tests) may delete such files only when no run is `running`.

---

## 5. Resume protocol

```text
1. Load run. Verify fingerprint and schema_version.
2. Acquire lease.
3. Read events after the last committed position and reconcile:
   - attempt with no committed outcome → mark abandoned, record restart cause, re-run from phase inputs
   - artifact file without row → ignore and re-create on demand
   - row without file → integrity failure; fail the run with evidence (never silently re-run)
4. Verify event chain for the run.
5. Continue execution from the current position.
```

Failure at any step leaves the run in a terminal or paused state with a recorded reason; it never proceeds on partial knowledge.

---

## 6. Conformance tests

1. Commit of an attempt result inserts attempt + artifact rows + events atomically (fault injection between file write and commit yields an orphan file and an aborted attempt, never a half-committed row).
2. Concurrent `AcquireLease` from two connections → exactly one success.
3. Expired lease takeover writes `run.taken_over` with the previous owner.
4. `UPDATE`/`DELETE` on `events` fails at the schema level.
5. Reference-integrity check detects a deleted artifact file.
6. Schema version mismatch yields `state.migration_refused` (or a recorded migration).
