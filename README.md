# Zamnius

A recoverable multi-agent workflow framework in Go.

You describe a workflow as an ordered list of phases. Zamnius validates it, runs the phases in
order, fans a phase out into concurrent branches, joins their results, and records every step in a
durable, verifiable audit trail. If the process dies mid-run — `kill -9`, a reboot, a provider
outage — a fresh process resumes from the same state, reuses the work that already committed, and
never repeats a recorded side effect.

This is the first increment: ordered phases with fan-out/join, recoverability, and audit
reconstruction. Roles, the task board, messaging, memory, tools/MCP, and the HTTP/gRPC/A2A/AG-UI
surfaces are deferred ([scope](#scope-of-this-increment)).

---

## Requirements

| Requirement | Notes |
|---|---|
| Go 1.26 or newer | `go.mod` floor is 1.26; built and tested on 1.27.3, CI runs 1.27.4 |
| No C toolchain | the SQLite driver is pure Go, so `CGO_ENABLED=0` builds work |
| A POSIX filesystem | atomic `rename` and `fsync` are load-bearing for recoverability |
| An OpenAI-compatible endpoint and a key | the only model adapter in this increment |

## Build

```bash
git clone https://github.com/qi-lore/zamnius && cd zamnius
go build ./...                              # compiles everything
go build -o bin/zamnius ./cmd/zamnius       # the CLI
./bin/zamnius version
# zamnius 0.1.0 (api_version "1", schema 1)
```

`go install github.com/qi-lore/zamnius/cmd/zamnius@v0.1.0` installs the CLI without a checkout; the
build below compiles the same source. The repository is self-contained: `go test ./... -count=1`
needs no network and no provider key.

A [`Makefile`](Makefile) wraps the same commands: `make help` lists the targets, `make bin` builds the
CLI into `bin/zamnius`, `make verify` runs the CI gate in order (build, vet, tests, race, the
recoverability suite ten times, pure-Go build), and `make check` adds the `gofmt` check and the
linter ahead of it.

## Point it at a model

Two environment variables configure the provider:

| Variable | Required | Meaning |
|---|---|---|
| `ZAMNIUS_MODEL_API_KEY` | yes | the credential; never logged, never persisted, never written into an artifact |
| `ZAMNIUS_MODEL_BASE_URL` | no | provider root, default `https://api.openai.com/v1`; `/chat/completions` is appended |

```bash
export ZAMNIUS_MODEL_API_KEY=sk-…
# export ZAMNIUS_MODEL_BASE_URL=http://localhost:8080/v1   # any OpenAI-compatible endpoint
```

Without a key a workflow command refuses to start with exit `5` and a hint naming the variable:

```console
$ bin/zamnius run start --file examples/workflows/hello.yaml
zamnius: … the model adapter cannot be constructed: configuration is invalid …
hint: set ZAMNIUS_MODEL_API_KEY in the environment; the key is never read from a workflow definition
$ echo $?
5
```

Anything that answers `POST /chat/completions` with the OpenAI chat-completions shape works — a local
stub, a gateway, or the real service. A definition names its provider as `openai-compatible` and its
model by name; the provider root comes from the environment, not from the definition.

## Your first run

`examples/workflows/hello.yaml` is a one-phase workflow:

```yaml
version: "1"
name: hello
description: One single-mode phase that declares a produced plan.
phases:
  - name: greet
    mode: single
    agent:
      name: writer
      instructions: Write one short greeting for the subject named in the input.
      model:
        provider: openai-compatible
        name: gpt-4.1-mini
        max_output_tokens: 128
    outputs:
      - plan
```

Run it:

```console
$ bin/zamnius run start --file examples/workflows/hello.yaml
definition: hello (1 phase), state: ./.zamnius
time=… level=INFO msg="run started" run_id=cf6c1dac-… definition=hello
time=… level=INFO msg="run finished" run_id=cf6c1dac-… status=succeeded
run cf6c1dac-d3c8-41bc-aa42-6812a359a9e2 finished: succeeded (1 phase, 1 attempt, 1 artifact)
$ echo $?
0
```

What just happened:

- `outputs` declares the artifact kinds the phase promises; a phase only succeeds once each declared
  kind has a committed artifact. The accepted kinds are `plan`, `diff`, `report`, `evidence`,
  `aggregate`, `log`, and `custom:<name>`.
- The artifact is stored content-addressed under the state directory, and its row records the kind,
  media type, byte size, digest, and producing phase.
- Every step — `run.created`, `phase.started`, `attempt.succeeded`, `artifact.committed`,
  `run.succeeded` — is an event in the run's log, and each commit that made a non-trivial choice also
  writes a decision record.

Log lines go to **stderr**; results go to **stdout**, so `bin/zamnius run start … > out.json` with
`--json` gives you exactly one parseable object.

## The same workflow in three formats

`--file` accepts YAML, JSON, and TOML. `examples/workflows/` carries the same workflow written three
ways, and the run is equivalent in each: same phase count, same artifact kinds.

| Format | Extensions | Example |
|---|---|---|
| YAML | `.yaml`, `.yml` | [`hello.yaml`](examples/workflows/hello.yaml) |
| JSON | `.json` | [`hello.json`](examples/workflows/hello.json) |
| TOML | `.toml` | [`hello.toml`](examples/workflows/hello.toml) |

Decoding is strict in every format: an unknown field is refused, and a duplicated key in one JSON
object is an error rather than a silent last-one-wins. A refused definition exits `5` and names the
offending field path:

```console
$ bin/zamnius run start --file examples/workflows/invalid.yaml
zamnius: … workflow definition is invalid …
hint: phases[0].mode: must be "single" or "fanout", got "parallel"
$ echo $?
5
```

## Fan out, then join

[`examples/workflows/fanout.yaml`](examples/workflows/fanout.yaml), in full: one fan-out phase with
three branches joined by `all`, then a summary phase that depends on the join (`join.rule` accepts
`all`, `any`, or `n_of_m`).

```yaml
version: "1"
name: fanout
description: A fan-out phase with three branches joined by "all", then a summary phase.
phases:
  - name: review
    mode: fanout
    join:
      rule: all
    branches:
      - name: correctness
        agent:
          name: correctness
          instructions: Review the change for correctness defects and note each one.
          model:
            provider: openai-compatible
            name: gpt-4.1-mini
            max_output_tokens: 256
      - name: security
        agent:
          name: security
          instructions: Review the change for security weaknesses and note each one.
          model:
            provider: openai-compatible
            name: gpt-4.1-mini
            max_output_tokens: 256
      - name: performance
        agent:
          name: performance
          instructions: Review the change for performance risks and note each one.
          model:
            provider: openai-compatible
            name: gpt-4.1-mini
            max_output_tokens: 256
    outputs:
      - aggregate
  - name: summarise
    mode: single
    depends_on:
      - review
    agent:
      name: summariser
      instructions: Summarise the review findings into one short report.
      model:
        provider: openai-compatible
        name: gpt-4.1-mini
        max_output_tokens: 256
    outputs:
      - report
```

```console
$ bin/zamnius run start --file examples/workflows/fanout.yaml
definition: fanout (2 phases), state: ./.zamnius
run 1d0dee25-2814-4482-b460-cd8549caa550 finished: succeeded (2 phases, 4 attempts, 5 artifacts)
$ bin/zamnius run status 1d0dee25-2814-4482-b460-cd8549caa550
run 1d0dee25-2814-4482-b460-cd8549caa550
status: succeeded
current phase: summarise
  review               succeeded  3 attempts
  summarise            succeeded  1 attempt
spent: 4 attempts, 48 tokens in, 24 tokens out
next: nothing; the run is complete
last events:
      29 attempt.succeeded        agent:summariser (summarise)
      30 artifact.committed       agent:summariser (summarise)
      31 phase.succeeded          system (summarise)
      32 artifact.committed       system (summarise)
      33 run.succeeded            system
```

The join produced one `aggregate` artifact listing the branch results in declared order, and the
summary phase started only after it committed.

## Inspect what a run produced

```console
$ bin/zamnius run artifacts 1d0dee25-2814-4482-b460-cd8549caa550
aggregate cf16d669… 9 bytes review
aggregate 65765fa8… 9 bytes review
aggregate ca713d53… 9 bytes review
aggregate 98e8f411… 433 bytes review
report    d18abd10… 9 bytes summarise
5 artifacts of run 1d0dee25-2814-4482-b460-cd8549caa550
```

(digests are abbreviated above and below; the command prints them in full)

With `--out` the command copies the artifacts out, re-hashing each file against its recorded digest
before it writes. A file whose bytes no longer match its fingerprint aborts the export with exit `6`
and writes nothing, so a corrupt copy can never look like a verified one.

```console
$ bin/zamnius run artifacts 1d0dee25-2814-4482-b460-cd8549caa550 --out out
wrote out/aggregate-98e8f411596e2067f59d5345235de64a3e76a94467119129c5eea2fa57b6ab44
…
exported 5 artifacts to out
```

## Interrupt it, then resume

This is the point of the framework. [`examples/workflows/slow.yaml`](examples/workflows/slow.yaml) is
a two-branch fan-out built for interruption drills: start it, kill the process while a branch is in
flight, then resume from a new process. (The id below is the one that drill produced — yours will
differ.)

```console
$ bin/zamnius run start --file examples/workflows/slow.yaml &
$ kill -9 %1
$ bin/zamnius run status cc146e73-a19d-4ad0-a75d-e81c3f0135a3
run cc146e73-a19d-4ad0-a75d-e81c3f0135a3
status: running
current phase: draft
  draft                running    0 attempts
spent: 0 attempts, 0 tokens in, 0 tokens out
next: resume the run; phase draft was interrupted
last events:
       3 phase.started            system (draft)
       4 attempt.started          agent:details (draft)
       5 attempt.started          agent:outline (draft)
       6 attempt.succeeded        agent:details (draft)
       7 artifact.committed       agent:details (draft)
```

The run is left `running`, and the log already shows the `details` branch committed. A `kill -9`
releases nothing, so the dead process still holds the ownership lease for up to 30 seconds; a resume
inside that window is refused with exit `4` and a hint naming the owner and its expiry:

```console
$ bin/zamnius run resume cc146e73-a19d-4ad0-a75d-e81c3f0135a3
zamnius: … run is owned by another live process …
hint: run cc146e73-… is owned by MacBook-Air.local:40937 until 2026-11-17T13:05:21Z; resume it after that lease expires, or stop the owning process
$ echo $?
4

$ sleep 30 && bin/zamnius run resume cc146e73-a19d-4ad0-a75d-e81c3f0135a3
run cc146e73-a19d-4ad0-a75d-e81c3f0135a3 finished: succeeded (1 phase, 2 attempts, 3 artifacts)
$ echo $?
0
```

The resumed process took over the expired lease, abandoned the attempt that died, and ran only the
branch that had not committed. This is the store read directly — the `events` table in order:

```console
$ sqlite3 .zamnius/zamnius.db "select seq, type, actor, coalesce(phase,'') from events where substr(run_id,1,8)='cc146e73' order by seq"
1|run.created|system|
2|run.started|system|
3|phase.started|system|draft
4|attempt.started|agent:details|draft
5|attempt.started|agent:outline|draft
6|attempt.succeeded|agent:details|draft
7|artifact.committed|agent:details|draft
8|run.taken_over|system|
9|attempt.abandoned|system|draft
10|run.resumed|system|
11|phase.started|system|draft
12|attempt.started|agent:outline|draft
13|attempt.succeeded|agent:outline|draft
14|artifact.committed|agent:outline|draft
15|phase.succeeded|system|draft
16|artifact.committed|system|draft
17|artifact.committed|system|draft
18|artifact.committed|system|draft
19|run.succeeded|system|
```

Read the middle of it and the recovery is visible: the `details` branch committed at seq 6 and is
never started again, seq 8–10 record the takeover, the abandonment, and the resume, and only the
unfinished `outline` branch restarts. Three decision records explain the same choices: `takeover`
(the lease taken over from the dead owner), `resume_restart` (which attempt was abandoned, and why),
and `join` (how the branch results were combined).

Other ways a run stops, all resumable the same way:

- **Ctrl-C / SIGTERM** cancels the context, commits the current attempt boundary, releases the lease,
  and leaves the run `paused` with exit `7`.
- **A provider outage** retries per the phase's `retry` policy (`max_attempts`, `backoff`,
  `retry_on`), then applies its `on_failure` policy: `continue` records the phase as `degraded` and
  the run proceeds, `abort` and `escalate` stop the run as `failed` with exit `3`. Either way the
  choice is written to a `failure_policy` decision record.

## Machine-readable output and exit codes

Every command takes `--json` and emits exactly one object on stdout. The same fan-out run as above,
trimmed — `last_events` carries five entries of which two are shown, and the `artifacts` array is
left out here:

```json
{
  "api_version": "1",
  "command": "run.status",
  "ok": true,
  "run": {
    "id": "1d0dee25-2814-4482-b460-cd8549caa550",
    "status": "succeeded",
    "current_phase": "summarise",
    "phases": [
      { "name": "review", "status": "succeeded", "attempts": 3, "artifacts": 4 },
      { "name": "summarise", "status": "succeeded", "attempts": 1, "artifacts": 1 }
    ],
    "attempts": 4,
    "budget": {},
    "spent": { "attempts": 4, "wall_clock_ms": 14035, "tokens_in": 48, "tokens_out": 24 },
    "next_action": "nothing; the run is complete",
    "last_events": [
      { "seq": 29, "type": "attempt.succeeded", "ts": "2026-11-17T13:04:45.385798000Z", "actor": "agent:summariser", "phase": "summarise" },
      { "seq": 30, "type": "artifact.committed", "ts": "2026-11-17T13:04:45.385798000Z", "actor": "agent:summariser", "phase": "summarise" },
      …
    ]
  },
  "artifacts": [ … ],
  "error": null
}
```

`api_version` is versioned separately from the binary, so a script can pin the shape it parses. On
failure `ok` is `false` and `error` carries `class`, `message`, and `hint` — the hint names the
setting, file, or digest to act on.

| Exit | Meaning |
|---|---|
| `0` | the command completed; the run reached a terminal state |
| `1` | the command failed before or during execution |
| `2` | usage error: unknown flag or missing argument |
| `3` | the run reached `failed` |
| `4` | the run is owned by another live process |
| `5` | the definition or the configuration is invalid |
| `6` | persistence integrity failure: a fingerprint mismatch, a broken event chain, or a schema this build refuses to migrate |
| `7` | cancelled by SIGINT/SIGTERM; the run is left `paused` and resumable |

The full contract — flags, envelope, and exit table — is
[`contracts/cli.md`](specs/001-multi-agent-framework/contracts/cli.md).

## The state directory

Everything a run persists lives under one directory, `./.zamnius` by default:

```text
.zamnius/
  zamnius.db                          runs, phases, attempts, events, decisions
  artifacts/<2 hex>/<sha256>          content-addressed artifact files
```

The directory is created owner-only (`0o750`): it holds the audit record, and a world-readable trail
is a disclosure risk.

| Setting | Source |
|---|---|
| state directory | `--state-dir`, `ZAMNIUS_STATE_DIR`, `state_dir` in a config file, default `./.zamnius` |
| log level | `ZAMNIUS_LOG_LEVEL`, `log_level` — `debug`, `info` (default), `warn`, `error` |
| log format | `ZAMNIUS_LOG_FORMAT`, `log_format` — `text` (default) or `json` |
| branch concurrency | `ZAMNIUS_MAX_CONCURRENCY`, `max_concurrency` — default 8 |

A config file is found in the working directory under one of `zamnius.yaml`, `.zamnius.yaml`,
`zamnius.json`, `zamnius.toml`, or named explicitly with `--config`. Precedence is flags >
environment > `--config` > discovered file > defaults. Two config files in one directory is an error
rather than a coin toss, and a refused value names the setting and the accepted values:

```console
$ bin/zamnius run status <run-id>
zamnius: … configuration is invalid …
hint: multiple configuration files found in "…": …/zamnius.yaml, …/.zamnius.yaml; keep exactly one or pass --config
$ echo $?
5
```

Because the audit trail is the product, every write to it is a transaction: an attempt either commits
fully or is abandoned with a recorded reason, an artifact is either fully present or fully absent, and
ownership changes are recorded as decisions.

## Embed it as a library

The engine is importable at `github.com/qi-lore/zamnius/pkg/zamnius`. Every dependency is injected
at construction — no package-level defaults, no `init()` configuration — so a test can freeze the
clock, script the model, and assert the resulting event sequence. This is the shape of the entry
point; the required dependencies are the three ports:

```go
import "github.com/qi-lore/zamnius/pkg/zamnius"

engine, err := zamnius.New(zamnius.Options{
    Store:     store,          // StateStore      — durable run state   (required)
    Artifacts: artifacts,      // ArtifactStore   — content-addressed store (required)
    Models:    registry,       // ModelRegistry   — provider name to client (required)
    Logger:    logger,         // *slog.Logger    (optional; discards by default)
    Clock:     clock,          // func() time.Time   (optional; tests inject a frozen clock)
    IDSource:  ids,            // func() uuid.UUID   (optional; tests inject a counter)
})
```

`Start`, `Resume`, `Status`, `Artifacts`, and `Verify` are the operations; `Engine.Verify` recomputes
the event chain and every artifact fingerprint and reports what disagrees. The stable surfaces,
behavioural guarantees, and the semver policy are in
[`contracts/library-api.md`](specs/001-multi-agent-framework/contracts/library-api.md).

## Tests

```bash
go build ./...
go vet ./...
go test ./... -count=1
go test ./... -race -count=1
CGO_ENABLED=0 go build ./...
```

`test/recoverability` interrupts runs at four commit boundaries, with a real `SIGKILL` child process,
and asserts no committed work is repeated; `test/audit` reconstructs the log and verifies the chain;
`test/secrets` runs the whole offline corpus with a canary key and asserts it never reaches an
artifact, event, decision, log line, or envelope. The suites use in-process providers, so no network
and no key are required. CI runs the five commands above plus `go test ./test/recoverability
-count=10` (no flakes tolerated) — see [`.github/workflows/ci.yml`](.github/workflows/ci.yml).

## Examples and where the truth lives

| Path | What it is |
|---|---|
| [`examples/workflows/`](examples/workflows/README.md) | runnable definitions, each annotated with the behaviour it demonstrates: one workflow in three formats, a fan-out, a budget-bounded run, a delay-heavy run for interruption drills, and a deliberately invalid one |
| [`specs/001-multi-agent-framework/spec.md`](specs/001-multi-agent-framework/spec.md) | what the framework must do, and why, with the decisions made while drafting |
| [`specs/001-multi-agent-framework/quickstart.md`](specs/001-multi-agent-framework/quickstart.md) | the runnable validation scenarios per user story |
| [`specs/001-multi-agent-framework/contracts/`](specs/001-multi-agent-framework/contracts/) | the schemas and interfaces: the workflow definition schema, the CLI, the library API, the model client, the store |
| [`specs/001-multi-agent-framework/tasks.md`](specs/001-multi-agent-framework/tasks.md) | the task list and the per-task implementation status |
| [`docs/domain_knowledge/CONTEXT.md`](docs/domain_knowledge/CONTEXT.md) | the canonical glossary: use these words (phases, branches, attempts, artifacts, events) |
| [`docs/domain_knowledge/adr/`](docs/domain_knowledge/adr/) | why the load-bearing decisions are what they are |

## Scope of this increment

Delivered: ordered phases, single and fan-out modes with `all`/`any`/`n_of_m` joins, per-run and
per-phase budgets (attempts, tokens, wall clock), retries with backoff, phase timeouts,
`on_failure` policies that are recorded as decisions (`continue` leaves a phase `degraded` and the
run proceeds; `abort` and `escalate` stop the run), crash-safe resume, single-host ownership with
recorded takeover, content-addressed artifacts, an event log with decision records, definition
snapshots that refuse drift, and the `run start|resume|status|artifacts` CLI over an embeddable
engine. Everything a definition accepts is in
[`contracts/workflow-definition.schema.json`](specs/001-multi-agent-framework/contracts/workflow-definition.schema.json).

Deferred to follow-on increments: roles, the task board, inter-agent messaging, memory systems,
tools/MCP, HTTP and gRPC servers, A2A and AG-UI surfaces, and cross-host ownership.

## License

MIT — see [LICENSE](LICENSE).
