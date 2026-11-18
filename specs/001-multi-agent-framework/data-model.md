# Phase 1 Data Model: Zamnius Core Increment (001)

**Feature**: `specs/001-multi-agent-framework/spec.md` · **Plan**: [plan.md](plan.md) · **Research**: [research.md](research.md)

The increment persists four kinds of durable things: **run state**, the **event log**, **decision records**, and **artifacts**. Everything else is derived. Field lists below are the contract; SQL types are the storage mapping from [contracts/store.md](contracts/store.md).

---

## 1. Workflow definition (input, not persisted as mutable state)

Validated from a declarative document or built programmatically; both produce the same in-memory shape ([contracts/workflow-definition.schema.json](contracts/workflow-definition.schema.json)). A definition is immutable once a run references it; the run records a **definition fingerprint** (`sha256` of canonical JSON).

### Definition

| Field | Type | Rules |
|---|---|---|
| `version` | string | Required; schema version, currently `"1"`. |
| `name` | string | Required; 1–120 chars; `[a-z0-9][a-z0-9-]*` |
| `description` | string | Optional; ≤ 2000 chars |
| `budget` | Budget | Optional; run-level allowance |
| `phases` | Phase[] | Required; ≥ 1; `name` unique within the definition |
| `defaults` | PhaseDefaults | Optional; inherited by phases that omit a value |

### Phase

| Field | Type | Rules |
|---|---|---|
| `name` | string | Required; unique; 1–80 chars; `[a-z0-9][a-z0-9-]*` |
| `mode` | enum | `single` \| `fanout`; required |
| `agent` | AgentRef | Required when `mode=single`; forbidden when `mode=fanout` |
| `branches` | Branch[] | Required when `mode=fanout`; ≥ 2; unique `name` per phase |
| `join` | JoinPolicy | Required when `mode=fanout`; forbidden when `mode=single` |
| `depends_on` | string[] | Optional; phase names that must reach a terminal outcome first (defaults to previous phase) |
| `when` | Condition | Optional; skip condition evaluated against recorded outcomes |
| `retry` | RetryPolicy | Optional; defaults from definition |
| `timeout` | duration | Optional; wall-clock cap for the whole phase attempt |
| `budget` | Budget | Optional; overrides run allowance for this phase |
| `on_failure` | enum | `abort` \| `continue` \| `escalate`; default `abort` |
| `outputs` | string[] | Artifact kinds the phase promises; used to validate completion |

### Branch, AgentRef, JoinPolicy, Budget, Retry, Condition

- **Branch**: `{ name, agent: AgentRef, budget?, timeout? }`. Only valid in a fan-out phase.
- **AgentRef**: `{ name, instructions, model: {provider, name, base_url?, max_output_tokens?, temperature?}, budget? }`. Instructions are inline text; external prompt files are a later increment.
- **JoinPolicy**: `{ rule: "all" | "any" | "n_of_m", n?: int }`. `n` required and `2 <= n <= branch count` when `rule=n_of_m`. Aggregation is deterministic: branch results ordered by declared branch order.
- **Budget**: `{ attempts?: int >= 1, wall_clock?: duration, tokens?: int >= 1 }`. At least one field when present.
- **Retry**: `{ max_attempts: int >= 1, backoff: {initial, max, multiplier}, retry_on: ErrorClass[] }`. Default `max_attempts: 1` (no retry), `initial: 1s`, `max: 30s`, `multiplier: 2`, `retry_on: [transient, rate_limited]`.
- **Condition**: `{ after_phase: string, outcome: "succeeded" | "failed" | "any" }` — deliberately minimal in this increment.

---

## 2. Run

One execution of a definition. Owned by exactly one process at a time.

| Field | Type | Notes |
|---|---|---|
| `id` | UUID | Primary identity, exposed in CLI |
| `definition_name` | string | Copied for readability |
| `definition_fingerprint` | hex sha256 | Detects definition drift on resume |
| `definition_json` | canonical JSON | Immutable snapshot; resume must not depend on the file on disk |
| `status` | RunStatus | See state machine |
| `owner_id` | string? | Process identity holding the lease |
| `lease_expires_at` | timestamp? | Null when unowned |
| `current_phase` | string? | Position for status reporting |
| `outcome_reason` | string? | Human-readable terminal cause |
| `budget` | Budget | Effective run allowance |
| `spent` | Consumption | `{attempts, wall_clock_ms, tokens_in, tokens_out}` accumulated |
| `created_at` / `updated_at` | timestamp | Informational only; ordering uses `events.seq` |
| `record_version` | int | Optimistic concurrency guard on row updates |
| `schema_version` | int | Persistence format version for migration (FR-017) |

### Run state machine

```text
pending ──start──> running ──┬─> succeeded
                             ├─> failed
                             ├─> cancelled
                             └─> paused ──resume──> running
```

- `pending`: created, never advanced.
- `running`: a live owner holds an unexpired lease.
- `paused`: no live owner and work remains (lease expired, or operator paused). Resume reacquires the lease.
- Terminal states (`succeeded`, `failed`, `cancelled`) are immutable; resume on them is a recorded no-op (FR-018).

**Invariants**
1. At most one `owner_id` per run; enforced by the conditional lease update.
2. `owner_id` non-null implies `lease_expires_at` non-null and `status = running`.
3. A run's terminal status never transitions again.
4. `definition_json` never changes after creation.

---

## 3. Phase state

Persisted per run.

| Field | Type | Notes |
|---|---|---|
| `run_id` + `name` | composite key | |
| `ordinal` | int | Declared order |
| `status` | PhaseStatus | See below |
| `started_at` / `ended_at` | timestamp? | |
| `attempt_count` | int | Number of step attempts begun |
| `join_result` | JSON? | Aggregation record for fan-out phases |
| `failure_reason` | string? | Set when failed |
| `degraded` | bool | True when `on_failure=continue` was applied |

### Phase state machine

```text
pending ─> running ─┬─> succeeded
                    ├─> failed
                    ├─> degraded        (on_failure=continue)
                    ├─> cancelled
                    └─> skipped         (when-condition false)
```

**Invariants**
1. A phase starts only after every phase in `depends_on` is terminal.
2. `succeeded` requires every declared `outputs` kind to have at least one committed artifact.
3. A `fanout` phase's `join_result` names the branch outcomes it aggregated; a failed `all` join must not produce a partial aggregate (US1 scenario 5).

---

## 4. Step attempt

The atomic unit of work and of recovery (ADR-0003).

| Field | Type | Notes |
|---|---|---|
| `id` | UUID | |
| `run_id` | UUID | |
| `phase` | string | |
| `attempt_no` | int | 1-based, monotonic per phase |
| `status` | AttemptStatus | `running` \| `succeeded` \| `failed` \| `abandoned` |
| `agent_name` | string | Resolved from definition |
| `model_provider` / `model_name` | string | Actually used |
| `input_fingerprint` | sha256 | Hash of phase inputs + resolved prompt |
| `prompt_version` | string | FR-039: prompt identity recorded per action |
| `started_at` / `ended_at` | timestamp? | |
| `error_class` | ErrorClass? | Set when failed |
| `error_detail` | string? | Redacted message |
| `usage` | Consumption | Tokens and wall-clock consumed by this attempt |
| `artifacts` | ArtifactRef[] | Committed by this attempt |
| `restarted_from` | UUID? | Prior attempt this one replaced, with cause |

**Invariants**
1. An attempt commits its outcome and artifacts in one transaction, or leaves no durable trace beyond a `attempt.started` event.
2. `abandoned` is written when a resumed run finds an attempt with no committed outcome; it records the restart cause.
3. `attempt_no` is never reused within a phase.

**ErrorClass** (closed set): `transient`, `rate_limited`, `auth`, `permanent`, `context_overflow`, `invalid_response`, `timeout`, `budget_exhausted`, `cancelled`, `internal`.

---

## 5. Artifact

Immutable content-addressed output.

| Field | Type | Notes |
|---|---|---|
| `id` | UUID | |
| `sha256` | hex (64) | Content hash; also the storage key |
| `run_id`, `phase`, `attempt_id` | keys | Provenance |
| `kind` | string | `plan`, `diff`, `report`, `evidence`, `aggregate`, `log`, `custom:*` |
| `media_type` | string | Default `text/markdown`; `application/json` allowed |
| `size_bytes` | int | |
| `relative_path` | string | `<state>/artifacts/ab/<sha256>` |
| `created_at` | timestamp | |

**Invariants**
1. Content is write-once; the same hash from two runs produces one file.
2. A row exists only for a fully written file (file first, row second).
3. Deleting an artifact is not supported in this increment; compaction is a follow-on concern.

---

## 6. Event

Append-only, totally ordered per run, tamper-evident.

| Field | Type | Notes |
|---|---|---|
| `seq` | integer | Global AUTOINCREMENT; the only ordering authority |
| `run_id` | UUID | |
| `ts` | timestamp | Informational; never used for ordering |
| `type` | string | Dotted, closed vocabulary (below) |
| `actor` | string | `system`, `operator:<id>`, `agent:<name>` |
| `phase` | string? | |
| `attempt_id` | UUID? | |
| `payload` | canonical JSON | Type-specific facts; secrets redacted before write |
| `prev_hash` | hex? | Hash of the previous event for this run |
| `hash` | hex | `sha256(prev_hash || canonical(payload-with-metadata))` |

**Event vocabulary (this increment)**: `run.created`, `run.started`, `run.paused`, `run.resumed`, `run.taken_over`, `run.cancel_requested`, `run.succeeded`, `run.failed`, `run.cancelled`, `phase.started`, `phase.succeeded`, `phase.failed`, `phase.degraded`, `phase.skipped`, `phase.budget_exhausted`, `attempt.started`, `attempt.succeeded`, `attempt.failed`, `attempt.abandoned`, `artifact.committed`, `artifact.verified`, `decision.recorded`, `state.migrated`, `state.migration_refused`.

**Invariants**
1. No update or delete; enforced by the store (no UPDATE/DELETE path in code) and verified by a chain check.
2. Every state transition in sections 2–5 has a corresponding event, committed in the same transaction.
3. Payloads never contain secret values (redaction happens before hashing).

---

## 7. Decision record

| Field | Type | Notes |
|---|---|---|
| `id` | UUID | |
| `run_id`, `phase`, `attempt_id?` | keys | |
| `ts` | timestamp | |
| `kind` | enum | `retry`, `join`, `failure_policy`, `budget_stop`, `takeover`, `skip`, `resume_restart` |
| `summary` | string | One line, human-readable |
| `rationale` | string | Why; must identify the triggering condition (US3 scenario 2) |
| `evidence` | JSON | Pointers into events/artifacts, never duplicated content |

**Invariant**: a decision record exists for every retry, escalation, budget stop, takeover, and restart. The audit completeness check (SC-005) asserts this by cross-referencing events.

---

## 8. Derived views

Computed on demand, never stored as a second source of truth:

- **Run status view**: run row + latest events + phase rows → what `zamnius run status` prints.
- **Timeline view**: events in `seq` order with decision records interleaved.
- **Reconstruction view**: given only the store, the ordered timeline, current position, and next action (FR-023, SC-004).
- **Verification view**: event chain validity + artifact fingerprint checks.

---

## 9. Relationship summary

```text
Definition 1─┬─* Run        (snapshot + fingerprint)
             └─* Phase      (declared; instantiated per run)
Run 1─* PhaseState 1─* Attempt 1─* Artifact
Run 1─* Event
Run 1─* DecisionRecord
```

## 10. Persistence format and migration

- `schema_version` on the `runs` row and a `schema_migrations` table.
- A newer binary resuming older state either runs a recorded migration (`state.migrated`) or refuses with `state.migration_refused` and an actionable message — never continues on a best-effort interpretation (FR-017).
- Definition drift is not a migration: if `definition_fingerprint` differs from the definition supplied at resume, resuming is refused unless `--allow-definition-drift` is passed, and the override is recorded as a decision.
