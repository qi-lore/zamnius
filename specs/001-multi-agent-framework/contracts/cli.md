# Contract: command-line surface

Binary: `zamnius` (`cmd/zamnius`). Scope: FR-060 — start, resume, status, artifacts.
Related: [research.md R5](../research.md), [data-model.md](../data-model.md).

Design rules: no business logic in the command layer; every command is a thin adapter over
`pkg/zamnius.Engine`. Human output is stable prose; `--json` output is a versioned, machine-readable
shape and is the only form tests assert against.

---

## 1. Commands

```text
zamnius run start   --file <path> [--state-dir <dir>] [--json]
zamnius run resume  <run-id> [--file <path>] [--state-dir <dir>] [--allow-definition-drift] [--json]
zamnius run status  <run-id> [--state-dir <dir>] [--json]
zamnius run artifacts <run-id> [--state-dir <dir>] [--out <dir>] [--json]
zamnius version
```

### `run start`

Validates the definition, creates the run, acquires the lease, and executes to a terminal state or
until interrupted.

| Flag | Default | Meaning |
|---|---|---|
| `--file` | required | Definition file (`.yaml`, `.yml`, `.json`, `.toml`) |
| `--state-dir` | `./.zamnius` | State database and artifact root |
| `--config` | discovered | Additional configuration file (precedence: flags > env > `--config` > discovered > defaults) |
| `--json` | false | Emit only the JSON result on stdout |

Human output on success ends with a line naming the run id and status, so it can be captured:

```text
run 6f1c1f0e-... finished: succeeded (3 phases, 7 attempts, 4 artifacts)
```

### `run resume`

Resumes a run that is `paused`, `pending`, or `running` with an expired lease.

- If another live process owns the run: exit code `4`, message names the owner and lease expiry.
- If the run is already terminal: exit `0`, prints that it is already complete, executes nothing (FR-018).
- `--file` supplies the workflow the run was started from. It is optional: without it the run resumes
  from its stored snapshot alone. With it, a fingerprint that differs from the run's recorded one is
  refused (exit `5`) with a hint naming both fingerprints; the engine finishes the recorded workflow
  either way, never the supplied one.
- `--allow-definition-drift` permits resuming when the supplied definition fingerprint differs; the
  override is recorded as a decision (`failure_policy`-class `resume_restart` variant).

### `run status`

Prints run status, current phase, per-phase status, attempt counts, budgets consumed, and the last
five events. `--json` returns the same data as the status view object.

### `run artifacts`

Lists committed artifacts (`kind`, `sha256`, `bytes`, `phase`). With `--out`, copies them to a
directory, verifying each fingerprint before writing; any mismatch aborts the command with exit `6`.

---

## 2. `--json` envelope

Every command emits exactly one object:

```json
{
  "api_version": "1",
  "command": "run.start",
  "ok": true,
  "run": { "id": "...", "status": "succeeded", "phases": [ ... ] },
  "artifacts": [ { "sha256": "...", "kind": "plan", "media_type": "text/markdown", "size_bytes": 1234, "phase": "plan" } ],
  "error": null
}
```

On failure, `ok` is `false` and `error` is `{ "class": "<ErrorClass or usage/config class>", "message": "...", "hint": "..." }`. `hint` is required for configuration and integrity failures and must name the offending setting or file.

---

## 3. Exit codes

| Code | Meaning |
|---|---|
| `0` | Command completed; run reached a terminal state or status was reported |
| `1` | Command failed before or during execution (general error) |
| `2` | Usage error (unknown flag, missing argument) |
| `3` | Run reached `failed` |
| `4` | Run is owned by another live process |
| `5` | Definition or configuration invalid, including a resume whose supplied definition has drifted |
| `6` | Persistence integrity failure (artifact fingerprint mismatch, broken event chain), or state written by an incompatible schema this build refuses to migrate |
| `7` | Cancelled by signal (SIGINT/SIGTERM) — run is left `paused`, resumable |

**Signal handling**: SIGINT/SIGTERM cancel the context, let the engine commit the current attempt
boundary, release the lease, mark the run `paused`, and exit `7`. A second signal exits immediately
with `1`; the run stays resumable either way.

---

## 4. Environment variables

| Variable | Meaning |
|---|---|
| `ZAMNIUS_STATE_DIR` | Default state directory |
| `ZAMNIUS_MODEL_API_KEY` | Api key for the reference adapter (never logged) |
| `ZAMNIUS_MODEL_BASE_URL` | Default base URL for the reference adapter |
| `ZAMNIUS_LOG_LEVEL` | `debug` \| `info` \| `warn` \| `error` (default `info`) |
| `ZAMNIUS_LOG_FORMAT` | `text` \| `json` (default `text`; `json` required for machine ingestion) |

Precedence: flag > environment > config file > discovered config > built-in default.

---

## 5. Test expectations

1. Every command has a golden `--json` test.
2. `run resume` against a terminal run exits `0` and performs no work (asserted via event count).
3. `run resume` while another connection holds the lease exits `4` and prints the owner.
4. SIGTERM during a branch leaves the run `paused` and resumable (subprocess test).
5. Invalid definition exits `5` and names the offending field path.
6. `run artifacts --out` with a corrupted file exits `6`.
