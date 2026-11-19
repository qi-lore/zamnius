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
| `state_dir`, `log_level`, `log_format`, `max_concurrency` | config | flag > env > `--config` > discovered > default | no |
| `ZAMNIUS_MODEL_API_KEY` | secrets | environment only | never; redacted before persistence |
| `LeaseTTL`, `Owner` | library | `engine.Options` (no file key yet — see deviations) | lease row only |

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
- **Known deviations, accepted as follow-ups** (each its own change, none blocking this ADR):
  1. `model.base_url` is still accepted in the definition and ignored — remove it from the schema or
     make it win; today it is a silent trap.
  2. The concurrency default is declared twice (`config.go:40`, `engine.go:30`), both `8`, with no
     test tying them.
  3. Both surfaces implement strict YAML/JSON/TOML decoding, and they have diverged: only
     `internal/config` refuses duplicate JSON keys. A shared decoder would remove the divergence;
     validation — the part that enforces promises — stays in `definition`.
  4. `LeaseTTL` and `Owner` are reachable only through the library, while `max_concurrency` is
     reachable three ways; decide whether host policy becomes a config key or is declared
     library-only.
  5. `contracts/cli.md` §4 omitted `ZAMNIUS_MAX_CONCURRENCY`; corrected alongside this ADR.
- **Concurrency is not snapshotted**, so a resumed run may execute with a different concurrency than
  the original. Accepted: it changes scheduling, not committed outcomes. Stated here so nobody
  "fixes" it by snapshotting a scheduling knob.
- Two surfaces still mean two decoders and two validation paths; the cost is contained by ADR-0004's
  single rule per concern and revisited only if a third surface appears.

## Alternatives considered

- **One surface** (everything in the workflow definition): rejected — `state_dir` and `log_level`
  would become run semantics, get snapshotted, and drift-refuse a resume because a log level changed.
- **Three layers, adding a "policy" layer** for retries/budgets/joins: rejected — no second consumer
  exists; policies are values plus pure decisions inside the logic surface, not a configuration tier.
- **Leave `base_url` in both surfaces**: rejected — silent divergence is precisely the failure this
  ADR exists to prevent.
