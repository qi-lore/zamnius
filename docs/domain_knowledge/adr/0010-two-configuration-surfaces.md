# ADR-0010: Two configuration surfaces — workflow definition and process settings

**Status**: accepted · 2026-11-19

## Context

Zamnius accepts configuration through two independent surfaces that were designed separately:

- **The workflow definition** (`internal/definition`): 34 distinct properties, 51 occurrences across
  10 schema definitions plus the top level, authored in YAML, JSON, or TOML (or a programmatic
  builder), snapshotted with the run and drift-checked on resume.
- **Process settings** (`internal/config`): four keys — `state_dir`, `log_level`, `log_format`,
  `max_concurrency` — resolved from five sources under one documented precedence
  (flags > environment > `--config` > discovered file > built-in default).

A third category exists in practice: **credentials**, read from the environment only
(`ZAMNIUS_MODEL_API_KEY`, `ZAMNIUS_MODEL_BASE_URL`), never written to state, redacted before
persistence (constitution V).

Nothing in the repository states which setting belongs to which surface, and the first symptom has
already appeared: `model.base_url` is accepted by the schema and URI-validated
(`internal/definition/validate.go:382`), yet nothing reads it — the adapter is constructed from
`ZAMNIUS_MODEL_BASE_URL` (`internal/app/app.go:128`). One setting, two surfaces, one silently
ignored. A contributor adding the 35th property has no rule to follow.

## Decision

**Two surfaces, one test.** A setting belongs to *logic* if the run snapshots it; to *config* if it
varies per machine or process and is never recorded in the run; to *secrets* if it is a credential.

The test, in order:

1. **Does it change what is committed or refused for a run?** → logic surface (workflow definition).
2. **Does it change where or how the process runs?** → config surface (process settings).
3. **Is it a credential?** → environment only; never a file, never persisted, always redacted.

### Ownership

| Setting | Surface | Sources | In the run snapshot |
|---|---|---|---|
| Workflow semantics — `version`, `name`, `description`, `phases` (`mode`, `agent`, `branches`, `join`, `depends_on`, `when`, `retry`, `timeout`, `budget`, `on_failure`, `outputs`), `defaults`, `budget` | logic | definition file (YAML/JSON/TOML) or builder | **yes** — fingerprinted, drift refused with exit `5` |
| Per-agent model selection — `model.provider`, `model.name`, `model.temperature`, `model.max_output_tokens` | logic | definition file | **yes** |
| Model endpoint — `base_url` | config | `ZAMNIUS_MODEL_BASE_URL` | no |
| `state_dir`, `log_level`, `log_format`, `max_concurrency`, `lease_ttl` | config | flag > env > `--config` > discovered > default | no |
| `ZAMNIUS_MODEL_API_KEY` | secrets | environment only | never; redacted before persistence |
| `Owner` | library | `engine.Options` (a process identity, not a preference — see below) | lease row only |

### Interface rules that keep the surfaces apart

- **Ports are declared by the consumer**, in the package that needs them: `Store`, `Artifacts`,
  `Model`, `Models`, `Observer` in `internal/engine` (`engine.go:38–75`). An interface used only by
  an adapter stays private to that adapter.
- **Mechanism leaves through function seams, policy stays.** `Clock`, `IDSource`, and `Sleep` are
  injected because time, randomness, and waiting are not the product's to own — but *when to retry*
  is logic, and `Sleep` injection is what makes a 30-second lease testable in microseconds.
- **The exposed contract is a use-case surface**, not a type zoo: `Engine.Start/Resume/Status/
  Artifacts/Verify` plus the commit units (`AttemptCommit`, `PhaseCommit`, `RunCommit`) so a
  third-party store has something implementable.
- **Adapters own their mechanics**: decoding three formats, flag parsing, exit-code mapping, log
  formatting and redaction, SQL, provider payloads. The domain imports no transport.

## Consequences

- A contributor now has a checklist instead of a judgement call: the three questions above, and the
  table to extend.
- **Deviations found while writing this ADR, and how each was closed** (2026-11-19, one commit each;
  the rule above is the decision, these are the consequences of applying it):
  1. `model.base_url` was accepted by the definition schema and ignored. Closed by **refusing** it:
     validation rejects a definition that sets the key and the hint names `ZAMNIUS_MODEL_BASE_URL`.
     The struct field survives, documented as legacy, because a v0.1.0 snapshot can carry it and those
     runs must still resume — `FromSnapshot` refuses unknown fields on purpose, being the one document
     that is program output rather than operator input.
  2. The concurrency default was declared twice (`config.go`, `engine.go`), both `8`, with nothing
     tying them. Closed by defining it once in `domain.DefaultMaxConcurrency` and aliasing it from both
     surfaces, so the two can no longer disagree in a routine edit.
  3. The surfaces implemented strict decoding separately and had diverged: only the process settings
     refused a duplicate JSON key. Closed by `internal/strict`, used by both; validation — the part
     that enforces promises — stayed in `definition`.
  4. `LeaseTTL` was reachable only through the library while `max_concurrency` was reachable through
     a config file and the environment. Resolved by applying the three questions: a lease window
     changes *how* a process runs, not what is committed or refused, so it became the process setting
     `lease_ttl` (`ZAMNIUS_LEASE_TTL`, minimum `1s`, default `30s`) and reaches the engine through
     `zamnius.Options.LeaseTTL`. `Owner` stays library-only: it is a process identity that takeover
     evidence is *about*, so an operator must not be able to present another process's name in it.
  5. `contracts/cli.md` §4 omitted `ZAMNIUS_MAX_CONCURRENCY`; corrected alongside this ADR.
- **Concurrency is not snapshotted**, so a resumed run may execute with a different concurrency than
  the original. Accepted: it changes scheduling, not committed outcomes. Stated here so nobody
  "fixes" it by snapshotting a scheduling knob. The same holds for `lease_ttl`.
- Decoding is now one shared implementation, but each surface keeps **its own validator**, which is
  the intended shape: the definition enforces what a workflow may say, the process settings enforce
  what a machine may be told. A third surface would revisit this ADR.

## Alternatives considered

- **One surface** (everything in the workflow definition): rejected — `state_dir` and `log_level`
  would become run semantics, get snapshotted, and drift-refuse a resume because a log level changed.
- **Three layers, adding a "policy" layer** for retries/budgets/joins: rejected — no second consumer
  exists; policies are values plus pure decisions inside the logic surface, not a configuration tier.
- **Leave `base_url` in both surfaces**: rejected — silent divergence is precisely the failure this
  ADR exists to prevent.
